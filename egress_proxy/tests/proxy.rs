//! End-to-end tests: curl -> proxy (TLS ended with the test CA) -> a TLS
//! upstream that echoes each request. The control API registers the jobs,
//! as the Reactorcide worker does. The plugin is the real WASM component
//! from plugins/test-actions. A fake ask service records plugin calls.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

use axum::body::Bytes;
use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use ciborium::Value;
use http_body_util::{BodyExt as _, Full};
use hyper::service::service_fn;
use hyper_util::rt::TokioIo;
use reactorcide_egress::ask::Ask;
use reactorcide_egress::ca::{self, Ca};
use reactorcide_egress::flow::Proxy;
use reactorcide_egress::jobs::Jobs;
use reactorcide_egress::plugins::Plugins;
use reactorcide_egress::policy::Policy;
use reactorcide_egress::records::Records;
use reactorcide_egress::scan::Secrets;
use reactorcide_egress::{net, proxy, server};
use sha2::{Digest, Sha256};

/// An operator secret (secrets dir): plugins and SSH use it.
const SECRET: &str = "ghp_s3cr3tTOKENvalue0123456789";
/// The real values of the job grants.
const GRANT_VALUE: &str = "reactorcide-real-token-0123456789";
const FORMAT_VALUE: &str = "format-secret-abcdef123456";
const PH: &str = "reactorcide-secret-00112233445566778899aabbccddeeff";
const PH_FMT: &str = "reactorcide-secret-ffeeddccbbaa99887766554433221100";
const JOB_KEY: &[u8] = b"0123456789abcdef0123456789abcdef";
const CONTROL_TOKEN: &str = "control-token-0123456789abcdef0123456789";
const JOB: &str = "job_test";

/// Builds the plugins once for all tests.
fn plugins_dir() -> &'static Path {
    static DIR: OnceLock<PathBuf> = OnceLock::new();
    DIR.get_or_init(|| {
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("plugins");
        let st = Command::new("cargo")
            .args(["build", "-q", "--release", "--target", "wasm32-wasip2"])
            .current_dir(&root)
            .env_remove("CARGO_TARGET_DIR")
            .status()
            .expect("cargo");
        assert!(st.success(), "plugin build failed");
        root.join("target/wasm32-wasip2/release")
    })
}

fn grant(name: &str, file: &str, stages: &[&str], ops: &[&str]) -> serde_json::Value {
    let path = plugins_dir().join(file);
    let sha = hex::encode(Sha256::digest(std::fs::read(&path).unwrap()));
    serde_json::json!({"name": name, "file": path, "sha256": sha, "stages": stages, "ask_ops": ops, "fuel": 500_000_000u64, "memory_mb": 32, "timeout_ms": 10_000})
}

#[derive(Default)]
struct Seen {
    /// The last request that the upstream got: method, path, headers, body.
    upstream: Option<serde_json::Value>,
    /// Each request that the ask service got: (op, decoded CBOR).
    ask: Vec<(String, Value)>,
}

type Shared = Arc<Mutex<Seen>>;

struct World {
    proxy: Arc<Proxy>,
    proxy_addr: std::net::SocketAddr,
    api_addr: std::net::SocketAddr,
    up_port: u16,
    ca_file: PathBuf,
    seen: Shared,
    _dir: tempfile::TempDir,
}

fn cbor(v: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    ciborium::into_writer(v, &mut out).unwrap();
    out
}

fn get<'a>(v: &'a Value, k: &str) -> Option<&'a Value> {
    v.as_map()?.iter().find(|(x, _)| x.as_text() == Some(k)).map(|(_, v)| v)
}

fn text(v: &Value, k: &str) -> String {
    get(v, k).and_then(|v| v.as_text()).unwrap_or_default().to_string()
}

async fn fake_ask(seen: Shared) -> std::net::SocketAddr {
    use axum::extract::{Path as P, State};
    async fn call(State(seen): State<Shared>, P(op): P<String>, body: Bytes) -> axum::response::Response {
        use axum::response::IntoResponse as _;
        let v: Value = ciborium::from_reader(&body[..]).unwrap();
        seen.lock().unwrap().ask.push((op.clone(), v));
        let out = Value::Map(vec![(Value::Text("answer".into()), Value::Text(format!("ok {op}")))]);
        ([(axum::http::header::CONTENT_TYPE, "application/cbor")], cbor(&out)).into_response()
    }
    let app = axum::Router::new().route("/{op}", axum::routing::post(call)).with_state(seen);
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let a = l.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(l, app).await.unwrap() });
    a
}

