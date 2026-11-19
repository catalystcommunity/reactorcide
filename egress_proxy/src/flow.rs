//! The work on one flow (docs/egress-proxy.md, "Order of work"):
//! destination check, decode, built-in secret scan, plugins, review, grant
//! substitution, plugin secrets, placeholder check, send, and record. The steps that keep secrets in (the
//! destination check, the decode depth, the scan, and the placeholder
//! check) are here in the host, so a plugin cannot turn them off.

use std::sync::Arc;
use std::time::Instant;

use bytes::Bytes;
use http::{HeaderMap, HeaderName, HeaderValue, Request, Response, StatusCode};
use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt as _, Full, Limited};
use rand::Rng as _;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;

use crate::ask::Ask;
use crate::ca::Ca;
use crate::jobs::{JobState, Jobs};
use crate::net::Client;
use crate::records::{FlowRecord, Records};
use crate::plugins::{Action, Direction, Flow, Header, Plugins, SecretUse};
use crate::policy::Policy;
use crate::scan::{self, PLACEHOLDER, Secrets};

pub type Body = BoxBody<Bytes, hyper::Error>;

/// Headers that belong to one connection. The proxy does not pass them on.
const HOP: &[&str] = &[
    "connection", "keep-alive", "proxy-authorization", "proxy-authenticate", "proxy-connection", "te", "trailer",
    "transfer-encoding", "upgrade", "content-length", "content-encoding", "host",
];

/// What a destination may do.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Access {
    No,
    /// HTTP and TLS, inspected.
    Inspect,
    /// Bytes only, not inspected (protocols that the proxy does not end).
    Layer4,
}

pub struct Proxy {
    /// The global policy of the operator.
    pub policy: Policy,
    /// The operator secrets (plugin use-secret, SSH keys).
    pub secrets: Arc<Secrets>,
    pub plugins: Plugins,
    pub ask: Option<Arc<Ask>>,
    pub records: Arc<Records>,
    /// The registered jobs (control API).
    pub jobs: Arc<Jobs>,
    pub ca: Ca,
    pub upstream: Client,
    /// The HMAC key of the job credentials.
    pub job_key: Vec<u8>,
    /// Accept a valid credential of a job that is not registered. The job
    /// then gets only the global policy and no grants.
    pub allow_unregistered: bool,
    /// The SSH host key of the proxy (ssh.rs). None turns SSH off.
    pub ssh_key: Option<russh::keys::PrivateKey>,
}

/// The job that sent a flow, from its proxy credential.
#[derive(Clone, Default)]
pub struct Peer {
    pub job: String,
    /// The identity summary of the job ("org/project/node").
    pub identity: String,
    /// The registration of the job. None only with allow_unregistered.
    pub state: Option<Arc<JobState>>,
}

/// Where the flow goes.
#[derive(Debug, Clone)]
pub struct Target {
    pub scheme: &'static str,
    pub host: String,
    pub port: u16,
}

impl Target {
    pub fn destination(&self) -> String {
        format!("{}:{}", self.host, self.port)
    }
    fn base(&self) -> String {
        let default = (self.scheme == "https" && self.port == 443) || (self.scheme == "http" && self.port == 80);
        // An IPv6 literal needs brackets in a URL.
        let host = if self.host.contains(':') && !self.host.starts_with('[') { format!("[{}]", self.host) } else { self.host.clone() };
        if default { format!("{}://{host}", self.scheme) } else { format!("{}://{host}:{}", self.scheme, self.port) }
    }
}

/// The result of one step: go on, or answer the client now.
type Step<T> = Result<T, Stop>;

#[derive(Debug)]
pub struct Stop {
    pub status: StatusCode,
    pub reason: String,
    pub outcome: &'static str,
}

fn block(reason: impl Into<String>) -> Stop {
    Stop { status: StatusCode::FORBIDDEN, reason: reason.into(), outcome: "blocked" }
}

