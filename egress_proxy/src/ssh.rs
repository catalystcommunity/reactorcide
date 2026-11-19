//! The SSH proxy. An environment that connects to an `ssh_hosts`
//! destination talks SSH to the proxy, not to the host: the proxy ends the
//! session with its own host key (environments trust it in known_hosts),
//! checks each command, and runs the command on the host in a session of
//! its own, with the key in the secret of the host. The environment never
//! has the key.
//!
//! Only `exec` requests run (for example `git-upload-pack 'org/repo.git'`).
//! A shell, a terminal, a subsystem, and port forwarding are refused. Each
//! command is a flow (method "SSH", URL `ssh://host/<command>`) that goes
//! through the secret scan and the plugins, so a plugin can permit or
//! block it. The bytes of a command are not inspected.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use russh::keys::{PrivateKey, PrivateKeyWithHashAlg, PublicKey};
use russh::server::{Auth, ChannelOpenHandle, Msg, Session, VerifiedPublicKey};
use russh::{Channel, ChannelId, ChannelMsg, MethodKind, MethodSet};
use tokio::io::{AsyncRead, AsyncWrite};

use crate::records::FlowRecord;
use crate::flow::{Peer, Proxy, Target, new_id};
use crate::plugins::{Direction, Flow};
use crate::policy::SshHost;

/// The OpenSSH public key line of a private key.
pub fn public_line(k: &PrivateKey) -> String {
    k.public_key().to_openssh().unwrap_or_default()
}

/// Serves one SSH connection of an environment to `t`.
pub async fn serve<S>(p: Arc<Proxy>, peer: Peer, t: Target, io: S)
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    let (Some(key), Some(host)) = (p.ssh_key.clone(), p.policy.ssh_host(&t.host, t.port).cloned()) else {
        p.record(&peer, &t, "blocked", vec!["SSH: no ssh_hosts entry or no SSH host key".into()]);
        return;
    };
    let cfg = Arc::new(russh::server::Config {
        keys: vec![key],
        methods: MethodSet::from(&[MethodKind::None, MethodKind::PublicKey][..]),
        auth_rejection_time: Duration::from_millis(200),
        auth_rejection_time_initial: Some(Duration::ZERO),
        inactivity_timeout: Some(Duration::from_secs(3600)),
        ..Default::default()
    });
    let h = Handler { p: p.clone(), peer: peer.clone(), t: t.clone(), host, chans: HashMap::new() };
    match russh::server::run_stream(cfg, io, h).await {
        Ok(s) => {
            if let Err(e) = s.await {
                tracing::debug!(error = %e, "ssh session");
            }
        }
        Err(e) => p.record(&peer, &t, "failed", vec![format!("SSH from the client: {e}")]),
    }
}

struct Handler {
    p: Arc<Proxy>,
    peer: Peer,
    t: Target,
    host: SshHost,
    chans: HashMap<ChannelId, Channel<Msg>>,
}

impl Handler {
    fn refuse(&self, what: &str, id: ChannelId, session: &mut Session) -> Result<(), russh::Error> {
        self.p.record(&self.peer, &self.t, "blocked", vec![format!("SSH {what} is not permitted; only commands (exec) run")]);
        session.channel_failure(id)
    }

    /// Refuses a command: the client gets the reason on stderr and exit
    /// status 1.
    fn fail(&self, id: ChannelId, session: &mut Session, reason: &str) -> Result<(), russh::Error> {
        session.channel_success(id)?;
        session.extended_data(id, 1, format!("reactorcide egress: {reason}\n").into_bytes())?;
        session.exit_status_request(id, 1)?;
        session.eof(id)?;
        session.close(id)
    }
}

impl russh::server::Handler for Handler {
    type Error = russh::Error;

    async fn auth_none(&mut self, _user: &str) -> Result<Auth, Self::Error> {
        // The environment is known from its proxy credential.
        Ok(Auth::Accept)
    }

    async fn auth_publickey(&mut self, _user: &str, _key: VerifiedPublicKey<'_>) -> Result<Auth, Self::Error> {
        Ok(Auth::Accept)
    }

    async fn channel_open_session(&mut self, channel: Channel<Msg>, reply: ChannelOpenHandle, _session: &mut Session) -> Result<(), Self::Error> {
        self.chans.insert(channel.id(), channel);
        reply.accept().await;
        Ok(())
    }

    async fn pty_request(
        &mut self,
        id: ChannelId,
        _term: &str,
        _cols: u32,
        _rows: u32,
        _pw: u32,
        _ph: u32,
        _modes: &[(russh::Pty, u32)],
        session: &mut Session,
    ) -> Result<(), Self::Error> {
        self.refuse("terminal (pty)", id, session)
    }

    async fn shell_request(&mut self, id: ChannelId, session: &mut Session) -> Result<(), Self::Error> {
        self.refuse("shell", id, session)
    }

    async fn subsystem_request(&mut self, id: ChannelId, name: &str, session: &mut Session) -> Result<(), Self::Error> {
        self.refuse(&format!("subsystem {name}"), id, session)
    }

    async fn env_request(&mut self, id: ChannelId, _name: &str, _value: &str, session: &mut Session) -> Result<(), Self::Error> {
        session.channel_failure(id)
    }