/// A TLS server for "localhost" and 127.0.0.1 that echoes each request
/// as JSON. "/leak" answers with the operator secret, "/leak-grant" with a
/// grant value.
async fn upstream(seen: Shared, dir: &Path) -> (u16, PathBuf) {
    let (ca_pem, ca_key) = ca::generate("upstream test CA").unwrap();
    let key = rcgen::KeyPair::from_pem(&ca_key).unwrap();
    let issuer = rcgen::Issuer::from_ca_cert_pem(&ca_pem, key).unwrap();
    let leaf_key = rcgen::KeyPair::generate().unwrap();
    let leaf = rcgen::CertificateParams::new(vec!["localhost".into(), "127.0.0.1".into()]).unwrap().signed_by(&leaf_key, &issuer).unwrap();
    let ca_file = dir.join("upstream-ca.pem");
    std::fs::write(&ca_file, &ca_pem).unwrap();
    let chain = vec![leaf.der().clone()];
    let pk = rustls::pki_types::PrivateKeyDer::Pkcs8(leaf_key.serialize_der().into());
    let cfg = rustls::ServerConfig::builder_with_provider(net::provider()).with_safe_default_protocol_versions().unwrap().with_no_client_auth().with_single_cert(chain, pk).unwrap();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = l.local_addr().unwrap().port();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            let (acc, seen) = (acceptor.clone(), seen.clone());
            tokio::spawn(async move {
                let Ok(tls) = acc.accept(s).await else { return };
                let svc = service_fn(move |req: hyper::Request<hyper::body::Incoming>| {
                    let seen = seen.clone();
                    async move {
                        let (parts, body) = req.into_parts();
                        if parts.uri.path() == "/sse" {
                            // A streamed answer: one event, a pause, then an
                            // event with the secret.
                            let (mut tx, rx) = http_body_util::Channel::<Bytes, std::convert::Infallible>::new(4);
                            tokio::spawn(async move {
                                let _ = tx.send_data(Bytes::from("data: {\"n\":1}\n\n")).await;
                                tokio::time::sleep(std::time::Duration::from_millis(2500)).await;
                                let _ = tx.send_data(Bytes::from(format!("data: {{\"n\":2,\"t\":\"{SECRET}\",\"g\":\"{GRANT_VALUE}\"}}\n\ndata: [DONE]\n\n"))).await;
                            });
                            let mut r = hyper::Response::new(http_body_util::BodyExt::boxed(rx));
                            r.headers_mut().insert(hyper::header::CONTENT_TYPE, "text/event-stream".parse().unwrap());
                            return Ok::<_, std::convert::Infallible>(r);
                        }
                        let body = body.collect().await.unwrap().to_bytes();
                        let headers: HashMap<String, String> = parts.headers.iter().map(|(k, v)| (k.to_string(), v.to_str().unwrap_or("").to_string())).collect();
                        let echo = serde_json::json!({"method": parts.method.as_str(), "path": parts.uri.to_string(), "headers": headers, "body": String::from_utf8_lossy(&body)});
                        seen.lock().unwrap().upstream = Some(echo.clone());
                        let out = match parts.uri.path() {
                            "/leak" => format!("the token is {SECRET}"),
                            "/leak-grant" => format!("the token is {GRANT_VALUE}"),
                            _ => echo.to_string(),
                        };
                        Ok::<_, std::convert::Infallible>(hyper::Response::new(http_body_util::BodyExt::boxed(Full::new(Bytes::from(out)))))
                    }
                });
                let _ = hyper::server::conn::http1::Builder::new().serve_connection(TokioIo::new(tls), svc).await;
            });
        }
    });
    (port, ca_file)
}

#[derive(Default)]
struct Opts {
    ssh_hosts: Option<serde_json::Value>,
    ssh_key: Option<russh::keys::PrivateKey>,
    more: Vec<(String, Vec<u8>)>,
    passthrough_port: Option<u16>,
}

async fn world() -> World {
    world_with(Opts::default()).await
}

/// The job policy of job_test: 127.0.0.1 is a job destination (no grant
/// permits it); the grants permit localhost only.
fn job_policy(up_port: u16) -> serde_json::Value {
    serde_json::json!({
        "job_id": JOB,
        "identity": {"org_id": "org1", "project_id": "proj1", "node": "build", "workflow_security_id": "wf-sec", "ref_classes": ["protected"]},
        "destinations": [{"host": "127.0.0.1", "ports": [up_port]}],
        "grants": [
            {"id": "g-plain", "secret_ref": "ci/github:token", "placeholder": PH, "value": GRANT_VALUE, "hosts": ["localhost"]},
            {"id": "g-format", "secret_ref": "ci/api:key", "placeholder": PH_FMT, "value": FORMAT_VALUE, "hosts": ["localhost"], "header": "authorization", "format": "Bearer {}"}
        ]
    })
}

async fn world_with(o: Opts) -> World {
    let dir = tempfile::tempdir().unwrap();
    let seen: Shared = Arc::default();
    let (up_port, up_ca) = upstream(seen.clone(), dir.path()).await;
    let ask_addr = fake_ask(seen.clone()).await;
    let token = dir.path().join("ask-token");
    std::fs::write(&token, "ask-token\n").unwrap();
    let (ca_pem, ca_key) = ca::generate("Reactorcide egress test CA").unwrap();
    let ca_file = dir.path().join("ca.pem");
    std::fs::write(&ca_file, &ca_pem).unwrap();
    let passthrough = match o.passthrough_port {
        Some(p) => serde_json::json!([{"host": "127.0.0.2", "ports": [p]}]),
        None => serde_json::json!([]),
    };
    let policy: Policy = serde_json::from_value(serde_json::json!({
        "destinations": [{"host": "localhost", "ports": [up_port]}],
        "passthrough": passthrough,
        "secrets": [{"id": "gh", "hosts": ["localhost"]}],
        "event_stream_hosts": ["localhost"],
        "ssh_hosts": o.ssh_hosts.unwrap_or(serde_json::json!([])),
        "plugins": [grant("test-actions", "test_actions.wasm", &["out", "in"], &["evaluate"])],
    }))
    .unwrap();
    policy.validate().unwrap();
    let client = net::client(net::roots(Some(&up_ca)).unwrap()).unwrap();
    let ask = Arc::new(Ask::new(&format!("http://{ask_addr}"), token, net::client(net::roots(None).unwrap()).unwrap()));
    let plugins = Plugins::load(&policy.plugins, Some(ask.clone())).unwrap();
    let records = Arc::new(Records::new(100));
    let jobs = Arc::new(Jobs::default());
    let p = Arc::new(Proxy {
        policy,
        secrets: Arc::new(Secrets::new([vec![("gh".into(), SECRET.into())], o.more].concat())),
        plugins,
        ask: Some(ask),
        records: records.clone(),
        jobs: jobs.clone(),
        ca: Ca::from_pem(&ca_pem, &ca_key).unwrap(),
        upstream: client,
        job_key: JOB_KEY.to_vec(),
        allow_unregistered: false,
        ssh_key: o.ssh_key,
    });
    let pl = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let proxy_addr = pl.local_addr().unwrap();
    tokio::spawn(proxy::serve(p.clone(), pl));
    let srv = Arc::new(server::Server { records, jobs, token: CONTROL_TOKEN.as_bytes().to_vec() });
    let al = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let api_addr = al.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(al, server::router(srv)).await.unwrap() });
    let w = World { proxy: p, proxy_addr, api_addr, up_port, ca_file, seen, _dir: dir };
    let (code, body) = w.control("PUT", &format!("/v1/jobs/{JOB}"), Some(CONTROL_TOKEN), Some(job_policy(up_port))).await;
    assert_eq!(code, 200, "{body}");
    assert_eq!(body["grants"], 2);
    w
}