/// Blocked now because a plugin asked for a review. The flow record keeps
/// the reason, so an operator can change the policy.
fn review(reason: impl Into<String>) -> Stop {
    Stop {
        status: StatusCode::FORBIDDEN,
        reason: format!("{} (blocked for review; the request was not sent)", reason.into()),
        outcome: "review",
    }
}

pub fn new_id(prefix: &str) -> String {
    format!("{prefix}{:016x}", rand::rng().random::<u64>())
}

pub fn text_response(status: StatusCode, outcome: &str, text: &str) -> Response<Body> {
    let mut r = Response::new(full(format!("reactorcide egress: {text}\n").into_bytes()));
    *r.status_mut() = status;
    r.headers_mut().insert("x-reactorcide-egress", HeaderValue::from_str(outcome).unwrap_or(HeaderValue::from_static("blocked")));
    r.headers_mut().insert(http::header::CONTENT_TYPE, HeaderValue::from_static("text/plain; charset=utf-8"));
    r
}

pub fn full(b: Vec<u8>) -> Body {
    Full::new(Bytes::from(b)).map_err(|never| match never {}).boxed()
}

impl Proxy {
    /// Checks the destination of an HTTP request. See `access`.
    pub async fn permitted(&self, peer: &Peer, t: &Target, method: &str, url: &str) -> Result<(), Stop> {
        self.access(peer, t, method, url).await.map(|_| ())
    }

    /// What the destination may do. The global policy permits it
    /// (`destinations` inspected, `passthrough` layer 4), or the job
    /// registration permits it (job destinations and grant hosts,
    /// inspected). Else the flow stops, and the record keeps the reason.
    pub async fn access(&self, peer: &Peer, t: &Target, _method: &str, _url: &str) -> Result<Access, Stop> {
        if self.policy.destination(&peer.job, &t.host, t.port)
            || self.policy.ssh_host_for(&peer.job, &t.host, t.port).is_some()
            || peer.state.as_ref().is_some_and(|s| s.destination(&t.host, t.port))
        {
            return Ok(Access::Inspect);
        }
        if self.policy.passthrough(&peer.job, &t.host, t.port) {
            return Ok(Access::Layer4);
        }
        Err(block(format!("destination {} is not permitted for this job", t.destination())))
    }

    /// Stops a destination that the proxy cannot inspect.
    pub fn refuse_destination(&self, t: &Target, reason: String) -> Stop {
        tracing::info!(destination = %t.destination(), reason = %reason, "destination refused");
        block(reason)
    }

    /// Stops a flow that a plugin wants a person to review. The flow is
    /// after the scan: it has placeholders, not secrets.
    fn review(&self, f: &Flow, reviews: Vec<(String, String)>) -> Stop {
        let reason = reviews.iter().map(|(p, r)| format!("plugin {p}: {r}")).collect::<Vec<_>>().join("; ");
        tracing::warn!(flow = %f.id, job = %f.environment, destination = %f.destination, reason = %reason, "flow blocked for review");
        review(reason)
    }

    pub fn record(&self, peer: &Peer, t: &Target, outcome: &str, reasons: Vec<String>) {
        self.records.add(FlowRecord {
            id: new_id("flw_"),
            job_id: peer.job.clone(),
            identity: peer.identity.clone(),
            destination: t.destination(),
            method: None,
            url: None,
            status: None,
            outcome: outcome.into(),
            reasons,
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: crate::now_ms(),
            duration_ms: 0,
        });
    }

    /// One request and its response.
    pub async fn handle<B>(self: Arc<Self>, peer: Peer, t: Target, req: Request<B>) -> Response<Body>
    where
        B: hyper::body::Body<Data = Bytes> + Send,
        B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
    {
        let start = Instant::now();
        let mut rec = FlowRecord {
            id: new_id("flw_"),
            job_id: peer.job.clone(),
            identity: peer.identity.clone(),
            destination: t.destination(),
            method: Some(req.method().to_string()),
            url: None,
            status: None,
            outcome: "sent".into(),
            reasons: vec![],
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: crate::now_ms(),
            duration_ms: 0,
        };
        let res = match self.exchange(&mut rec, &peer, &t, req).await {
            Ok(r) => r,
            Err(s) => {
                rec.outcome = s.outcome.into();
                rec.reasons.push(s.reason.clone());
                text_response(s.status, s.outcome, &s.reason)
            }
        };
        rec.status = Some(res.status().as_u16() as u64);
        rec.duration_ms = start.elapsed().as_millis() as u64;
        self.records.add(rec);
        res
    }

