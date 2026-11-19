//! Protocol detection: the first bytes of a CONNECT tunnel say what the
//! client speaks. The proxy ends TLS and HTTP, and stops the others unless
//! the destination is permitted at layer 4.

use std::io;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;

use tokio::io::{AsyncRead, AsyncReadExt as _, AsyncWrite, ReadBuf};

/// How long the proxy waits for the first bytes of the client. A protocol
/// where the server speaks first sends nothing.
pub const FIRST_BYTES: Duration = Duration::from_secs(5);

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Protocol {
    Tls,
    Http,
    Ssh,
    /// Something else, with a short description for the record.
    Other(String),
}

impl Protocol {
    pub fn name(&self) -> String {
        match self {
            Protocol::Tls => "TLS".into(),
            Protocol::Http => "HTTP".into(),
            Protocol::Ssh => "SSH".into(),
            Protocol::Other(d) => d.clone(),
        }
    }
}

const METHODS: &[&[u8]] = &[b"GET ", b"POST", b"PUT ", b"HEAD", b"DELE", b"OPTI", b"PATC", b"TRAC"];

/// The protocol of the first bytes of a client.
pub fn classify(b: &[u8]) -> Protocol {
    if b.is_empty() {
        return Protocol::Other("no data from the client (the server speaks first)".into());
    }
    if b.len() >= 2 && b[0] == 0x16 && b[1] == 0x03 {
        return Protocol::Tls;
    }
    if b.len() >= 4 && METHODS.iter().any(|m| b.starts_with(m)) {
        return Protocol::Http;
    }
    if b.starts_with(b"SSH-") {
        return Protocol::Ssh;
    }
    let shown: String = b.iter().take(8).map(|c| format!("{c:02x}")).collect();
    Protocol::Other(format!("unknown protocol (first bytes {shown})"))
}

/// Reads the first bytes of `io` (up to 4, or what comes in FIRST_BYTES).
pub async fn peek<S: AsyncRead + Unpin>(io: &mut S) -> io::Result<Vec<u8>> {
    let mut buf = vec![0u8; 4];
    let mut n = 0;
    let read = async {
        while n < 4 {
            let m = io.read(&mut buf[n..]).await?;
            if m == 0 {
                break;
            }
            n += m;
            // A short TLS or SSH start is enough.
            if n >= 2 && buf[0] == 0x16 {
                break;
            }
        }
        Ok::<_, io::Error>(())
    };
    match tokio::time::timeout(FIRST_BYTES, read).await {
        Ok(r) => r?,
        Err(_) => {}
    }
    buf.truncate(n);
    Ok(buf)
}

/// A stream that gives `head` before the bytes of `inner`.
pub struct Prefixed<S> {
    head: Vec<u8>,
    pos: usize,
    inner: S,
}

impl<S> Prefixed<S> {
    pub fn new(head: Vec<u8>, inner: S) -> Self {
        Prefixed { head, pos: 0, inner }
    }
}

impl<S: AsyncRead + Unpin> AsyncRead for Prefixed<S> {
    fn poll_read(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buf: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        if self.pos < self.head.len() {
            let n = (self.head.len() - self.pos).min(buf.remaining());
            let start = self.pos;
            buf.put_slice(&self.head[start..start + n]);
            self.pos += n;
            return Poll::Ready(Ok(()));
        }
        Pin::new(&mut self.inner).poll_read(cx, buf)
    }
}

impl<S: AsyncWrite + Unpin> AsyncWrite for Prefixed<S> {
    fn poll_write(mut self: Pin<&mut Self>, cx: &mut Context<'_>, b: &[u8]) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.inner).poll_write(cx, b)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_shutdown(cx)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncWriteExt as _;

    #[test]
    fn protocols() {
        assert_eq!(classify(&[0x16, 0x03, 0x01, 0x02]), Protocol::Tls);
        assert_eq!(classify(b"GET "), Protocol::Http);
        assert_eq!(classify(b"POST"), Protocol::Http);
        assert_eq!(classify(b"SSH-"), Protocol::Ssh);
        assert!(matches!(classify(b""), Protocol::Other(d) if d.contains("server speaks first")));
        assert!(matches!(classify(&[0, 1, 2, 3]), Protocol::Other(d) if d.contains("00010203")));
    }

    #[tokio::test]
    async fn peek_then_replay() {
        let (mut a, b) = tokio::io::duplex(64);
        a.write_all(b"SSH-2.0-x\r\n").await.unwrap();
        drop(a);
        let mut b = b;
        let head = peek(&mut b).await.unwrap();
        assert_eq!(classify(&head), Protocol::Ssh);
        let mut all = Vec::new();
        Prefixed::new(head, b).read_to_end(&mut all).await.unwrap();
        assert_eq!(all, b"SSH-2.0-x\r\n");
    }
}