/// The result of one curl call.
#[derive(Debug)]
struct Out {
    code: u16,
    body: String,
    err: String,
}

impl World {
    async fn curl(&self, host: &str, path: &str, args: &[&str]) -> Out {
        self.curl_as(Some(JOB), host, path, args).await
    }

    async fn curl_as(&self, job: Option<&str>, host: &str, path: &str, args: &[&str]) -> Out {
        let mut c = Command::new("curl");
        // curl applies NO_PROXY also with -x.
        for v in ["NO_PROXY", "no_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"] {
            c.env_remove(v);
        }
        c.args(["-sS", "--max-time", "30", "-x", &format!("http://{}", self.proxy_addr), "--cacert"]).arg(&self.ca_file);
        if let Some(j) = job {
            c.args(["--proxy-user", &format!("{j}:{}", proxy::credential(JOB_KEY, j))]);
        }
        c.args(["-w", "\n%{http_code}"]).arg(format!("https://{host}:{}{path}", self.up_port)).args(args);
        let o = tokio::task::spawn_blocking(move || c.output().expect("curl")).await.unwrap();
        let s = String::from_utf8_lossy(&o.stdout).to_string();
        let (body, code) = s.rsplit_once('\n').unwrap_or(("", &s));
        Out { code: code.trim().parse().unwrap_or(0), body: body.to_string(), err: String::from_utf8_lossy(&o.stderr).to_string() }
    }

    fn upstream(&self) -> serde_json::Value {
        self.seen.lock().unwrap().upstream.take().unwrap_or(serde_json::Value::Null)
    }

    /// One control API call: (status, JSON body).
    async fn control(&self, method: &str, path: &str, token: Option<&str>, body: Option<serde_json::Value>) -> (u16, serde_json::Value) {
        let c = net::client(net::roots(None).unwrap()).unwrap();
        let mut req = http::Request::builder().method(method).uri(format!("http://{}{path}", self.api_addr));
        if let Some(t) = token {
            req = req.header("authorization", format!("Bearer {t}"));
        }
        let req = req.body(Full::new(Bytes::from(body.map(|b| b.to_string()).unwrap_or_default()))).unwrap();
        let r = c.request(req).await.unwrap();
        let s = r.status().as_u16();
        let b = r.into_body().collect().await.unwrap().to_bytes();
        (s, serde_json::from_slice(&b).unwrap_or(serde_json::Value::Null))
    }

    async fn flows(&self, job: &str) -> Vec<serde_json::Value> {
        let (code, body) = self.control("GET", &format!("/v1/flows?job={job}&limit=100"), Some(CONTROL_TOKEN), None).await;
        assert_eq!(code, 200, "{body}");
        body["flows"].as_array().unwrap().clone()
    }