    async fn exchange<B>(&self, rec: &mut FlowRecord, peer: &Peer, t: &Target, req: Request<B>) -> Step<Response<Body>>
    where
        B: hyper::body::Body<Data = Bytes> + Send,
        B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
    {
        // 1. Destination.
        let path = req.uri().path_and_query().map_or("/", |p| p.as_str());
        let url = format!("{}{path}", t.base());
        self.permitted(peer, t, req.method().as_str(), &url).await?;
        if let Some(h) = req.headers().get(http::header::HOST).and_then(|v| v.to_str().ok()) {
            let name = h.rsplit_once(':').filter(|(_, p)| p.parse::<u16>().is_ok()).map_or(h, |(n, _)| n);
            if !name.eq_ignore_ascii_case(&t.host) {
                return Err(block(format!("Host {h} is not the destination {}", t.host)));
            }
        }
        if req.method() == http::Method::CONNECT || req.headers().contains_key(http::header::UPGRADE) {
            return Err(block("protocol upgrades and nested tunnels are not permitted"));
        }
        let (parts, body) = req.into_parts();
        rec.url = Some(url.clone());

        // 2. Decode.
        let raw = Limited::new(body, self.policy.max_body)
            .collect()
            .await
            .map_err(|e| block(format!("request body: {e} (the limit is {} bytes)", self.policy.max_body)))?
            .to_bytes();
        let body = decode_body(&parts.headers, &raw, self.policy.max_body)?;

        // 3. Built-in secret scan.
        let depth = self.policy.depth_for(&t.host);
        let mut flow = Flow {
            id: rec.id.clone(),
            environment: peer.job.clone(),
            owner: peer.identity.clone(),
            direction: Direction::Out,
            destination: t.destination(),
            method: parts.method.to_string(),
            url: url.clone(),
            status: 0,
            headers: headers_of(&parts.headers),
            body,
        };
        self.clean(rec, &mut flow, peer.state.as_deref(), Some(depth))?;

        // 4. Plugins.
        let (reviews, uses) = self.run_plugins(rec, &mut flow).await?;
        self.clean(rec, &mut flow, peer.state.as_deref(), Some(depth))?;

        // 5. Review: block now; the record keeps the reason.
        if !reviews.is_empty() {
            return Err(self.review(&flow, reviews));
        }

        // 6. The grants of the job: the real value in place of the
        // placeholder, only in the grant header and only for grant hosts.
        // A secret goes only to an upstream over TLS: on a plain HTTP flow
        // (http:// or plain HTTP in a CONNECT tunnel) anyone on the path
        // could read it.
        let tls = t.scheme == "https";
        if let Some(state) = &peer.state {
            apply_grants(rec, &mut flow, state, &t.host, tls)?;
        }
        // 7. Operator secrets that plugins ask for. Then no placeholder
        // must stay in the URL or a header. A placeholder in the body is
        // not a secret and passes (for example a log upload).
        if !uses.is_empty() && !tls {
            return Err(block(format!("a plugin secret goes only over TLS; {} is plain HTTP", t.base())));
        }
        for (plugin, u) in uses {
            self.use_secret(rec, &mut flow, &t.host, &plugin, &u)?;
        }
        let left = placeholders(&flow, false);
        if !left.is_empty() {
            return Err(block(format!("no secret for placeholder {} at {}", left.join(", "), t.host)));
        }

        // 7. Send.
        let mut up = Request::builder().method(parts.method.clone()).uri(&flow.url);
        for h in &flow.headers {
            if !HOP.contains(&h.name.to_ascii_lowercase().as_str()) && !h.name.eq_ignore_ascii_case("accept-encoding") {
                up = up.header(&h.name, &h.value);
            }
        }
        // Ask for an identity body, so the response can be scanned.
        up = up.header(http::header::ACCEPT_ENCODING, "identity");
        let up = up.body(Full::new(Bytes::from(std::mem::take(&mut flow.body)))).map_err(|e| block(format!("request: {e}")))?;
        let res = self
            .upstream
            .request(up)
            .await
            .map_err(|e| Stop { status: StatusCode::BAD_GATEWAY, reason: format!("upstream {}: {}", t.destination(), err_chain(&e)), outcome: "failed" })?;
        self.response(rec, peer, t, &parts.method, &url, res).await
    }

