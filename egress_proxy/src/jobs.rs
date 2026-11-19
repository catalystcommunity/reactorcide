//! The job registry. The Reactorcide worker registers each job before the
//! job starts (control API `PUT /v1/jobs/{job_id}`) and removes it when the
//! job ends. A registration holds the identity facts of the job, the extra
//! destinations of the job, and the secret grants: placeholder, real value,
//! hosts, and the header where the value can go. The real values never go
//! into the job.

use std::collections::HashMap;
use std::sync::RwLock;

use serde::{Deserialize, Serialize};

use crate::policy::{Destination, host_match};
use crate::scan::{MIN_SECRET, PLACEHOLDER, Secrets};

/// The identity facts of a job. Unknown keys are kept, so that newer
/// coordinators can send more facts (for example ref classes or actor
/// fields) to an older proxy.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(default)]
pub struct Identity {
    pub org_id: String,
    pub project_id: String,
    pub workflow_id: String,
    pub workflow_security_id: String,
    pub node: String,
    pub actor_principal_id: String,
    pub actor_identity: String,
    pub execution_profile: String,
    pub ci_origin: String,
    pub ref_classes: Vec<String>,
    #[serde(flatten)]
    pub extra: serde_json::Map<String, serde_json::Value>,
}

impl Identity {
    /// A short text for records and plugins: "org/project/node".
    pub fn summary(&self) -> String {
        [self.org_id.as_str(), self.project_id.as_str(), self.node.as_str()].join("/")
    }
}

fn default_header() -> String {
    "authorization".into()
}

/// One secret grant of a job. It has no Debug: the value must not go to a
/// log.
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Grant {
    pub id: String,
    #[serde(default)]
    pub secret_ref: String,
    pub placeholder: String,
    pub value: String,
    pub hosts: Vec<String>,
    #[serde(default = "default_header")]
    pub header: String,
    #[serde(default)]
    pub format: Option<String>,
}

impl Grant {
    pub fn permits(&self, host: &str) -> bool {
        self.hosts.iter().any(|h| host_match(h, host))
    }
}

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct JobPolicy {
    pub job_id: String,
    #[serde(default)]
    pub identity: Identity,
    #[serde(default)]
    pub destinations: Vec<Destination>,
    #[serde(default)]
    pub grants: Vec<Grant>,
    /// Unix milliseconds. 0: no expiry.
    #[serde(default)]
    pub expires_at_ms: u64,
}

impl JobPolicy {
    pub fn validate(&self, path_job: &str) -> Result<(), String> {
        if self.job_id.is_empty() || self.job_id != path_job {
            return Err("job_id must be the job id of the path".into());
        }
        let mut seen = std::collections::HashSet::new();
        for g in &self.grants {
            if g.id.is_empty() {
                return Err("each grant must have an id".into());
            }
            if !PLACEHOLDER.find(g.placeholder.as_bytes()).is_some_and(|m| m.as_bytes() == g.placeholder.as_bytes()) {
                return Err(format!("grant {}: the placeholder is not valid", g.id));
            }
            if !seen.insert(g.placeholder.clone()) {
                return Err(format!("grant {}: the placeholder is used two times", g.id));
            }
            if g.value.is_empty() {
                return Err(format!("grant {}: the value is empty", g.id));
            }
            // The scanner does not look for a shorter value, so it could
            // leave in a response or a request without detection.
            if g.value.len() < MIN_SECRET {
                return Err(format!("grant {}: the value is shorter than {MIN_SECRET} bytes", g.id));
            }
            if g.value.contains(&g.placeholder) {
                return Err(format!("grant {}: the value contains the placeholder", g.id));
            }
            if g.hosts.is_empty() || g.hosts.iter().any(|h| h.trim().is_empty()) {
                return Err(format!("grant {}: hosts must not be empty", g.id));
            }
            if g.header.is_empty() || http::HeaderName::from_bytes(g.header.as_bytes()).is_err() {
                return Err(format!("grant {}: the header name is not valid", g.id));
            }
            if let Some(f) = &g.format
                && f.matches("{}").count() != 1
            {
                return Err(format!("grant {}: format must contain \"{{}}\" one time", g.id));
            }
        }
        Ok(())
    }
}

/// A registered job.
pub struct JobState {
    pub policy: JobPolicy,
    /// The grant values, for the scan (secret id = grant id).
    pub secrets: Secrets,
    pub identity: String,
}

impl JobState {
    pub fn new(policy: JobPolicy) -> Self {
        let secrets = Secrets::new(policy.grants.iter().map(|g| (g.id.clone(), g.value.as_bytes().to_vec())).collect());
        let identity = policy.identity.summary();
        JobState { policy, secrets, identity }
    }

