//! HTTP clients and TLS settings.

use std::path::Path;
use std::sync::Arc;

use bytes::Bytes;
use http_body_util::Full;
use hyper_rustls::HttpsConnector;
use hyper_util::client::legacy::connect::HttpConnector;
use hyper_util::rt::TokioExecutor;
use rustls::RootCertStore;

pub type Client = hyper_util::client::legacy::Client<HttpsConnector<HttpConnector>, Full<Bytes>>;

pub fn provider() -> Arc<rustls::crypto::CryptoProvider> {
    Arc::new(rustls::crypto::ring::default_provider())
}

/// The public web roots, and the certificates in `extra` (PEM) when set.
pub fn roots(extra: Option<&Path>) -> anyhow::Result<RootCertStore> {
    let mut r = RootCertStore { roots: webpki_roots::TLS_SERVER_ROOTS.to_vec() };
    if let Some(p) = extra {
        let pem = std::fs::read(p).map_err(|e| anyhow::anyhow!("{}: {e}", p.display()))?;
        for c in rustls_pemfile::certs(&mut &pem[..]) {
            r.add(c?)?;
        }
    }
    Ok(r)
}

/// A client for HTTP and HTTPS. It does not use a proxy.
pub fn client(roots: RootCertStore) -> anyhow::Result<Client> {
    let tls = rustls::ClientConfig::builder_with_provider(provider())
        .with_safe_default_protocol_versions()?
        .with_root_certificates(roots)
        .with_no_client_auth();
    let https = hyper_rustls::HttpsConnectorBuilder::new().with_tls_config(tls).https_or_http().enable_http1().build();
    Ok(hyper_util::client::legacy::Client::builder(TokioExecutor::new()).build(https))
}