    async fn response(&self, rec: &mut FlowRecord, peer: &Peer, t: &Target, method: &http::Method, url: &str, res: Response<hyper::body::Incoming>) -> Step<Response<Body>> {
        let (parts, body) = res.into_parts();
        let len = parts.headers.get(http::header::CONTENT_LENGTH).and_then(|v| v.to_str().ok()).and_then(|v| v.parse::<usize>().ok());
        if len.is_some_and(|n| n > self.policy.max_body) {
            if self.policy.streams(&t.host) {
                rec.reasons.push("large response sent without inspection".into());
                let mut out = Response::from_parts(parts, body.boxed());
                strip(out.headers_mut(), false);
                return Ok(out);
            }
            return Err(Stop { status: StatusCode::BAD_GATEWAY, reason: format!("response is larger than {} bytes", self.policy.max_body), outcome: "blocked" });
        }
        if parts.status.is_success() && is_event_stream(&parts.headers) && self.policy.event_streams(&t.host) {
            // The headers get the scan of each response; the body is scanned
            // one event at a time while it goes to the client.
            let mut flow = Flow {
                id: rec.id.clone(),
                environment: peer.job.clone(),
                owner: peer.identity.clone(),
                direction: Direction::In,
                destination: t.destination(),
                method: method.to_string(),
                url: url.to_string(),
                status: parts.status.as_u16(),
                headers: headers_of(&parts.headers),
                body: Vec::new(),
            };
            self.clean(rec, &mut flow, peer.state.as_deref(), None)?;
            rec.reasons.push("event stream: each event scanned, no plugins".into());
            let mut out = Response::new(event_stream(self.secrets.clone(), peer.state.clone(), body, self.policy.max_body));
            *out.status_mut() = parts.status;
            for h in &flow.headers {
                if let (Ok(n), Ok(v)) = (HeaderName::from_bytes(h.name.as_bytes()), HeaderValue::from_str(&h.value)) {
                    out.headers_mut().append(n, v);
                }
            }
            strip(out.headers_mut(), true);
            return Ok(out);
        }
        let raw = Limited::new(body, self.policy.max_body)
            .collect()
            .await
            .map_err(|e| Stop { status: StatusCode::BAD_GATEWAY, reason: format!("response body: {e}"), outcome: "blocked" })?
            .to_bytes();
        let body = decode_body(&parts.headers, &raw, self.policy.max_body)?;
        let mut flow = Flow {
            id: rec.id.clone(),
            environment: peer.job.clone(),
            owner: peer.identity.clone(),
            direction: Direction::In,
            destination: t.destination(),
            method: method.to_string(),
            url: url.to_string(),
            status: parts.status.as_u16(),
            headers: headers_of(&parts.headers),
            body,
        };
        // A response can carry encoded data by design: no depth limit.
        self.clean(rec, &mut flow, peer.state.as_deref(), None)?;
        let (reviews, uses) = self.run_plugins(rec, &mut flow).await?;
        if !uses.is_empty() {
            return Err(block("a plugin tried to put a secret in a response"));
        }
        self.clean(rec, &mut flow, peer.state.as_deref(), None)?;
        if !reviews.is_empty() {
            return Err(self.review(&flow, reviews));
        }
        let mut out = Response::new(full(flow.body));
        *out.status_mut() = parts.status;
        for h in &flow.headers {
            if let (Ok(n), Ok(v)) = (HeaderName::from_bytes(h.name.as_bytes()), HeaderValue::from_str(&h.value)) {
                out.headers_mut().append(n, v);
            }
        }
        strip(out.headers_mut(), true);
        Ok(out)
    }