    /// True when a job destination or a grant host permits host:port.
    pub fn destination(&self, host: &str, port: u16) -> bool {
        let web = port == 80 || port == 443;
        self.policy.destinations.iter().any(|d| {
            host_match(&d.host, host) && (if d.ports.is_empty() { web } else { d.ports.contains(&port) })
        }) || (web && self.policy.grants.iter().any(|g| g.permits(host)))
    }

    /// True when a job destination or a grant names host (for DNS).
    pub fn names(&self, host: &str) -> bool {
        self.policy.destinations.iter().any(|d| d.host != "*" && host_match(&d.host, host))
            || self.policy.grants.iter().any(|g| g.hosts.iter().any(|h| h != "*" && host_match(h, host)))
    }

    fn expired(&self, now_ms: u64) -> bool {
        self.policy.expires_at_ms != 0 && now_ms >= self.policy.expires_at_ms
    }
}

#[derive(Default)]
pub struct Jobs {
    map: RwLock<HashMap<String, std::sync::Arc<JobState>>>,
}

impl Jobs {
    pub fn put(&self, p: JobPolicy) -> std::sync::Arc<JobState> {
        let s = std::sync::Arc::new(JobState::new(p));
        self.map.write().unwrap().insert(s.policy.job_id.clone(), s.clone());
        s
    }

    /// The registration of a job, when it is there and not expired.
    pub fn get(&self, job: &str) -> Option<std::sync::Arc<JobState>> {
        let now = crate::now_ms();
        let s = self.map.read().unwrap().get(job).cloned()?;
        if s.expired(now) {
            self.map.write().unwrap().remove(job);
            return None;
        }
        Some(s)
    }

    pub fn remove(&self, job: &str) -> Option<std::sync::Arc<JobState>> {
        self.map.write().unwrap().remove(job)
    }

    pub fn len(&self) -> usize {
        self.map.read().unwrap().len()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const PH: &str = "reactorcide-secret-0123456789abcdef0123456789abcdef";

    fn policy(v: serde_json::Value) -> JobPolicy {
        serde_json::from_value(v).unwrap()
    }

    fn base() -> serde_json::Value {
        serde_json::json!({
            "job_id": "job_a",
            "identity": {"org_id": "o", "project_id": "p", "node": "build", "future_fact": 7},
            "destinations": [{"host": "coord.local", "ports": [8080]}],
            "grants": [{"id": "g1", "secret_ref": "ci/gh:token", "placeholder": PH, "value": "real-secret-value",
                        "hosts": ["api.example.local"], "format": "Bearer {}"}]
        })
    }

    #[test]
    fn validates_and_keeps_unknown_identity_facts() {
        let p = policy(base());
        assert!(p.validate("job_a").is_ok());
        assert!(p.validate("job_b").is_err());
        assert_eq!(p.grants[0].header, "authorization");
        assert_eq!(p.identity.summary(), "o/p/build");
        assert_eq!(p.identity.extra["future_fact"], 7);
        let bad = |f: &dyn Fn(&mut serde_json::Value)| {
            let mut v = base();
            f(&mut v);
            policy(v).validate("job_a").is_err()
        };
        assert!(bad(&|v| v["grants"][0]["placeholder"] = "magi-secret-x".into()));
        assert!(bad(&|v| v["grants"][0]["value"] = "".into()));
        assert!(bad(&|v| v["grants"][0]["value"] = "1234567".into()));
        assert!(bad(&|v| v["grants"][0]["hosts"] = serde_json::json!([])));
        assert!(bad(&|v| v["grants"][0]["format"] = "Bearer".into()));
        assert!(bad(&|v| v["grants"][0]["format"] = "{} {}".into()));
        assert!(bad(&|v| v["grants"][0]["header"] = "bad header".into()));
        assert!(bad(&|v| {
            let g = v["grants"][0].clone();
            v["grants"].as_array_mut().unwrap().push(g);
        }));
        let mut v = base();
        v["unknown"] = 1.into();
        assert!(serde_json::from_value::<JobPolicy>(v).is_err());
    }

    #[test]
    fn destinations_names_and_expiry() {
        let jobs = Jobs::default();
        let s = jobs.put(policy(base()));
        assert!(s.destination("coord.local", 8080));
        assert!(!s.destination("coord.local", 443));
        assert!(s.destination("api.example.local", 443));
        assert!(!s.destination("api.example.local", 22));
        assert!(!s.destination("other.local", 443));
        assert!(s.names("api.example.local") && s.names("coord.local") && !s.names("other.local"));
        assert_eq!(s.secrets.get("g1"), Some(&b"real-secret-value"[..]));
        assert!(jobs.get("job_a").is_some());
        let mut v = base();
        v["expires_at_ms"] = 1.into();
        jobs.put(policy(v));
        assert!(jobs.get("job_a").is_none());
        assert!(jobs.is_empty());
    }
}