    async fn exec_request(&mut self, id: ChannelId, data: &[u8], session: &mut Session) -> Result<(), Self::Error> {
        let command = String::from_utf8_lossy(data).into_owned();
        let Some(chan) = self.chans.remove(&id) else { return session.channel_failure(id) };
        let mut rec = FlowRecord {
            id: new_id("flw_"),
            job_id: self.peer.job.clone(),
            identity: self.peer.identity.clone(),
            destination: self.t.destination(),
            method: Some("SSH".into()),
            url: Some(format!("ssh://{}/{command}", self.t.host)),
            status: None,
            outcome: "sent".into(),
            reasons: vec![],
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: crate::now_ms(),
            duration_ms: 0,
        };
        if let Err(s) = self.p.check_command(&mut rec, &self.peer, &self.t, &command).await {
            rec.outcome = s.outcome.into();
            rec.reasons.push(s.reason.clone());
            self.p.records.add(rec);
            return self.fail(id, session, &s.reason);
        }
        let (up, up_session) = match upstream(&self.p, &self.host, &self.t, &command).await {
            Ok(c) => c,
            Err(e) => {
                rec.outcome = "failed".into();
                rec.reasons.push(e.clone());
                self.p.records.add(rec);
                return self.fail(id, session, &e);
            }
        };
        rec.secrets_used.push(self.host.secret.clone());
        rec.reasons.push("command bytes are not inspected".into());
        self.p.records.add(rec);
        session.channel_success(id)?;
        let handle = session.handle();
        let (mut down_r, _down_w) = chan.split();
        let (mut up_r, up_w) = up.split();
        // The environment to the host.
        tokio::spawn(async move {
            while let Some(m) = down_r.wait().await {
                match m {
                    ChannelMsg::Data { data } => {
                        if up_w.data_bytes(data).await.is_err() {
                            break;
                        }
                    }
                    ChannelMsg::Eof => {
                        let _ = up_w.eof().await;
                    }
                    ChannelMsg::Close => break,
                    _ => {}
                }
            }
            let _ = up_w.close().await;
        });
        // The host to the environment. The upstream session ends with it.
        tokio::spawn(async move {
            let _session = up_session;
            while let Some(m) = up_r.wait().await {
                let ok = match m {
                    ChannelMsg::Data { data } => handle.data(id, data).await.is_ok(),
                    ChannelMsg::ExtendedData { data, ext } => handle.extended_data(id, ext, data).await.is_ok(),
                    ChannelMsg::ExitStatus { exit_status } => handle.exit_status_request(id, exit_status).await.is_ok(),
                    ChannelMsg::Eof => handle.eof(id).await.is_ok(),
                    ChannelMsg::Close => break,
                    _ => true,
                };
                if !ok {
                    break;
                }
            }
            let _ = handle.close(id).await;
        });
        Ok(())
    }
}

/// Checks the host key of the real host against `host_keys`.
struct UpHandler {
    keys: Vec<PublicKey>,
}

impl russh::client::Handler for UpHandler {
    type Error = russh::Error;

    async fn check_server_key(&mut self, key: &russh::keys::PublicKeyOrCertificate) -> Result<bool, Self::Error> {
        Ok(match key {
            russh::keys::PublicKeyOrCertificate::PublicKey { key, .. } => self.keys.iter().any(|k| k.key_data() == key.key_data()),
            russh::keys::PublicKeyOrCertificate::Certificate(_) => false,
        })
    }
}

/// Opens a session on the real host and starts the command.
async fn upstream(p: &Proxy, host: &SshHost, t: &Target, command: &str) -> Result<(Channel<russh::client::Msg>, russh::client::Handle<UpHandler>), String> {
    let keys: Vec<PublicKey> = host.host_keys.iter().filter_map(|k| PublicKey::from_openssh(k).ok()).collect();
    if keys.is_empty() {
        return Err(format!("no valid host key for {} in the policy", t.host));
    }
    let secret = p.secrets.get(&host.secret).ok_or_else(|| format!("no secret {} for {}", host.secret, t.host))?;
    let key = PrivateKey::from_openssh(secret).map_err(|e| format!("secret {} is not an OpenSSH private key: {e}", host.secret))?;
    let cfg = Arc::new(russh::client::Config { inactivity_timeout: Some(Duration::from_secs(3600)), ..Default::default() });
    let stream = tokio::time::timeout(Duration::from_secs(20), tokio::net::TcpStream::connect((t.host.as_str(), t.port)))
        .await
        .map_err(|_| format!("connect to {}: timeout", t.destination()))?
        .map_err(|e| format!("connect to {}: {e}", t.destination()))?;
    let mut s = russh::client::connect_stream(cfg, stream, UpHandler { keys })
        .await
        .map_err(|e| format!("SSH to {}: {e} (is the host key in the policy?)", t.destination()))?;
    let hash = s.best_supported_rsa_hash().await.map_err(|e| e.to_string())?.flatten();
    let auth = s
        .authenticate_publickey(host.user.clone(), PrivateKeyWithHashAlg::new(Arc::new(key), hash))
        .await
        .map_err(|e| format!("SSH authentication at {}: {e}", t.destination()))?;
    if !auth.success() {
        return Err(format!("{} refused the key in secret {}", t.destination(), host.secret));
    }
    let ch = s.channel_open_session().await.map_err(|e| format!("SSH session at {}: {e}", t.destination()))?;
    ch.exec(true, command.as_bytes()).await.map_err(|e| format!("SSH exec at {}: {e}", t.destination()))?;
    Ok((ch, s))
}

impl Proxy {
    /// Checks an SSH command like a request: the secret scan, the decode
    /// depth, and the plugins.
    pub async fn check_command(&self, rec: &mut FlowRecord, peer: &Peer, t: &Target, command: &str) -> Result<(), crate::flow::Stop> {
        let mut f = Flow {
            id: rec.id.clone(),
            environment: peer.job.clone(),
            owner: peer.identity.clone(),
            direction: Direction::Out,
            destination: t.destination(),
            method: "SSH".into(),
            url: format!("ssh://{}/{command}", t.host),
            status: 0,
            headers: vec![],
            body: vec![],
        };
        self.inspect_out(rec, &mut f, peer.state.as_deref(), &t.host).await
    }
}