    /// Steps 3 to 5 for a flow that is not an HTTP request (an SSH
    /// command): the scan, the plugins, and the review. A flow of this kind
    /// cannot take secrets, and must have no placeholder.
    pub async fn inspect_out(&self, rec: &mut FlowRecord, f: &mut Flow, state: Option<&JobState>, host: &str) -> Step<()> {
        let depth = self.policy.depth_for(host);
        self.clean(rec, f, state, Some(depth))?;
        let (reviews, uses) = self.run_plugins(rec, f).await?;
        self.clean(rec, f, state, Some(depth))?;
        if !reviews.is_empty() {
            return Err(self.review(f, reviews));
        }
        if !uses.is_empty() {
            return Err(block("a plugin tried to put a secret in an SSH command"));
        }
        let left = placeholders(f, true);
        if !left.is_empty() {
            return Err(block(format!("placeholder {} in an SSH command", left.join(", "))));
        }
        Ok(())
    }

    /// The built-in scan of the URL, the headers, and the body. With
    /// `depth`, data that is encoded more times stops the flow.
    fn clean(&self, rec: &mut FlowRecord, f: &mut Flow, state: Option<&JobState>, depth: Option<u32>) -> Step<()> {
        let limit = depth.unwrap_or(16);
        let mut check = |data: &[u8], what: &str| -> Step<Vec<u8>> {
            let s = scan_all(data, &self.secrets, state.map(|s| &s.secrets), limit);
            if !s.hidden.is_empty() {
                return Err(block(format!("{what} has secret {} in an encoded form", s.hidden.join(", "))));
            }
            if let Some(d) = depth
                && s.depth > d
            {
                return Err(block(format!("{what} is encoded {} times; the limit is {d}", s.depth)));
            }
            rec.redacted += s.redacted.len() as u64;
            for id in s.redacted {
                rec.reasons.push(format!("removed secret {id} from {what}"));
            }
            Ok(s.data)
        };
        if f.direction == Direction::Out {
            f.url = String::from_utf8_lossy(&check(f.url.as_bytes(), "the URL")?).into_owned();
        }
        for h in f.headers.iter_mut() {
            h.value = String::from_utf8_lossy(&check(h.value.as_bytes(), &format!("header {}", h.name))?).into_owned();
        }
        f.body = check(&f.body, "the body")?;
        Ok(())
    }

    async fn run_plugins(&self, rec: &mut FlowRecord, f: &mut Flow) -> Step<(Vec<(String, String)>, Vec<(String, SecretUse)>)> {
        let mut reviews = Vec::new();
        let mut uses = Vec::new();
        let list: Vec<_> = self.plugins.for_flow(f).collect();
        for p in list {
            let name = p.grant.name.clone();
            if !rec.plugins.contains(&name) {
                rec.plugins.push(name.clone());
            }
            let actions = self.plugins.run(p, f).await.map_err(|e| block(e.to_string()))?;
            for a in actions {
                match a {
                    Action::Pass => {}
                    Action::Block(r) => return Err(block(format!("plugin {name}: {r}"))),
                    Action::Review(r) => reviews.push((name.clone(), r)),
                    Action::SetBody(b) => {
                        rec.reasons.push(format!("plugin {name} changed the body"));
                        f.body = b;
                    }
                    Action::SetHeader(h) => {
                        if HOP.contains(&h.name.to_ascii_lowercase().as_str()) || HeaderName::from_bytes(h.name.as_bytes()).is_err() {
                            return Err(block(format!("plugin {name} cannot set header {:?}", h.name)));
                        }
                        f.headers.retain(|x| !x.name.eq_ignore_ascii_case(&h.name));
                        if !h.value.is_empty() {
                            f.headers.push(h.clone());
                        }
                        rec.reasons.push(format!("plugin {name} set header {}", h.name));
                    }
                    Action::UseSecret(u) => uses.push((name.clone(), u)),
                }
            }
        }
        Ok((reviews, uses))
    }

