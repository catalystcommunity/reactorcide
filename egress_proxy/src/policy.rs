//! The global proxy policy of the operator: a JSON file with no secret
//! values. It lists the destinations that all jobs can use, the hosts of each secret, the decode depth, and
//! the plugin grants.

use std::path::Path;

use serde::Deserialize;

#[derive(Debug, Clone, Deserialize, Default)]
#[serde(deny_unknown_fields)]
pub struct Policy {
    /// HTTP and TLS destinations. The proxy ends TLS and inspects them.
    #[serde(default)]
    pub destinations: Vec<Destination>,
    /// Layer 4 only destinations: the proxy copies bytes and does not
    /// inspect them. Use only for protocols that are not HTTP.
    #[serde(default)]
    pub passthrough: Vec<Destination>,
    /// The hosts where each secret can go.
    #[serde(default)]
    pub secrets: Vec<SecretPolicy>,
    /// The number of nested encodings that a request can have. A request
    /// with more is stopped.
    #[serde(default = "default_depth")]
    pub decode_depth: u32,
    #[serde(default)]
    pub decode_exceptions: Vec<DecodeException>,
    /// Hosts whose large responses go to the client without inspection.
    #[serde(default)]
    pub stream_hosts: Vec<String>,
    /// Hosts whose event streams (text/event-stream, such as the streamed
    /// answers of a model provider) go to the client one event at a time.
    /// The proxy scans each event for secrets; plugins do not see them.
    #[serde(default)]
    pub event_stream_hosts: Vec<String>,
    /// The largest body that the proxy reads into memory.
    #[serde(default = "default_max_body")]
    pub max_body: usize,
    #[serde(default)]
    pub plugins: Vec<PluginGrant>,
    /// SSH hosts. The proxy ends SSH to them and connects with the key in
    /// the secret (see ssh.rs).
    #[serde(default)]
    pub ssh_hosts: Vec<SshHost>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SshHost {
    pub host: String,
    #[serde(default = "default_ssh_port")]
    pub port: u16,
    /// The host keys of the real host, as OpenSSH public key lines.
    pub host_keys: Vec<String>,
    /// The secret with the OpenSSH private key of the proxy at the host.
    pub secret: String,
    /// The SSH user at the host.
    #[serde(default = "default_ssh_user")]
    pub user: String,
    /// Job id patterns, such as "01J*". Empty permits all jobs.
    #[serde(default)]
    pub environments: Vec<String>,
}

fn default_ssh_port() -> u16 {
    22
}
fn default_ssh_user() -> String {
    "git".into()
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Destination {
    /// A host pattern: "api.example.local", "*.example.local", or "*".
    pub host: String,
    /// Empty permits 80 and 443.
    #[serde(default)]
    pub ports: Vec<u16>,
    /// Job id patterns, such as "01J*". Empty permits all jobs.
    #[serde(default)]
    pub environments: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SecretPolicy {
    pub id: String,
    pub hosts: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DecodeException {
    pub hosts: Vec<String>,
    pub depth: u32,
}

/// The grant of one plugin. The proxy loads the file only when its SHA-256
/// is `sha256`.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PluginGrant {
    pub name: String,
    pub file: String,
    pub sha256: String,
    /// "out", "in", or both.
    pub stages: Vec<String>,
    /// Host patterns. Empty means all hosts.
    #[serde(default)]
    pub hosts: Vec<String>,
    /// The ask service operations that the plugin can call.
    #[serde(default)]
    pub ask_ops: Vec<String>,
    #[serde(default = "default_fuel")]
    pub fuel: u64,
    #[serde(default = "default_memory_mb")]
    pub memory_mb: usize,
    #[serde(default = "default_timeout_ms")]
    pub timeout_ms: u64,
}

fn default_depth() -> u32 {
    2
}
fn default_max_body() -> usize {
    16 << 20
}
fn default_fuel() -> u64 {
    2_000_000_000
}
fn default_memory_mb() -> usize {
    64
}
fn default_timeout_ms() -> u64 {
    30_000
}

impl Policy {
    pub fn load(path: &Path) -> anyhow::Result<Self> {
        let p: Policy = serde_json::from_slice(&std::fs::read(path)?)
            .map_err(|e| anyhow::anyhow!("policy {}: {e}", path.display()))?;
        p.validate()?;
        Ok(p)
    }

    /// An empty policy with the defaults of the file format (no
    /// destinations, decode depth 2, max body 16 MiB).
    pub fn default_valid() -> Self {
        serde_json::from_str("{}").expect("empty policy")
    }

    pub fn validate(&self) -> anyhow::Result<()> {
        for g in &self.plugins {
            if g.name.is_empty() || g.stages.is_empty() || g.stages.iter().any(|s| s != "out" && s != "in") {
                anyhow::bail!("plugin {:?}: name and stages (\"out\", \"in\") are necessary", g.name);
            }
            if g.sha256.len() != 64 || hex::decode(&g.sha256).is_err() {
                anyhow::bail!("plugin {}: sha256 must be 64 hex digits", g.name);
            }
        }
        for s in &self.ssh_hosts {
            if s.host.contains('*') || s.host_keys.is_empty() || s.secret.is_empty() {
                anyhow::bail!("ssh host {:?}: an exact host, host_keys, and secret are necessary", s.host);
            }
        }
        if self.plugins.iter().map(|g| &g.name).collect::<std::collections::HashSet<_>>().len() != self.plugins.len() {
            anyhow::bail!("plugin names must be unique");
        }
        Ok(())
    }

    /// The destination rule for an inspected flow.
    pub fn destination(&self, env: &str, host: &str, port: u16) -> bool {
        find(&self.destinations, env, host, port)
    }

    pub fn passthrough(&self, env: &str, host: &str, port: u16) -> bool {
        find(&self.passthrough, env, host, port)
    }

    /// True when a destination or passthrough rule names `host` for the
    /// environment, on any port (for DNS queries of that name).
    pub fn names(&self, env: &str, host: &str) -> bool {
        self.destinations.iter().chain(&self.passthrough).any(|d| {
            d.host != "*" && host_match(&d.host, host) && (d.environments.is_empty() || d.environments.iter().any(|e| glob(e, env)))
        })
    }

    /// The SSH host entry for a destination (exact host name).
    pub fn ssh_host(&self, host: &str, port: u16) -> Option<&SshHost> {
        self.ssh_hosts.iter().find(|s| s.port == port && host_match(&s.host, host) && !s.host.contains('*'))
    }

    /// The SSH host entry that an environment can use.
    pub fn ssh_host_for(&self, env: &str, host: &str, port: u16) -> Option<&SshHost> {
        self.ssh_host(host, port).filter(|s| s.environments.is_empty() || s.environments.iter().any(|e| glob(e, env)))
    }

    /// True when secret `id` can go to `host`.
    pub fn secret_permitted(&self, id: &str, host: &str) -> bool {
        self.secrets.iter().any(|s| s.id == id && s.hosts.iter().any(|h| host_match(h, host)))
    }

    pub fn depth_for(&self, host: &str) -> u32 {
        self.decode_exceptions
            .iter()
            .filter(|e| e.hosts.iter().any(|h| host_match(h, host)))
            .map(|e| e.depth)
            .max()
            .unwrap_or(self.decode_depth)
    }

    pub fn streams(&self, host: &str) -> bool {
        self.stream_hosts.iter().any(|h| host_match(h, host))
    }

    pub fn event_streams(&self, host: &str) -> bool {
        self.event_stream_hosts.iter().any(|h| host_match(h, host))
    }
}

fn find(list: &[Destination], env: &str, host: &str, port: u16) -> bool {
    list.iter().any(|d| {
        host_match(&d.host, host)
            && (if d.ports.is_empty() { port == 80 || port == 443 } else { d.ports.contains(&port) })
            && (d.environments.is_empty() || d.environments.iter().any(|e| glob(e, env)))
    })
}

/// "*" matches all hosts. "*.a.b" matches each name below a.b, not a.b.
/// Other patterns match the whole name. Case does not matter.
pub fn host_match(pattern: &str, host: &str) -> bool {
    let (p, h) = (pattern.to_ascii_lowercase(), host.trim_end_matches('.').to_ascii_lowercase());
    if p == "*" {
        return true;
    }
    match p.strip_prefix("*.") {
        Some(base) => h.len() > base.len() + 1 && h.ends_with(&format!(".{base}")),
        None => p == h,
    }
}

/// A pattern with "*" at the end or alone, such as "env_*".
fn glob(pattern: &str, s: &str) -> bool {
    match pattern.strip_suffix('*') {
        Some(prefix) => s.starts_with(prefix),
        None => pattern == s,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hosts() {
        assert!(host_match("*", "a.b"));
        assert!(host_match("*.example.local", "api.example.local"));
        assert!(host_match("*.example.local", "a.b.example.local"));
        assert!(!host_match("*.example.local", "example.local"));
        assert!(!host_match("*.example.local", "badexample.local"));
        assert!(host_match("API.example.local", "api.example.local."));
        assert!(!host_match("api.example.local", "api.example.local.evil"));
    }

    #[test]
    fn destinations_and_secrets() {
        let p: Policy = serde_json::from_str(
            r#"{"destinations":[{"host":"*.example.local"},{"host":"git.example.local","ports":[22,443],"environments":["env_a*"]}],
                "secrets":[{"id":"gh","hosts":["api.example.local"]}],
                "decode_exceptions":[{"hosts":["files.example.local"],"depth":3}]}"#,
        )
        .unwrap();
        assert!(p.destination("env_x", "api.example.local", 443));
        assert!(!p.destination("env_x", "api.example.local", 8443));
        assert!(p.destination("env_ab", "git.example.local", 22));
        assert!(!p.destination("env_b", "other.local", 443));
        assert!(p.secret_permitted("gh", "api.example.local"));
        assert!(!p.secret_permitted("gh", "evil.example.local"));
        assert!(!p.secret_permitted("other", "api.example.local"));
        assert_eq!(p.depth_for("files.example.local"), 3);
        assert_eq!(p.depth_for("api.example.local"), 2);
        assert!(serde_json::from_str::<Policy>(r#"{"destination":[]}"#).is_err());
    }
}
