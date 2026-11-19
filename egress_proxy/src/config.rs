//! Process settings. Each flag has an environment variable default.

use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct Settings {
    /// The control API (the worker registers jobs here).
    pub listen: String,
    /// The proxy port for jobs.
    pub proxy_listen: String,
    /// The file with the bearer token of the control API.
    pub control_token: PathBuf,
    /// The global policy. None: an empty policy.
    pub policy: Option<PathBuf>,
    pub ca_cert: PathBuf,
    pub ca_key: PathBuf,
    /// The HMAC key of the job credentials.
    pub job_key: PathBuf,
    pub secrets_dir: Option<PathBuf>,
    pub upstream_ca: Option<PathBuf>,
    pub ask_url: Option<String>,
    pub ask_token: Option<PathBuf>,
    /// The SSH host key of the proxy (OpenSSH format). None turns SSH off.
    pub ssh_host_key: Option<PathBuf>,
    pub allow_unregistered: bool,
    /// The host name (or IP) of the control API in the worker URL. When
    /// set, the control API uses TLS with a certificate for this name from
    /// the proxy CA; the worker verifies it with the CA certificate.
    pub control_tls_name: Option<String>,
}

/// `--name value` or `--name=value`, else the environment variable.
pub fn flag(args: &[String], name: &str, env: &str) -> Option<String> {
    let long = format!("--{name}");
    let mut it = args.iter();
    while let Some(a) = it.next() {
        if *a == long {
            return it.next().cloned();
        }
        if let Some(v) = a.strip_prefix(&format!("{long}=")) {
            return Some(v.to_string());
        }
    }
    if env.is_empty() {
        return None;
    }
    std::env::var(env).ok().filter(|v| !v.is_empty())
}

fn addr(a: String) -> String {
    if a.starts_with(':') { format!("0.0.0.0{a}") } else { a }
}

impl Settings {
    pub fn from_args(args: &[String]) -> anyhow::Result<Self> {
        let get = |n: &str, e: &str| flag(args, n, e);
        let need = |n: &str, e: &str| get(n, e).ok_or_else(|| anyhow::anyhow!("--{n} ({e}) is required"));
        let s = Settings {
            listen: addr(get("listen", "REACTORCIDE_EGRESS_LISTEN").unwrap_or_else(|| ":8080".into())),
            proxy_listen: addr(get("proxy-listen", "REACTORCIDE_EGRESS_PROXY_LISTEN").unwrap_or_else(|| ":3128".into())),
            control_token: need("control-token", "REACTORCIDE_EGRESS_CONTROL_TOKEN_FILE")?.into(),
            policy: get("policy", "REACTORCIDE_EGRESS_POLICY").map(PathBuf::from),
            ca_cert: need("ca-cert", "REACTORCIDE_EGRESS_CA_CERT")?.into(),
            ca_key: need("ca-key", "REACTORCIDE_EGRESS_CA_KEY")?.into(),
            job_key: need("job-key", "REACTORCIDE_EGRESS_JOB_KEY")?.into(),
            secrets_dir: get("secrets-dir", "REACTORCIDE_EGRESS_SECRETS_DIR").map(PathBuf::from),
            upstream_ca: get("upstream-ca", "REACTORCIDE_EGRESS_UPSTREAM_CA").map(PathBuf::from),
            ask_url: get("ask-url", "REACTORCIDE_EGRESS_ASK_URL"),
            ask_token: get("ask-token", "REACTORCIDE_EGRESS_ASK_TOKEN_FILE").map(PathBuf::from),
            ssh_host_key: get("ssh-host-key", "REACTORCIDE_EGRESS_SSH_HOST_KEY").map(PathBuf::from),
            allow_unregistered: get("allow-unregistered", "REACTORCIDE_EGRESS_ALLOW_UNREGISTERED")
                .is_some_and(|v| matches!(v.to_ascii_lowercase().as_str(), "1" | "true" | "yes")),
            control_tls_name: get("control-tls-name", "REACTORCIDE_EGRESS_CONTROL_TLS_NAME"),
        };
        anyhow::ensure!(s.ask_url.is_some() == s.ask_token.is_some(), "REACTORCIDE_EGRESS_ASK_URL and REACTORCIDE_EGRESS_ASK_TOKEN_FILE go together");
        Ok(s)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn flags() {
        let a: Vec<String> = ["--control-token", "/t", "--ca-cert=/c", "--ca-key", "/k", "--job-key", "/j", "--allow-unregistered", "true"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let s = Settings::from_args(&a).unwrap();
        assert_eq!(s.listen, "0.0.0.0:8080");
        assert_eq!(s.proxy_listen, "0.0.0.0:3128");
        assert_eq!(s.ca_cert, PathBuf::from("/c"));
        assert!(s.allow_unregistered && s.policy.is_none() && s.control_tls_name.is_none());
        let mut t = a.clone();
        t.extend(["--control-tls-name".to_string(), "egress.ns.svc".to_string()]);
        assert_eq!(Settings::from_args(&t).unwrap().control_tls_name.as_deref(), Some("egress.ns.svc"));
        let mut b = a.clone();
        b.extend(["--ask-url".to_string(), "http://x".to_string()]);
        assert!(Settings::from_args(&b).is_err());
    }
}