    fn use_secret(&self, rec: &mut FlowRecord, f: &mut Flow, host: &str, plugin: &str, u: &SecretUse) -> Step<()> {
        if !PLACEHOLDER.find(u.placeholder.as_bytes()).is_some_and(|m| m.as_bytes() == u.placeholder.as_bytes()) {
            return Err(block(format!("plugin {plugin}: {:?} is not a placeholder", u.placeholder)));
        }
        let Some(value) = self.secrets.get(&u.secret) else {
            return Err(block(format!("plugin {plugin}: no secret {}", u.secret)));
        };
        if !self.policy.secret_permitted(&u.secret, host) {
            return Err(block(format!("secret {} is not permitted for {host}", u.secret)));
        }
        let value = String::from_utf8_lossy(value).into_owned();
        let (header, url, body) = match u.place.as_str() {
            "any" => (None, true, true),
            "url" => (Some(""), true, false),
            "body" => (Some(""), false, true),
            p => match p.strip_prefix("header:") {
                Some(n) if !n.is_empty() => (Some(n), false, false),
                _ => return Err(block(format!("plugin {plugin}: place {p:?} is not valid"))),
            },
        };
        let mut n = 0;
        for h in f.headers.iter_mut() {
            if header.is_none() || header.is_some_and(|x| x.eq_ignore_ascii_case(&h.name)) {
                n += h.value.matches(&u.placeholder).count();
                h.value = h.value.replace(&u.placeholder, &value);
            }
        }
        if url {
            n += f.url.matches(&u.placeholder).count();
            f.url = f.url.replace(&u.placeholder, &value);
        }
        if body {
            let re = regex::bytes::Regex::new(&regex::escape(&u.placeholder)).expect("escaped");
            n += re.find_iter(&f.body).count();
            f.body = re.replace_all(&f.body, value.as_bytes()).into_owned();
        }
        if n > 0 && !rec.secrets_used.contains(&u.secret) {
            rec.secrets_used.push(u.secret.clone());
        }
        Ok(())
    }
}

/// The scan with the operator secrets, then with the grant values of the
/// job.
fn scan_all(data: &[u8], global: &Secrets, job: Option<&Secrets>, limit: u32) -> scan::Scanned {
    let mut s = scan::scan(data, global, limit);
    if let Some(j) = job.filter(|j| !j.is_empty()) {
        let t = scan::scan(&s.data, j, limit);
        s.data = t.data;
        s.redacted.extend(t.redacted);
        s.hidden.extend(t.hidden);
        s.depth = s.depth.max(t.depth);
        s.redacted.sort();
        s.redacted.dedup();
        s.hidden.sort();
        s.hidden.dedup();
    }
    s
}

