//! Reactorcide egress proxy: the route out for job containers when egress
//! control is on. It ends TLS, removes secrets, puts secrets in place of
//! placeholders only for the hosts of their grants, runs WASM plugins, and
//! records each flow.

pub mod ask;
pub mod ca;
pub mod config;
pub mod detect;
pub mod dns;
pub mod flow;
pub mod jobs;
pub mod net;
pub mod plugins;
pub mod policy;
pub mod proxy;
pub mod records;
pub mod scan;
pub mod server;
pub mod ssh;

/// Unix time in milliseconds.
pub fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}
