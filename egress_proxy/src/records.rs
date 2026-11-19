//! The flow records. They are in memory, and they also go to the log.

use std::collections::VecDeque;
use std::sync::Mutex;

use serde::Serialize;

/// The record of one flow. It has no body and no secret value.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct FlowRecord {
    pub id: String,
    pub job_id: String,
    /// The identity summary of the job ("org/project/node").
    pub identity: String,
    pub destination: String,
    pub method: Option<String>,
    pub url: Option<String>,
    pub status: Option<u64>,
    pub outcome: String,
    pub reasons: Vec<String>,
    pub plugins: Vec<String>,
    /// The grant ids (or operator secret ids) that went into the flow.
    pub secrets_used: Vec<String>,
    pub redacted: u64,
    pub created_ms: u64,
    pub duration_ms: u64,
}

pub struct Records {
    max: usize,
    list: Mutex<VecDeque<FlowRecord>>,
}

impl Records {
    pub fn new(max: usize) -> Self {
        Records { max, list: Mutex::new(VecDeque::new()) }
    }

    pub fn add(&self, r: FlowRecord) {
        tracing::info!(
            flow = %r.id, job = %r.job_id, identity = %r.identity, destination = %r.destination,
            method = r.method.as_deref().unwrap_or(""), url = r.url.as_deref().unwrap_or(""), status = r.status.unwrap_or(0),
            outcome = %r.outcome, reasons = ?r.reasons, plugins = ?r.plugins, secrets_used = ?r.secrets_used,
            redacted = r.redacted, ms = r.duration_ms, "flow"
        );
        let mut l = self.list.lock().unwrap();
        l.push_back(r);
        while l.len() > self.max {
            l.pop_front();
        }
    }

    /// Newest first.
    pub fn list(&self, job: Option<&str>, limit: usize) -> Vec<FlowRecord> {
        self.list
            .lock()
            .unwrap()
            .iter()
            .rev()
            .filter(|r| job.is_none_or(|j| r.job_id == j))
            .take(limit)
            .cloned()
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(job: &str) -> FlowRecord {
        FlowRecord {
            id: "f".into(),
            job_id: job.into(),
            identity: "o/p/n".into(),
            destination: "a:443".into(),
            method: None,
            url: None,
            status: None,
            outcome: "sent".into(),
            reasons: vec![],
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: 1,
            duration_ms: 0,
        }
    }

    #[test]
    fn keeps_the_newest() {
        let r = Records::new(2);
        r.add(rec("e1"));
        r.add(rec("e2"));
        r.add(rec("e3"));
        let all = r.list(None, 10);
        assert_eq!(all.iter().map(|f| f.job_id.as_str()).collect::<Vec<_>>(), ["e3", "e2"]);
        assert_eq!(r.list(Some("e2"), 10).len(), 1);
        assert_eq!(r.list(Some("e1"), 10).len(), 0);
        assert_eq!(r.list(None, 1).len(), 1);
    }
}