/// The built-in grant step. For each grant whose hosts permit `host`, the
/// value goes in place of the placeholder in the grant header (and only
/// there). With a format, the full header becomes the format with the
/// value. A Basic credential (git over HTTPS) is decoded, changed, and
/// encoded again. A grant for other hosts changes nothing, so the
/// placeholder check stops the request.
///
/// Without TLS to the upstream (`tls` false), no value goes in: a grant
/// placeholder in the URL or a header (also inside a Basic credential)
/// stops the request with 403.
pub fn apply_grants(rec: &mut FlowRecord, f: &mut Flow, state: &JobState, host: &str, tls: bool) -> Result<(), Stop> {
    for g in &state.policy.grants {
        if !g.permits(host) {
            if f.headers.iter().any(|h| h.value.contains(&g.placeholder)) || f.url.contains(&g.placeholder) {
                rec.reasons.push(format!("grant {} does not permit host {host}", g.id));
            }
            continue;
        }
        if !tls {
            let in_basic = |v: &str| {
                let v = v.trim();
                v.len() > 6
                    && v.is_char_boundary(6)
                    && v[..6].eq_ignore_ascii_case("basic ")
                    && STANDARD.decode(v[6..].trim()).is_ok_and(|raw| String::from_utf8_lossy(&raw).contains(&g.placeholder))
            };
            if f.url.contains(&g.placeholder) || f.headers.iter().any(|h| h.value.contains(&g.placeholder) || in_basic(&h.value)) {
                return Err(block(format!("grant {}: a secret goes only over TLS; http://{host} is plain HTTP, use https", g.id)));
            }
            continue;
        }
        let mut used = false;
        for h in f.headers.iter_mut().filter(|h| h.name.eq_ignore_ascii_case(&g.header)) {
            if h.value.contains(&g.placeholder) {
                h.value = match &g.format {
                    Some(fmt) => fmt.replacen("{}", &g.value, 1),
                    None => h.value.replace(&g.placeholder, &g.value),
                };
                used = true;
                continue;
            }
            let v = h.value.trim();
            if v.len() > 6
                && v.is_char_boundary(6)
                && v[..6].eq_ignore_ascii_case("basic ")
                && let Ok(raw) = STANDARD.decode(v[6..].trim())
                && let Ok(text) = String::from_utf8(raw)
                && text.contains(&g.placeholder)
            {
                h.value = format!("Basic {}", STANDARD.encode(text.replace(&g.placeholder, &g.value)));
                used = true;
            }
        }
        if used && !rec.secrets_used.contains(&g.id) {
            rec.secrets_used.push(g.id.clone());
        }
    }
    Ok(())
}

/// The placeholders in the URL, the headers, and (with body) the body.
fn placeholders(f: &Flow, body: bool) -> Vec<String> {
    let mut v: Vec<String> = Vec::new();
    let mut add = |b: &[u8]| {
        for m in PLACEHOLDER.find_iter(b) {
            v.push(String::from_utf8_lossy(m.as_bytes()).into_owned());
        }
    };
    add(f.url.as_bytes());
    for h in &f.headers {
        add(h.value.as_bytes());
    }
    if body {
        add(&f.body);
    }
    v.sort();
    v.dedup();
    v
}

fn headers_of(h: &HeaderMap) -> Vec<Header> {
    h.iter()
        .filter(|(n, _)| !matches!(n.as_str(), "proxy-authorization" | "proxy-connection" | "content-length" | "content-encoding" | "transfer-encoding"))
        .map(|(n, v)| Header { name: n.as_str().to_string(), value: String::from_utf8_lossy(v.as_bytes()).into_owned() })
        .collect()
}

/// A text/event-stream body with no content encoding (the proxy asks for
/// identity; an encoded stream gets the full inspection).
fn is_event_stream(h: &HeaderMap) -> bool {
    let ct = h.get(http::header::CONTENT_TYPE).and_then(|v| v.to_str().ok()).unwrap_or("");
    let enc = h.get(http::header::CONTENT_ENCODING).and_then(|v| v.to_str().ok()).unwrap_or("identity");
    ct.trim_start().to_ascii_lowercase().starts_with("text/event-stream") && enc.eq_ignore_ascii_case("identity")
}

/// The end of the first complete event in `buf` (after its blank line).
fn event_end(buf: &[u8]) -> Option<usize> {
    let lf = buf.windows(2).position(|w| w == b"\n\n").map(|i| i + 2);
    let crlf = buf.windows(4).position(|w| w == b"\r\n\r\n").map(|i| i + 4);
    match (lf, crlf) {
        (Some(a), Some(b)) => Some(a.min(b)),
        (a, b) => a.or(b),
    }
}

/// Scans one event. None: the event has a secret in a form that the scan
/// cannot remove, and the stream must stop.
fn clean_event(ev: &[u8], secrets: &Secrets, job: Option<&Secrets>) -> Option<Vec<u8>> {
    let s = scan_all(ev, secrets, job, 16);
    if !s.hidden.is_empty() {
        tracing::warn!(secrets = ?s.hidden, "event stream stopped: a secret in an encoded form");
        return None;
    }
    if !s.redacted.is_empty() {
        tracing::warn!(secrets = ?s.redacted, "removed secrets from an event stream");
    }
    Some(s.data)
}

