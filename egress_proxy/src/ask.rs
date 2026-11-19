//! The optional ask service of plugins. A plugin calls the host function
//! `ask(op, request)`; the host sends the request to
//! `POST {ask_url}/{op}` (CBOR, bearer token from a file). The proxy adds
//! "plugin" and "flow" to each request, so a plugin cannot speak for
//! another plugin or another flow. Reactorcide itself has no ask service:
//! an operator can add one for their own plugins.

use std::path::PathBuf;
use std::time::Duration;

use bytes::Bytes;
use ciborium::Value;
use http_body_util::{BodyExt as _, Full};

use crate::net::Client;
use crate::plugins::{Direction, Flow};

/// The most body bytes that go to the ask service with a flow.
pub const FLOW_BODY: usize = 8 << 10;

pub struct Ask {
    base: String,
    token_file: PathBuf,
    client: Client,
}

impl Ask {
    pub fn new(base: &str, token_file: PathBuf, client: Client) -> Self {
        Ask { base: base.trim_end_matches('/').to_string(), token_file, client }
    }

    async fn call(&self, op: &str, body: Vec<u8>) -> Result<Vec<u8>, String> {
        let token = std::fs::read_to_string(&self.token_file).map_err(|e| format!("read the ask token: {e}"))?;
        let req = http::Request::post(format!("{}/{op}", self.base))
            .header(http::header::CONTENT_TYPE, "application/cbor")
            .header(http::header::AUTHORIZATION, format!("Bearer {}", token.trim()))
            .body(Full::new(Bytes::from(body)))
            .map_err(|e| e.to_string())?;
        let res = tokio::time::timeout(Duration::from_secs(60), self.client.request(req))
            .await
            .map_err(|_| "the ask service did not answer in 60 s".to_string())?
            .map_err(|e| format!("ask service: {e}"))?;
        let status = res.status();
        let body = res.into_body().collect().await.map_err(|e| e.to_string())?.to_bytes();
        if !status.is_success() {
            return Err(format!("ask {op}: {status}: {}", String::from_utf8_lossy(&body)));
        }
        Ok(body.to_vec())
    }

    /// A plugin call. `request` must be a CBOR map.
    pub async fn ask(&self, op: &str, plugin: &str, flow: &Flow, request: &[u8]) -> Result<Vec<u8>, String> {
        let body = with_context(request, plugin, flow)?;
        self.call(op, body).await
    }
}

/// Puts "plugin" and "flow" in the request map, in place of any keys with
/// those names.
pub fn with_context(request: &[u8], plugin: &str, flow: &Flow) -> Result<Vec<u8>, String> {
    let v: Value = if request.is_empty() { Value::Map(vec![]) } else { ciborium::from_reader(request).map_err(|e| format!("request is not CBOR: {e}"))? };
    let Value::Map(mut m) = v else { return Err("request must be a CBOR map".into()) };
    m.retain(|(k, _)| !matches!(k.as_text(), Some("plugin" | "flow")));
    m.push((Value::Text("plugin".into()), Value::Text(plugin.into())));
    m.push((Value::Text("flow".into()), flow_value(flow)));
    Ok(encode(&Value::Map(m)))
}

pub fn flow_value(f: &Flow) -> Value {
    let headers = f
        .headers
        .iter()
        .map(|h| map(vec![("name", Value::Text(h.name.clone())), ("value", Value::Text(h.value.clone()))]))
        .collect();
    map(vec![
        ("id", Value::Text(f.id.clone())),
        ("environment", Value::Text(f.environment.clone())),
        ("owner", Value::Text(f.owner.clone())),
        ("direction", Value::Text(match f.direction { Direction::Out => "out", Direction::In => "in" }.into())),
        ("destination", Value::Text(f.destination.clone())),
        ("method", Value::Text(f.method.clone())),
        ("url", Value::Text(f.url.clone())),
        ("status", Value::Integer(f.status.into())),
        ("headers", Value::Array(headers)),
        ("body", Value::Bytes(f.body[..f.body.len().min(FLOW_BODY)].to_vec())),
    ])
}

fn map(kv: Vec<(&str, Value)>) -> Value {
    Value::Map(kv.into_iter().map(|(k, v)| (Value::Text(k.into()), v)).collect())
}

pub fn encode(v: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    ciborium::into_writer(v, &mut out).expect("CBOR encode");
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::plugins::Header;

    fn text(v: &Value, key: &str) -> String {
        v.as_map()
            .and_then(|m| m.iter().find(|(k, _)| k.as_text() == Some(key)))
            .and_then(|(_, v)| v.as_text())
            .unwrap_or_default()
            .to_string()
    }

    fn flow() -> Flow {
        Flow {
            id: "flw_1".into(),
            environment: "job_a".into(),
            owner: "o/p/build".into(),
            direction: Direction::Out,
            destination: "api.example.local:443".into(),
            method: "POST".into(),
            url: "https://api.example.local/x".into(),
            status: 0,
            headers: vec![Header { name: "accept".into(), value: "*/*".into() }],
            body: vec![b'x'; FLOW_BODY + 10],
        }
    }

    #[test]
    fn plugin_cannot_replace_the_context() {
        let req = map(vec![("question", Value::Text("q".into())), ("plugin", Value::Text("other".into())), ("flow", Value::Null)]);
        let out = with_context(&encode(&req), "review", &flow()).unwrap();
        let v: Value = ciborium::from_reader(&out[..]).unwrap();
        let m = v.as_map().unwrap();
        assert_eq!(m.len(), 3);
        assert_eq!(text(&v, "plugin"), "review");
        assert_eq!(text(&v, "question"), "q");
        let f = &m.iter().find(|(k, _)| k.as_text() == Some("flow")).unwrap().1;
        assert_eq!(text(f, "environment"), "job_a");
        let body = f.as_map().unwrap().iter().find(|(k, _)| k.as_text() == Some("body")).unwrap().1.as_bytes().unwrap().len();
        assert_eq!(body, FLOW_BODY);
        assert!(with_context(&encode(&Value::Array(vec![])), "p", &flow()).is_err());
        assert!(with_context(b"\xff", "p", &flow()).is_err());
        assert!(with_context(b"", "p", &flow()).is_ok());
    }

    #[tokio::test]
    async fn no_service_is_an_error() {
        let dir = tempfile::tempdir().unwrap();
        let tok = dir.path().join("t");
        std::fs::write(&tok, "x").unwrap();
        let a = Ask::new("http://127.0.0.1:1", tok, crate::net::client(crate::net::roots(None).unwrap()).unwrap());
        assert!(a.ask("op", "p", &flow(), b"").await.is_err());
    }
}
