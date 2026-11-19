//! DNS for jobs with the transparent route (reactorcide-egress-net). The
//! job asks for names locally (A records get fake addresses there); other
//! record types, such as TXT, come here: a CONNECT to DNS_HOST:53, then DNS over TCP
//! (RFC 1035 section 4.2.2: each message has a 2-byte length).
//!
//! A query is answered only for a permitted name: a destination or
//! passthrough host of the policy (any port), a destination of the job or
//! a host of its grants, or a destination on port 53. Leading labels that
//! start with "_" do not count (`_linkkeys.example.org` is a query for
//! example.org). Other queries get REFUSED. The proxy asks the resolver of
//! its own host.

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use tokio::io::{AsyncRead, AsyncReadExt as _, AsyncWrite, AsyncWriteExt as _};
use tokio::net::{TcpStream, UdpSocket};

use crate::flow::{Peer, Proxy, Target};

/// The CONNECT host of the DNS service.
pub const DNS_HOST: &str = "reactorcide-dns.internal";

/// The question of a DNS message: the lower-case name and the type.
pub fn question(m: &[u8]) -> Option<(String, u16)> {
    if m.len() < 12 || u16::from_be_bytes([m[4], m[5]]) != 1 {
        return None;
    }
    let mut i = 12;
    let mut labels = Vec::new();
    loop {
        let n = *m.get(i)? as usize;
        if n == 0 {
            i += 1;
            break;
        }
        if n & 0xc0 != 0 || labels.len() > 127 {
            return None; // no compression in a question
        }
        labels.push(String::from_utf8_lossy(m.get(i + 1..i + 1 + n)?).to_ascii_lowercase());
        i += 1 + n;
    }
    let t = u16::from_be_bytes([*m.get(i)?, *m.get(i + 1)?]);
    Some((labels.join("."), t))
}

/// The name that a policy checks: without leading "_" labels.
pub fn base_name(name: &str) -> &str {
    let mut n = name.trim_end_matches('.');
    while let Some(rest) = n.strip_prefix('_').and_then(|r| r.split_once('.').map(|(_, rest)| rest)) {
        n = rest;
    }
    n
}

/// A REFUSED answer to a query.
pub fn refused(q: &[u8]) -> Vec<u8> {
    let mut r = q.to_vec();
    if r.len() >= 4 {
        r[2] |= 0x80; // response
        r[3] = (r[3] & 0xf0) | 5; // REFUSED
        r[3] |= 0x80; // recursion available
    }
    if r.len() >= 12 {
        r[6..12].fill(0); // no answers, authority, or additional records
        // Keep only the question.
        if let Some(end) = question_end(&r) {
            r.truncate(end);
        }
    }
    r
}

fn question_end(m: &[u8]) -> Option<usize> {
    let mut i = 12;
    loop {
        let n = *m.get(i)? as usize;
        i += 1;
        if n == 0 {
            break;
        }
        i += n;
    }
    (i + 4 <= m.len()).then_some(i + 4)
}

/// The resolver: REACTORCIDE_EGRESS_DNS (host:port), or the first nameserver in
/// /etc/resolv.conf.
pub fn resolver() -> Option<SocketAddr> {
    if let Ok(v) = std::env::var("REACTORCIDE_EGRESS_DNS") {
        return v.parse().ok();
    }
    let conf = std::fs::read_to_string("/etc/resolv.conf").ok()?;
    conf.lines()
        .filter_map(|l| l.trim().strip_prefix("nameserver"))
        .filter_map(|a| a.trim().parse::<std::net::IpAddr>().ok())
        .map(|ip| SocketAddr::new(ip, 53))
        .next()
}