/// Sends `body` to the client one event at a time, each scanned for
/// secrets. A secret that the scan cannot remove, an upstream error, or an
/// event larger than `max_event` ends the stream. A secret split over two
/// events is not found: use this only for trusted hosts.
fn event_stream(secrets: Arc<Secrets>, job: Option<Arc<JobState>>, mut body: hyper::body::Incoming, max_event: usize) -> Body {
    let (mut tx, rx) = http_body_util::Channel::<Bytes, std::convert::Infallible>::new(16);
    tokio::spawn(async move {
        let mut buf: Vec<u8> = Vec::new();
        while let Some(frame) = body.frame().await {
            let Ok(frame) = frame else { return };
            let Ok(data) = frame.into_data() else { continue };
            buf.extend_from_slice(&data);
            while let Some(end) = event_end(&buf) {
                let ev: Vec<u8> = buf.drain(..end).collect();
                let Some(out) = clean_event(&ev, &secrets, job.as_ref().map(|j| &j.secrets)) else { return };
                if tx.send_data(Bytes::from(out)).await.is_err() {
                    return;
                }
            }
            if buf.len() > max_event {
                tracing::warn!(max_event, "event stream stopped: an event is too large");
                return;
            }
        }
        if !buf.is_empty()
            && let Some(out) = clean_event(&buf, &secrets, job.as_ref().map(|j| &j.secrets))
        {
            let _ = tx.send_data(Bytes::from(out)).await;
        }
    });
    rx.map_err(|never| match never {}).boxed()
}

fn strip(h: &mut HeaderMap, decoded: bool) {
    for n in ["connection", "keep-alive", "proxy-authenticate", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade"] {
        h.remove(n);
    }
    if decoded {
        h.remove(http::header::CONTENT_ENCODING);
        h.remove(http::header::CONTENT_LENGTH);
    }
}

/// Removes the content encoding. An encoding that the proxy cannot read
/// stops the flow.
fn decode_body(h: &HeaderMap, raw: &[u8], max: usize) -> Step<Vec<u8>> {
    use std::io::Read as _;
    let enc = h.get(http::header::CONTENT_ENCODING).and_then(|v| v.to_str().ok()).unwrap_or("").trim().to_ascii_lowercase();
    match enc.as_str() {
        "" | "identity" => Ok(raw.to_vec()),
        "gzip" | "x-gzip" => scan::gunzip(raw, max).ok_or_else(|| block("the gzip body cannot be decoded")),
        "deflate" => {
            let mut out = Vec::new();
            flate2::read::ZlibDecoder::new(raw).take(max as u64 + 1).read_to_end(&mut out).map_err(|_| block("the deflate body cannot be decoded"))?;
            if out.len() > max {
                return Err(block("the decoded body is too large"));
            }
            Ok(out)
        }
        e => Err(block(format!("content encoding {e:?} is not supported"))),
    }
}

fn err_chain(e: &dyn std::error::Error) -> String {
    let mut s = e.to_string();
    let mut cur = e.source();
    while let Some(c) = cur {
        s.push_str(": ");
        s.push_str(&c.to_string());
        cur = c.source();
    }
    s
}

#[cfg(test)]
mod tests {
    use super::Target;

    #[test]
    fn base_brackets_ipv6_literals() {
        let t = |scheme, host: &str, port| Target { scheme, host: host.into(), port };
        assert_eq!(t("https", "fd00::7", 443).base(), "https://[fd00::7]");
        assert_eq!(t("http", "::1", 8080).base(), "http://[::1]:8080");
        assert_eq!(t("https", "api.github.com", 443).base(), "https://api.github.com");
        assert_eq!(t("http", "10.0.0.1", 3000).base(), "http://10.0.0.1:3000");
    }
}
