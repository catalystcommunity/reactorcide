//! The control API. The Reactorcide worker calls it with the bearer
//! control token: it registers a job before the job starts, and removes it
//! when the job ends. JSON bodies, JSON `{"code","message"}` errors, and
//! `GET /healthz` (no token).

use std::sync::Arc;

use axum::Router;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, Query, State};
use axum::http::{HeaderMap, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, put};
use serde::Deserialize;

use crate::jobs::{JobPolicy, Jobs};
use crate::records::Records;

pub struct Server {
    pub records: Arc<Records>,
    pub jobs: Arc<Jobs>,
    /// The bearer token. Empty rejects every call.
    pub token: Vec<u8>,
}

pub fn router(s: Arc<Server>) -> Router {
    Router::new()
        .route("/v1/jobs/{job_id}", put(put_job).delete(delete_job))
        .route("/v1/flows", get(list_flows))
        .route("/healthz", get(|| async { "ok\n" }))
        .fallback(|| async { error(404, "unknown path") })
        .layer(DefaultBodyLimit::max(4 << 20))
        .with_state(s)
}

pub fn error(code: u16, message: &str) -> Response {
    let status = StatusCode::from_u16(code).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    let body = serde_json::json!({"code": code, "message": message}).to_string();
    (status, [(header::CONTENT_TYPE, "application/json")], body + "\n").into_response()
}

fn json(v: serde_json::Value) -> Response {
    ([(header::CONTENT_TYPE, "application/json")], v.to_string() + "\n").into_response()
}

/// Compares in constant time for tokens of the same length.
fn same(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

/// None when the caller has the control token; else the error response.
fn refused(s: &Server, h: &HeaderMap) -> Option<Response> {
    let token = h.get(header::AUTHORIZATION).and_then(|v| v.to_str().ok()).and_then(|v| v.strip_prefix("Bearer "));
    match token {
        _ if s.token.is_empty() => Some(error(401, "the control token is not configured")),
        None => Some(error(401, "bearer token required")),
        Some(t) if same(t.trim().as_bytes(), &s.token) => None,
        Some(_) => Some(error(401, "the token is not valid")),
    }
}

async fn put_job(State(s): State<Arc<Server>>, Path(job): Path<String>, h: HeaderMap, body: Bytes) -> Response {
    if let Some(r) = refused(&s, &h) {
        return r;
    }
    let p: JobPolicy = match serde_json::from_slice(&body) {
        Ok(p) => p,
        Err(e) => return error(400, &format!("job policy: {e}")),
    };
    if let Err(e) = p.validate(&job) {
        return error(400, &e);
    }
    let n = p.grants.len();
    let st = s.jobs.put(p);
    tracing::info!(job = %job, identity = %st.identity, grants = n, destinations = st.policy.destinations.len(), "job registered");
    json(serde_json::json!({"job_id": job, "grants": n}))
}

async fn delete_job(State(s): State<Arc<Server>>, Path(job): Path<String>, h: HeaderMap) -> Response {
    if let Some(r) = refused(&s, &h) {
        return r;
    }
    if s.jobs.remove(&job).is_none() {
        return error(404, "job is not registered");
    }
    let flows = s.records.list(Some(&job), usize::MAX);
    let blocked = flows.iter().filter(|f| f.outcome != "sent").count();
    let mut used: Vec<String> = flows.iter().flat_map(|f| f.secrets_used.clone()).collect();
    used.sort();
    used.dedup();
    tracing::info!(job = %job, flows = flows.len(), blocked, "job removed");
    json(serde_json::json!({"job_id": job, "flows": flows.len(), "blocked": blocked, "secrets_used": used}))
}

#[derive(Deserialize)]
struct FlowQuery {
    job: Option<String>,
    limit: Option<usize>,
}

async fn list_flows(State(s): State<Arc<Server>>, Query(q): Query<FlowQuery>, h: HeaderMap) -> Response {
    if let Some(r) = refused(&s, &h) {
        return r;
    }
    let limit = q.limit.unwrap_or(100).clamp(1, 1000);
    json(serde_json::json!({"flows": s.records.list(q.job.as_deref(), limit)}))
}

/// A control API listener with TLS (`--control-tls-name`). The TLS
/// handshake of each connection runs in its own task, so a slow client
/// does not stop others. The control API carries the secret values of the
/// grants, so TLS keeps them from the network between worker and proxy.
pub struct TlsListener {
    rx: tokio::sync::mpsc::Receiver<(tokio_rustls::server::TlsStream<tokio::net::TcpStream>, std::net::SocketAddr)>,
    addr: std::net::SocketAddr,
}

impl TlsListener {
    pub fn new(l: tokio::net::TcpListener, cfg: Arc<rustls::ServerConfig>) -> std::io::Result<Self> {
        let addr = l.local_addr()?;
        let (tx, rx) = tokio::sync::mpsc::channel(64);
        let acceptor = tokio_rustls::TlsAcceptor::from(cfg);
        tokio::spawn(async move {
            loop {
                let (sock, peer) = match l.accept().await {
                    Ok(v) => v,
                    Err(e) => {
                        tracing::debug!(error = %e, "control accept");
                        tokio::time::sleep(std::time::Duration::from_millis(50)).await;
                        continue;
                    }
                };
                let (acceptor, tx) = (acceptor.clone(), tx.clone());
                tokio::spawn(async move {
                    match tokio::time::timeout(std::time::Duration::from_secs(10), acceptor.accept(sock)).await {
                        Ok(Ok(tls)) => {
                            let _ = tx.send((tls, peer)).await;
                        }
                        Ok(Err(e)) => tracing::debug!(error = %e, peer = %peer, "control TLS handshake"),
                        Err(_) => tracing::debug!(peer = %peer, "control TLS handshake timeout"),
                    }
                });
            }
        });
        Ok(TlsListener { rx, addr })
    }
}

impl axum::serve::Listener for TlsListener {
    type Io = tokio_rustls::server::TlsStream<tokio::net::TcpStream>;
    type Addr = std::net::SocketAddr;

    async fn accept(&mut self) -> (Self::Io, Self::Addr) {
        match self.rx.recv().await {
            Some(v) => v,
            None => std::future::pending().await,
        }
    }

    fn local_addr(&self) -> std::io::Result<Self::Addr> {
        Ok(self.addr)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn token_compare() {
        assert!(same(b"abc", b"abc"));
        assert!(!same(b"abc", b"abd"));
        assert!(!same(b"abc", b"abcd"));
    }
}