/// Sends a query to the resolver: UDP first, TCP when the answer is
/// truncated.
pub async fn forward(server: SocketAddr, q: &[u8]) -> std::io::Result<Vec<u8>> {
    let sock = UdpSocket::bind(if server.is_ipv4() { "0.0.0.0:0" } else { "[::]:0" }).await?;
    sock.connect(server).await?;
    sock.send(q).await?;
    let mut buf = vec![0u8; 4096];
    let n = tokio::time::timeout(Duration::from_secs(5), sock.recv(&mut buf))
        .await
        .map_err(|_| std::io::Error::new(std::io::ErrorKind::TimedOut, "DNS timeout"))??;
    buf.truncate(n);
    if buf.len() < 3 || buf[2] & 0x02 == 0 {
        return Ok(buf);
    }
    let mut s = TcpStream::connect(server).await?;
    s.write_all(&(q.len() as u16).to_be_bytes()).await?;
    s.write_all(q).await?;
    read_msg(&mut s).await?.ok_or_else(|| std::io::Error::new(std::io::ErrorKind::UnexpectedEof, "no DNS answer"))
}

async fn read_msg<S: AsyncRead + Unpin>(s: &mut S) -> std::io::Result<Option<Vec<u8>>> {
    let mut len = [0u8; 2];
    match s.read_exact(&mut len).await {
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(e) => return Err(e),
    }
    let mut m = vec![0u8; u16::from_be_bytes(len) as usize];
    s.read_exact(&mut m).await?;
    Ok(Some(m))
}

/// Serves DNS over TCP on one tunnel of an environment.
pub async fn serve<S: AsyncRead + AsyncWrite + Unpin>(p: Arc<Proxy>, peer: Peer, mut s: S) {
    let server = resolver();
    loop {
        let q = match tokio::time::timeout(Duration::from_secs(60), read_msg(&mut s)).await {
            Ok(Ok(Some(q))) => q,
            _ => return,
        };
        let answer = match question(&q) {
            Some((name, qtype)) if p.dns_permitted(&peer, base_name(&name), qtype).await => match server {
                Some(srv) => match forward(srv, &q).await {
                    Ok(a) => a,
                    Err(e) => {
                        tracing::warn!(name = %name, error = %e, "DNS forward");
                        refused(&q)
                    }
                },
                None => refused(&q),
            },
            _ => refused(&q),
        };
        if s.write_all(&(answer.len() as u16).to_be_bytes()).await.is_err() || s.write_all(&answer).await.is_err() {
            return;
        }
    }
}

impl Proxy {
    /// True when the job can ask for records of `base` (a name without
    /// leading "_" labels). The record of a refused name has the
    /// destination "base:53".
    pub async fn dns_permitted(&self, peer: &Peer, base: &str, qtype: u16) -> bool {
        if base.is_empty() {
            return false;
        }
        if self.policy.names(&peer.job, base) || peer.state.as_ref().is_some_and(|s| s.names(base)) {
            return true;
        }
        let t = Target { scheme: "dns", host: base.to_string(), port: 53 };
        let ok = self.access(peer, &t, "DNS", &format!("dns://{base}?type={qtype}")).await.is_ok();
        self.record(peer, &t, if ok { "sent" } else { "blocked" }, vec![format!("DNS query type {qtype}")]);
        ok
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn query(name: &str, t: u16) -> Vec<u8> {
        let mut q = vec![0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        for l in name.split('.') {
            q.push(l.len() as u8);
            q.extend_from_slice(l.as_bytes());
        }
        q.push(0);
        q.extend_from_slice(&t.to_be_bytes());
        q.extend_from_slice(&1u16.to_be_bytes());
        q
    }

    #[test]
    fn questions() {
        let q = query("_linkkeys.Example.org", 16);
        assert_eq!(question(&q), Some(("_linkkeys.example.org".into(), 16)));
        assert_eq!(base_name("_linkkeys.example.org"), "example.org");
        assert_eq!(base_name("_x._tcp.example.org."), "example.org");
        assert_eq!(base_name("example.org"), "example.org");
        assert_eq!(base_name("_only"), "_only");
        let r = refused(&q);
        assert_eq!(r[0..2], q[0..2]);
        assert_eq!(r[3] & 0x0f, 5);
        assert!(r[2] & 0x80 != 0);
        assert_eq!(r.len(), q.len());
        assert_eq!(question(&[0; 5]), None);
    }
}
