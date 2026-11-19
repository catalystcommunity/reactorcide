//! The installation CA of the proxy. The proxy makes a certificate for
//! each destination host and signs it with this CA. Environments trust the
//! CA certificate (only), so all TLS goes through the proxy.

use std::collections::HashMap;
use std::path::Path;
use std::sync::{Arc, Mutex};

use rcgen::{BasicConstraints, CertificateParams, DistinguishedName, DnType, IsCa, Issuer, KeyPair, KeyUsagePurpose};
use rustls::ServerConfig;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};

pub struct Ca {
    issuer: Issuer<'static, KeyPair>,
    cert: CertificateDer<'static>,
    /// One key for all host certificates: making keys is slow.
    leaf_key: KeyPair,
    cache: Mutex<HashMap<String, Arc<ServerConfig>>>,
}

impl Ca {
    pub fn load(cert: &Path, key: &Path) -> anyhow::Result<Self> {
        let cert_pem = std::fs::read_to_string(cert).map_err(|e| anyhow::anyhow!("{}: {e}", cert.display()))?;
        let key_pem = std::fs::read_to_string(key).map_err(|e| anyhow::anyhow!("{}: {e}", key.display()))?;
        Self::from_pem(&cert_pem, &key_pem)
    }

    pub fn from_pem(cert_pem: &str, key_pem: &str) -> anyhow::Result<Self> {
        let key = KeyPair::from_pem(key_pem)?;
        let issuer = Issuer::from_ca_cert_pem(cert_pem, key)?;
        let cert = rustls_pemfile::certs(&mut cert_pem.as_bytes()).next().ok_or_else(|| anyhow::anyhow!("no certificate in the CA file"))??;
        Ok(Ca { issuer, cert, leaf_key: KeyPair::generate()?, cache: Mutex::new(HashMap::new()) })
    }

    /// The TLS server settings for one host, with a certificate for it.
    pub fn server_config(&self, host: &str) -> anyhow::Result<Arc<ServerConfig>> {
        let host = host.to_ascii_lowercase();
        if let Some(c) = self.cache.lock().unwrap().get(&host) {
            return Ok(c.clone());
        }
        let chain = vec![self.leaf(&host)?, self.cert.clone()];
        let key = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(self.leaf_key.serialize_der()));
        let mut cfg = ServerConfig::builder_with_provider(crate::net::provider())
            .with_safe_default_protocol_versions()?
            .with_no_client_auth()
            .with_single_cert(chain, key)?;
        cfg.alpn_protocols = vec![b"http/1.1".to_vec()];
        let cfg = Arc::new(cfg);
        let mut cache = self.cache.lock().unwrap();
        if cache.len() > 10_000 {
            cache.clear();
        }
        cache.insert(host, cfg.clone());
        Ok(cfg)
    }
}

impl Ca {
    /// The certificate of one host.
    fn leaf(&self, host: &str) -> anyhow::Result<CertificateDer<'static>> {
        let mut p = CertificateParams::new(vec![host.to_string()])?;
        p.distinguished_name = DistinguishedName::new();
        p.distinguished_name.push(DnType::CommonName, host);
        p.not_before = rcgen::date_time_ymd(2026, 1, 1);
        p.not_after = rcgen::date_time_ymd(2036, 1, 1);
        // Strict verifiers (Python 3.13 and later) need the issuer key id.
        p.use_authority_key_identifier_extension = true;
        Ok(p.signed_by(&self.leaf_key, &self.issuer)?.der().clone())
    }
}

/// Makes a new CA. Returns (certificate PEM, key PEM).
pub fn generate(name: &str) -> anyhow::Result<(String, String)> {
    let mut p = CertificateParams::new(Vec::<String>::new())?;
    p.distinguished_name = DistinguishedName::new();
    p.distinguished_name.push(DnType::CommonName, name);
    p.is_ca = IsCa::Ca(BasicConstraints::Constrained(0));
    p.key_usages = vec![KeyUsagePurpose::KeyCertSign, KeyUsagePurpose::CrlSign, KeyUsagePurpose::DigitalSignature];
    p.not_before = rcgen::date_time_ymd(2026, 1, 1);
    p.not_after = rcgen::date_time_ymd(2036, 1, 1);
    let key = KeyPair::generate()?;
    let cert = p.self_signed(&key)?;
    Ok((cert.pem(), key.serialize_pem()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn makes_host_certificates() {
        let (c, k) = generate("Reactorcide egress test CA").unwrap();
        let ca = Ca::from_pem(&c, &k).unwrap();
        let a = ca.server_config("api.example.local").unwrap();
        let b = ca.server_config("API.example.local").unwrap();
        assert!(Arc::ptr_eq(&a, &b));
        // The authority key id (OID 2.5.29.35) of a host certificate.
        let der = ca.leaf("api.example.local").unwrap();
        assert!(der.windows(5).any(|w| w == [0x06, 0x03, 0x55, 0x1d, 0x23]), "no authority key id");
        // The key id in the leaf is the subject key id of the CA (strict
        // verifiers, such as Python 3.13 and later, compare them).
        use x509_parser::prelude::*;
        let (_, leaf) = X509Certificate::from_der(&der).unwrap();
        let ca_der = rustls_pemfile::certs(&mut c.as_bytes()).next().unwrap().unwrap();
        let (_, root) = X509Certificate::from_der(&ca_der).unwrap();
        let aki = leaf.extensions().iter().find_map(|e| match e.parsed_extension() {
            ParsedExtension::AuthorityKeyIdentifier(a) => a.key_identifier.as_ref().map(|k| k.0.to_vec()),
            _ => None,
        });
        let ski = root.extensions().iter().find_map(|e| match e.parsed_extension() {
            ParsedExtension::SubjectKeyIdentifier(k) => Some(k.0.to_vec()),
            _ => None,
        });
        assert!(aki.is_some() && aki == ski, "the authority key id must be the CA subject key id");
    }
}