    /// The ask calls with op, after a short wait.
    async fn ask_calls(&self, op: &str, n: usize) -> Vec<Value> {
        for _ in 0..100 {
            let v: Vec<Value> = self.seen.lock().unwrap().ask.iter().filter(|(o, _)| o == op).map(|(_, v)| v.clone()).collect();
            if v.len() >= n {
                return v;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        self.seen.lock().unwrap().ask.iter().filter(|(o, _)| o == op).map(|(_, v)| v.clone()).collect()
    }
}

fn curl_present() -> bool {
    Command::new("curl").arg("--version").output().is_ok_and(|o| o.status.success())
}

macro_rules! need_curl {
    () => {
        if !curl_present() {
            eprintln!("skip: curl is not installed");
            return;
        }
    };
}

#[tokio::test]
async fn credential_registration_and_destination() {
    need_curl!();
    let w = world().await;
    // No credential, and a wrong credential.
    let o = w.curl_as(None, "localhost", "/", &[]).await;
    assert!(o.err.contains("407") || o.code == 407, "{o:?}");
    let o = w.curl_as(None, "localhost", "/", &["--proxy-user", &format!("{JOB}:bad")]).await;
    assert!(o.err.contains("407") || o.code == 407, "{o:?}");
    // A valid credential of a job that is not registered.
    let o = w.curl_as(Some("job_other"), "localhost", "/", &[]).await;
    assert!(o.err.contains("403") || o.code == 403, "{o:?}");
    assert!(w.upstream().is_null());
    // A destination that is not permitted.
    let o = w.curl("example.invalid", "/", &[]).await;
    assert!(o.err.contains("403"), "{o:?}");
    let o = w.curl("localhost", "/", &["-H", "Host: other.example.local"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("is not the destination"), "{o:?}");
    // A job destination (not in the global policy).
    let o = w.curl("127.0.0.1", "/job-destination", &[]).await;
    assert_eq!(o.code, 200, "{o:?}");

    // DELETE removes the job: the credential stops working.
    let (code, sum) = w.control("DELETE", &format!("/v1/jobs/{JOB}"), Some(CONTROL_TOKEN), None).await;
    assert_eq!(code, 200, "{sum}");
    assert_eq!(sum["job_id"], JOB);
    assert!(sum["flows"].as_u64().unwrap() >= 3, "{sum}");
    assert!(sum["blocked"].as_u64().unwrap() >= 2, "{sum}");
    let o = w.curl("localhost", "/", &[]).await;
    assert!(o.err.contains("403") || o.code == 403, "{o:?}");
    let (code, _) = w.control("DELETE", &format!("/v1/jobs/{JOB}"), Some(CONTROL_TOKEN), None).await;
    assert_eq!(code, 404);
}

/// With --control-tls-name the control API uses TLS with a certificate
/// from the proxy CA; a client that trusts only that CA connects, and a
/// client that trusts other roots does not.
#[tokio::test]
async fn control_api_over_tls() {
    let dir = tempfile::tempdir().unwrap();
    let (ca_pem, ca_key) = ca::generate("Reactorcide egress test CA").unwrap();
    let ca_file = dir.path().join("ca.pem");
    std::fs::write(&ca_file, &ca_pem).unwrap();
    let ca = Ca::from_pem(&ca_pem, &ca_key).unwrap();
    let srv = Arc::new(server::Server { records: Arc::new(Records::new(10)), jobs: Arc::new(Jobs::default()), token: CONTROL_TOKEN.as_bytes().to_vec() });
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = l.local_addr().unwrap().port();
    let tls = server::TlsListener::new(l, ca.server_config("localhost").unwrap()).unwrap();
    tokio::spawn(async move { axum::serve(tls, server::router(srv)).await.unwrap() });
    let get = async |c: &hyper_util::client::legacy::Client<_, Full<Bytes>>, url: String| {
        let req = http::Request::builder().uri(url).header("authorization", format!("Bearer {CONTROL_TOKEN}")).body(Full::new(Bytes::new())).unwrap();
        c.request(req).await
    };
    let trusted = net::client(net::roots(Some(&ca_file)).unwrap()).unwrap();
    let r = get(&trusted, format!("https://localhost:{port}/v1/flows")).await.expect("TLS to the control API");
    assert_eq!(r.status(), 200);
    let other = net::client(net::roots(None).unwrap()).unwrap();
    assert!(get(&other, format!("https://localhost:{port}/v1/flows")).await.is_err(), "a client without the proxy CA must not connect");
    // Plain HTTP to the TLS port does not get an answer.
    let plain = net::client(net::roots(None).unwrap()).unwrap();
    assert!(get(&plain, format!("http://localhost:{port}/v1/flows")).await.is_err());
}

#[tokio::test]
async fn control_api_needs_the_token() {
    let w = world().await;
    let path = format!("/v1/jobs/{JOB}");
    let (code, _) = w.control("PUT", &path, None, Some(job_policy(w.up_port))).await;
    assert_eq!(code, 401);
    let (code, _) = w.control("PUT", &path, Some("control-token-wrong-0123456789abcdef01234"), Some(job_policy(w.up_port))).await;
    assert_eq!(code, 401);
    let (code, _) = w.control("GET", "/v1/flows", Some("x"), None).await;
    assert_eq!(code, 401);
    let (code, _) = w.control("DELETE", &path, None, None).await;
    assert_eq!(code, 401);
    // The job id of the body must be the job id of the path.
    let (code, body) = w.control("PUT", "/v1/jobs/job_other", Some(CONTROL_TOKEN), Some(job_policy(w.up_port))).await;
    assert_eq!(code, 400, "{body}");
    // A placeholder that is not valid.
    let mut bad = job_policy(w.up_port);
    bad["grants"][0]["placeholder"] = "magi-secret-x".into();
    let (code, body) = w.control("PUT", &path, Some(CONTROL_TOKEN), Some(bad)).await;
    assert_eq!(code, 400, "{body}");
    assert!(body["message"].as_str().unwrap().contains("placeholder"), "{body}");
    // healthz has no token.
    let c = net::client(net::roots(None).unwrap()).unwrap();
    let r = c.request(http::Request::get(format!("http://{}/healthz", w.api_addr)).body(Full::new(Bytes::new())).unwrap()).await.unwrap();
    assert_eq!(r.status(), 200);
}

#[tokio::test]
async fn grant_placeholders_only_for_grant_hosts() {
    need_curl!();
    let w = world().await;
    // (a) The placeholder in the Authorization header: the upstream gets
    // the real value; the echo comes back without it.
    let o = w.curl("localhost", "/api", &["-H", &format!("Authorization: token {PH}")]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["headers"]["authorization"], format!("token {GRANT_VALUE}"));
    assert!(o.body.contains("token [redacted:g-plain]") && !o.body.contains(GRANT_VALUE), "{o:?}");

    // (b) The same placeholder to a host that no grant permits: blocked,
    // and the upstream never sees the request.
    let o = w.curl("127.0.0.1", "/api", &["-H", &format!("Authorization: token {PH}")]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains(&format!("no secret for placeholder {PH} at 127.0.0.1")), "{o:?}");
    assert!(w.upstream().is_null());

    // Only the grant header: in another header the placeholder stays and stops the request.
    let o = w.curl("localhost", "/api", &["-H", &format!("X-Token: {PH}")]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(w.upstream().is_null());
    // In the URL too.
    let o = w.curl("localhost", &format!("/api?t={PH}"), &[]).await;
    assert_eq!(o.code, 403, "{o:?}");
    // A placeholder in the body is not a secret: it passes, not replaced.
    let o = w.curl("localhost", "/api", &["-d", &format!("log line {PH}")]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["body"], format!("log line {PH}"));

    // (c) The format makes the full header.
    let o = w.curl("localhost", "/api", &["-H", &format!("Authorization: {PH_FMT}")]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["headers"]["authorization"], format!("Bearer {FORMAT_VALUE}"));
    // A git-style Basic credential: decoded, changed, encoded again.
    let o = w.curl("localhost", "/repo.git/info/refs", &["-u", &format!("x-access-token:{PH}")]).await;
    assert_eq!(o.code, 200, "{o:?}");
    let up = w.upstream();
    assert_eq!(up["headers"]["authorization"], format!("Basic {}", STANDARD.encode(format!("x-access-token:{GRANT_VALUE}"))));
    assert!(!o.body.contains(GRANT_VALUE) && !o.body.contains(&STANDARD.encode(format!("x-access-token:{GRANT_VALUE}"))), "{o:?}");

    // (e) A grant value in a response is removed.
    let o = w.curl("localhost", "/leak-grant", &[]).await;
    assert_eq!(o.body, "the token is [redacted:g-plain]");

    // (g) The records of the job.
    let flows = w.flows(JOB).await;
    assert!(flows.iter().all(|f| f["job_id"] == JOB && f["identity"] == "org1/proj1/build"), "{flows:?}");
    assert!(flows.iter().any(|f| f["secrets_used"] == serde_json::json!(["g-plain"]) && f["outcome"] == "sent"), "{flows:?}");
    assert!(flows.iter().any(|f| f["secrets_used"] == serde_json::json!(["g-format"])), "{flows:?}");
    assert!(
        flows.iter().any(|f| f["outcome"] == "blocked" && f["reasons"].to_string().contains("grant g-plain does not permit host 127.0.0.1")),
        "{flows:?}"
    );
    assert!(w.flows("job_other").await.is_empty());
}

/// A grant value goes only to an upstream over TLS. On plain HTTP (an
/// http:// URL, or plain HTTP in a CONNECT tunnel) a grant placeholder
/// stops the request with 403, also inside a Basic credential; the
/// upstream never sees it.
#[tokio::test]
async fn grants_only_over_tls() {
    need_curl!();
    let w = world().await;
    let plain = |extra: Vec<String>| {
        let mut c = Command::new("curl");
        for v in ["NO_PROXY", "no_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"] {
            c.env_remove(v);
        }
        c.args(["-sS", "--max-time", "30", "-x", &format!("http://{}", w.proxy_addr)]);
        c.args(["--proxy-user", &format!("{JOB}:{}", proxy::credential(JOB_KEY, JOB))]);
        c.args(["-w", "\n%{http_code}"]).arg(format!("http://localhost:{}/api", w.up_port)).args(extra);
        c
    };
    for extra in [
        vec!["-H".to_string(), format!("Authorization: token {PH}")],
        vec!["-H".to_string(), format!("Authorization: {PH_FMT}")],
        vec!["-u".to_string(), format!("x-access-token:{PH}")],
        vec!["--proxytunnel".to_string(), "-H".to_string(), format!("Authorization: token {PH}")],
    ] {
        let mut c = plain(extra.clone());
        let o = tokio::task::spawn_blocking(move || c.output().expect("curl")).await.unwrap();
        let s = String::from_utf8_lossy(&o.stdout).to_string();
        let (body, code) = s.rsplit_once('\n').unwrap_or(("", &s));
        assert_eq!(code.trim(), "403", "{extra:?}: {s}");
        assert!(body.contains("goes only over TLS"), "{extra:?}: {body}");
        assert!(w.upstream().is_null(), "{extra:?}: the upstream must not see the request");
    }
    let flows = w.flows(JOB).await;
    assert!(flows.iter().all(|f| f["secrets_used"].as_array().is_none_or(|a| a.is_empty())), "{flows:?}");
    // Over TLS the same grant works.
    let o = w.curl("localhost", "/api", &["-H", &format!("Authorization: token {PH}")]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["headers"]["authorization"], format!("token {GRANT_VALUE}"));
}

#[tokio::test]
async fn passes_and_removes_secrets() {
    need_curl!();
    let w = world().await;
    let o = w.curl("localhost", "/hello?x=1", &[]).await;
    assert_eq!(o.code, 200, "{o:?}");
    let up = w.upstream();
    assert_eq!(up["path"], "/hello?x=1");
    assert_eq!(up["headers"]["accept-encoding"], "identity");
    assert!(up["headers"].get("proxy-authorization").is_none());

    // A real secret in the body, raw and in base64 inside JSON: the
    // operator secret and a grant value.
    let body = format!("{{\"a\":\"{SECRET}\",\"b\":\"{}\",\"c\":\"{GRANT_VALUE}\"}}", STANDARD.encode(format!("user:{SECRET}")));
    let o = w.curl("localhost", "/post", &["-d", &body]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["body"], "{\"a\":\"[redacted:gh]\",\"b\":\"[redacted:gh]\",\"c\":\"[redacted:g-plain]\"}");

    // A secret in a response is removed too.
    let o = w.curl("localhost", "/leak", &[]).await;
    assert_eq!(o.body, "the token is [redacted:gh]");
    let flows = w.proxy.records.list(None, 10);
    assert!(flows.iter().any(|f| f.url.as_deref().is_some_and(|u| u.ends_with("/leak")) && f.redacted == 1));
}

#[tokio::test]
async fn plugin_secrets_only_where_permitted() {
    need_curl!();
    let w = world().await;
    // A plugin puts the operator secret gh in place of a placeholder ("any":
    // the command header of the test plugin has the placeholder too).
    let o = w.curl("localhost", "/api", &["-H", "Authorization: Bearer reactorcide-secret-gh", "-H", "x-test-plugin: secret:reactorcide-secret-gh:gh:any"]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert_eq!(w.upstream()["headers"]["authorization"], format!("Bearer {SECRET}"));
    // The policy permits gh only for localhost.
    let o = w.curl("127.0.0.1", "/api", &["-H", "Authorization: Bearer reactorcide-secret-gh", "-H", "x-test-plugin: secret:reactorcide-secret-gh:gh:any"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("secret gh is not permitted for 127.0.0.1"), "{o:?}");
    // A plugin can only use a placeholder, not any text.
    let o = w.curl("localhost", "/api", &["-H", "x-test-plugin: secret:Bearer:gh:any"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("is not a placeholder"), "{o:?}");
    // A placeholder with no grant and no plugin.
    let o = w.curl("localhost", "/api", &["-H", "X-Key: reactorcide-secret-unknown"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(w.upstream().is_null());
}

#[tokio::test]
async fn stops_deep_encodings() {
    need_curl!();
    let w = world().await;
    let text = "this text is long enough to be a layer of encoding";
    let two = STANDARD.encode(STANDARD.encode(text));
    let o = w.curl("localhost", "/up", &["-d", &two]).await;
    assert_eq!(o.code, 200, "{o:?}");
    let three = STANDARD.encode(&two);
    let o = w.curl("localhost", "/up", &["-d", &three]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("encoded 3 times; the limit is 2"), "{o:?}");
    let o = w.curl("localhost", "/up", &["-H", "Content-Encoding: br", "-d", "x"]).await;
    assert_eq!(o.code, 403, "{o:?}");
}

#[tokio::test]
async fn plugin_actions_and_limits() {
    need_curl!();
    let w = world().await;
    let o = w.curl("localhost", "/", &["-H", "x-test-plugin: block:not today"]).await;
    assert_eq!(o.code, 403);
    assert!(o.body.contains("plugin test-actions: not today"), "{o:?}");

    let o = w.curl("localhost", "/", &["-H", "x-test-plugin: body:new body;header:x-added=yes", "-d", "old"]).await;
    assert_eq!(o.code, 200, "{o:?}");
    let up = w.upstream();
    assert_eq!(up["body"], "new body");
    assert_eq!(up["headers"]["x-added"], "yes");

    let o = w.curl("localhost", "/", &["-H", "x-test-plugin: header:host=evil.example.local"]).await;
    assert_eq!(o.code, 403, "{o:?}");

    for (cmd, want) in [("loop", "failed"), ("memory", "failed"), ("panic", "failed")] {
        let o = w.curl("localhost", "/", &["-H", &format!("x-test-plugin: {cmd}")]).await;
        assert_eq!(o.code, 403, "{cmd}: {o:?}");
        assert!(o.body.contains(want), "{cmd}: {o:?}");
    }
}

#[tokio::test]
async fn plugins_ask_and_reviews_block() {
    need_curl!();
    let w = world().await;
    let o = w.curl("localhost", "/ask", &["-H", "x-test-plugin: ask:evaluate", "-d", "x"]).await;
    assert_eq!(o.code, 200, "{o:?}");
    let req = w.ask_calls("evaluate", 1).await.pop().unwrap();
    // The plugin said "forged"; the host put the real plugin name and the flow.
    assert_eq!(text(&req, "plugin"), "test-actions");
    assert_eq!(text(&req, "question"), "is this fine?");
    let flow = get(&req, "flow").unwrap();
    assert_eq!(text(flow, "environment"), JOB);
    assert_eq!(text(flow, "owner"), "org1/proj1/build");
    assert!(text(flow, "url").ends_with("/ask"));
    assert!(!w.upstream().is_null());

    // An operation that is not in the grant.
    let o = w.curl("localhost", "/", &["-H", "x-test-plugin: ask:other-op"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("not in the grant"), "{o:?}");

    // A review stops the flow at once; the record keeps it.
    let o = w.curl("localhost", "/later", &["-H", "x-test-plugin: review:please check"]).await;
    assert_eq!(o.code, 403, "{o:?}");
    assert!(o.body.contains("plugin test-actions: please check") && o.body.contains("blocked for review"), "{o:?}");
    assert_eq!(o.err, "");
    assert!(w.upstream().is_null(), "the request must not be sent");
    let flows = w.flows(JOB).await;
    assert!(flows.iter().any(|f| f["outcome"] == "review" && f["url"].as_str().unwrap().ends_with("/later")), "{flows:?}");
}

/// Sends CONNECT to the proxy and returns the status code and the stream.
async fn connect(w: &World, dest: &str) -> (u16, tokio::net::TcpStream) {
    use tokio::io::{AsyncReadExt as _, AsyncWriteExt as _};
    let mut s = tokio::net::TcpStream::connect(w.proxy_addr).await.unwrap();
    let auth = STANDARD.encode(format!("{JOB}:{}", proxy::credential(JOB_KEY, JOB)));
    s.write_all(format!("CONNECT {dest} HTTP/1.1\r\nHost: {dest}\r\nProxy-Authorization: Basic {auth}\r\n\r\n").as_bytes()).await.unwrap();
    let mut head = Vec::new();
    let mut b = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        if s.read(&mut b).await.unwrap() == 0 {
            break;
        }
        head.push(b[0]);
    }
    let line = String::from_utf8_lossy(&head).to_string();
    (line.split(' ').nth(1).and_then(|c| c.parse().ok()).unwrap_or(0), s)
}

fn dns_query(name: &str, t: u16) -> Vec<u8> {
    let mut q = vec![0xab, 0xcd, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
    for l in name.split('.') {
        q.push(l.len() as u8);
        q.extend_from_slice(l.as_bytes());
    }
    q.extend_from_slice(&[0, (t >> 8) as u8, t as u8, 0, 1]);
    q
}

/// A layer 4 destination passes bytes; a protocol that the proxy cannot
/// inspect stops at an inspected destination; DNS queries for permitted
/// names reach the resolver, others are refused.
#[tokio::test]
async fn protocols_layer4_and_dns() {
    use tokio::io::{AsyncReadExt as _, AsyncWriteExt as _};
    let echo = tokio::net::TcpListener::bind("127.0.0.2:0").await.unwrap();
    let echo_port = echo.local_addr().unwrap().port();
    let w = world_with(Opts { passthrough_port: Some(echo_port), ..Default::default() }).await;

    // Layer 4 (a passthrough destination): bytes go both ways.
    tokio::spawn(async move {
        let (mut c, _) = echo.accept().await.unwrap();
        let mut b = [0u8; 5];
        c.read_exact(&mut b).await.unwrap();
        c.write_all(&b).await.unwrap();
    });
    let (code, mut s) = connect(&w, &format!("127.0.0.2:{echo_port}")).await;
    assert_eq!(code, 200);
    s.write_all(b"\x00lk\x01!").await.unwrap();
    let mut b = [0u8; 5];
    s.read_exact(&mut b).await.unwrap();
    assert_eq!(&b, b"\x00lk\x01!");

    // Not TLS or HTTP to an inspected destination: closed and recorded.
    let (code, mut s) = connect(&w, &format!("localhost:{}", w.up_port)).await;
    assert_eq!(code, 200);
    s.write_all(b"\x00\x01\x02\x03binary").await.unwrap();
    let mut rest = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(10), s.read_to_end(&mut rest)).await.expect("the proxy must close the tunnel");
    assert!(rest.is_empty(), "{rest:?}");
    let flows = w.flows(JOB).await;
    assert!(flows.iter().any(|f| f["reasons"].to_string().contains("is not inspected")), "{flows:?}");

    // DNS: a fake resolver answers each query with its own ID and an
    // answer count of 1.
    let res = tokio::net::UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let res_addr = res.local_addr().unwrap();
    tokio::spawn(async move {
        let mut b = [0u8; 512];
        loop {
            let (n, from) = res.recv_from(&mut b).await.unwrap();
            let mut a = b[..n].to_vec();
            a[2] |= 0x80;
            a[7] = 1;
            res.send_to(&a, from).await.unwrap();
        }
    });
    // SAFETY: the only test that reads REACTORCIDE_EGRESS_DNS.
    unsafe { std::env::set_var("REACTORCIDE_EGRESS_DNS", res_addr.to_string()) };
    let (code, mut s) = connect(&w, "reactorcide-dns.internal:53").await;
    assert_eq!(code, 200);
    let mut ask = async |name: &str| {
        let q = dns_query(name, 16);
        s.write_all(&(q.len() as u16).to_be_bytes()).await.unwrap();
        s.write_all(&q).await.unwrap();
        let mut len = [0u8; 2];
        s.read_exact(&mut len).await.unwrap();
        let mut a = vec![0u8; u16::from_be_bytes(len) as usize];
        s.read_exact(&mut a).await.unwrap();
        a
    };
    let a = ask("_linkkeys.localhost").await;
    assert_eq!((a[3] & 0x0f, a[7]), (0, 1), "a permitted name reaches the resolver: {a:?}");
    let a = ask("_linkkeys.evil.example").await;
    assert_eq!(a[3] & 0x0f, 5, "refused: {a:?}");
    let flows = w.flows(JOB).await;
    assert!(flows.iter().any(|f| f["destination"] == "evil.example:53" && f["outcome"] == "blocked"), "{flows:?}");
}

fn ed25519(seed: u8) -> russh::keys::PrivateKey {
    russh::keys::PrivateKey::from(russh::keys::ssh_key::private::Ed25519Keypair::from_seed(&[seed; 32]))
}

/// The real SSH host: it accepts only the proxy's client key, and answers
/// each command with "ran <command>" and exit status 0.
async fn ssh_host(host_key: russh::keys::PrivateKey, client: russh::keys::PublicKey) -> u16 {
    use russh::server::{Auth, Msg, Session, VerifiedPublicKey};
    use russh::{Channel, ChannelId};
    #[derive(Clone)]
    struct H(russh::keys::PublicKey);
    impl russh::server::Handler for H {
        type Error = russh::Error;
        async fn auth_publickey(&mut self, user: &str, key: VerifiedPublicKey<'_>) -> Result<Auth, Self::Error> {
            let ok = user == "git" && key.public_key.key_data() == self.0.key_data();
            Ok(if ok { Auth::Accept } else { Auth::reject() })
        }
        async fn channel_open_session(&mut self, _c: Channel<Msg>, reply: russh::server::ChannelOpenHandle, _s: &mut Session) -> Result<(), Self::Error> {
            reply.accept().await;
            Ok(())
        }
        async fn exec_request(&mut self, id: ChannelId, data: &[u8], s: &mut Session) -> Result<(), Self::Error> {
            s.channel_success(id)?;
            s.data(id, format!("ran {}", String::from_utf8_lossy(data)).into_bytes())?;
            s.exit_status_request(id, 0)?;
            s.eof(id)?;
            s.close(id)
        }
    }
    let cfg = Arc::new(russh::server::Config { keys: vec![host_key], ..Default::default() });
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = l.local_addr().unwrap().port();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            let (cfg, h) = (cfg.clone(), H(client.clone()));
            tokio::spawn(async move {
                if let Ok(r) = russh::server::run_stream(cfg, s, h).await {
                    let _ = r.await;
                }
            });
        }
    });
    port
}

/// An SSH client in a job: it trusts only the proxy host key.
struct JobClient(russh::keys::PublicKey);
impl russh::client::Handler for JobClient {
    type Error = russh::Error;
    async fn check_server_key(&mut self, k: &russh::keys::PublicKeyOrCertificate) -> Result<bool, Self::Error> {
        Ok(matches!(k, russh::keys::PublicKeyOrCertificate::PublicKey { key, .. } if key.key_data() == self.0.key_data()))
    }
}

/// Runs one command over SSH through the proxy: (exit status, stdout,
/// stderr). A refused request is exit status 255.
async fn ssh_run(w: &World, port: u16, proxy_key: &russh::keys::PublicKey, request: &str) -> (u32, String, String) {
    let (code, s) = connect(w, &format!("localhost:{port}")).await;
    assert_eq!(code, 200);
    let cfg = Arc::new(russh::client::Config::default());
    let mut h = russh::client::connect_stream(cfg, s, JobClient(proxy_key.clone())).await.expect("the proxy host key");
    assert!(h.authenticate_none("anyone").await.unwrap().success());
    let mut ch = h.channel_open_session().await.unwrap();
    let r = match request.strip_prefix("shell") {
        Some(_) => ch.request_shell(true).await,
        None => ch.exec(true, request.as_bytes()).await,
    };
    r.unwrap();
    let (mut code, mut out, mut err) = (255, String::new(), String::new());
    while let Some(m) = tokio::time::timeout(Duration::from_secs(20), ch.wait()).await.expect("ssh answer") {
        match m {
            russh::ChannelMsg::Data { data } => out.push_str(&String::from_utf8_lossy(&data)),
            russh::ChannelMsg::ExtendedData { data, .. } => err.push_str(&String::from_utf8_lossy(&data)),
            russh::ChannelMsg::ExitStatus { exit_status } => code = exit_status,
            russh::ChannelMsg::Failure => break,
            russh::ChannelMsg::Close => break,
            _ => {}
        }
    }
    (code, out, err)
}

/// The proxy ends SSH with its own host key, checks each command, and runs
/// it on the real host with the key in a secret.
#[tokio::test]
async fn ssh_proxy() {
    let (host_key, proxy_client, proxy_host) = (ed25519(1), ed25519(2), ed25519(3));
    let port = ssh_host(host_key.clone(), proxy_client.public_key().clone()).await;
    let pem = proxy_client.to_openssh(russh::keys::ssh_key::LineEnding::LF).unwrap().to_string();
    let hosts = serde_json::json!([{"host": "localhost", "port": port, "host_keys": [host_key.public_key().to_openssh().unwrap()], "secret": "gh-ssh"}]);
    let w = world_with(Opts { ssh_hosts: Some(hosts), ssh_key: Some(proxy_host.clone()), more: vec![("gh-ssh".into(), pem.into_bytes())], ..Default::default() }).await;
    let pk = proxy_host.public_key().clone();

    let (code, out, err) = ssh_run(&w, port, &pk, "git-upload-pack 'org/repo.git'").await;
    assert_eq!((code, out.as_str()), (0, "ran git-upload-pack 'org/repo.git'"), "{err}");

    // A command with a placeholder: not run.
    let (code, out, err) = ssh_run(&w, port, &pk, &format!("git-receive-pack '{PH}'")).await;
    assert_eq!(code, 1, "{out} {err}");
    assert!(err.contains("in an SSH command") && out.is_empty(), "{err}");

    // A shell is refused.
    let (code, out, _) = ssh_run(&w, port, &pk, "shell").await;
    assert_eq!((code, out.as_str()), (255, ""));

    let flows = w.proxy.records.list(Some(JOB), 50);
    assert!(flows.iter().any(|f| f.url.as_deref() == Some("ssh://localhost/git-upload-pack 'org/repo.git'") && f.outcome == "sent"), "{flows:?}");
    assert!(flows.iter().any(|f| f.outcome == "blocked" && f.method.as_deref() == Some("SSH")), "{flows:?}");
}

#[tokio::test]
async fn event_streams_pass_one_event_at_a_time() {
    need_curl!();
    let w = world().await;
    // The first event comes before the pause of the upstream ends.
    let o = w.curl("localhost", "/sse", &["-N", "--max-time", "1.5"]).await;
    assert!(o.body.contains(r#"data: {"n":1}"#), "first event was held back: {o:?}");
    assert!(!o.body.contains(r#""n":2"#), "{o:?}");
    // The full stream: the secrets are removed from the second event.
    let o = w.curl("localhost", "/sse", &["-N"]).await;
    assert_eq!(o.code, 200, "{o:?}");
    assert!(o.body.contains(r#""n":2"#) && o.body.contains("[DONE]"), "{o:?}");
    assert!(!o.body.contains(SECRET) && !o.body.contains(GRANT_VALUE), "secret in the stream: {o:?}");
    assert!(o.body.contains("[redacted:gh]") && o.body.contains("[redacted:g-plain]"), "{o:?}");
}
