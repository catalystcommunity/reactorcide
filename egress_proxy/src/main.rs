use std::io::Write as _;
use std::os::unix::fs::OpenOptionsExt as _;
use std::path::Path;
use std::sync::Arc;

use reactorcide_egress::ask::Ask;
use reactorcide_egress::ca::{self, Ca};
use reactorcide_egress::config::{Settings, flag};
use reactorcide_egress::flow::Proxy;
use reactorcide_egress::jobs::Jobs;
use reactorcide_egress::plugins::Plugins;
use reactorcide_egress::policy::Policy;
use reactorcide_egress::records::Records;
use reactorcide_egress::scan::Secrets;
use reactorcide_egress::server::{Server, TlsListener, router};
use reactorcide_egress::{net, proxy};

const USAGE: &str = "usage:
  reactorcide-egress [serve] [flags]          run the proxy (see README.md for the flags)
  reactorcide-egress ca --out DIR [--name N]  make a CA: DIR/ca.crt and DIR/ca.key (mode 600)
  reactorcide-egress key --out FILE           make a job key (HMAC key of job credentials, mode 600)
  reactorcide-egress control-token --out FILE make a control API token (mode 600)
  reactorcide-egress ssh-key --out FILE       make the SSH host key (mode 600); prints the public key
  reactorcide-egress credential --job-key FILE --job JOB --out FILE
                                              write the proxy password of JOB (mode 600)";

/// Writes a private file. It does not replace a file that is there.
fn write_private(path: &Path, data: &[u8]) -> anyhow::Result<()> {
    let mut f = std::fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(path).map_err(|e| anyhow::anyhow!("{}: {e}", path.display()))?;
    f.write_all(data)?;
    Ok(())
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .json()
        .with_env_filter(tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()))
        .init();
    let args: Vec<String> = std::env::args().skip(1).collect();
    let need = |n: &str| flag(&args, n, "").ok_or_else(|| anyhow::anyhow!("--{n} is required\n{USAGE}"));
    match args.first().map(String::as_str) {
        Some("ca") => {
            let dir = Path::new(&need("out")?).to_path_buf();
            let (c, k) = ca::generate(&flag(&args, "name", "").unwrap_or_else(|| "Reactorcide egress CA".into()))?;
            write_private(&dir.join("ca.key"), k.as_bytes())?;
            std::fs::write(dir.join("ca.crt"), c)?;
            println!("wrote {}/ca.crt and {}/ca.key", dir.display(), dir.display());
            return Ok(());
        }
        Some("key" | "control-token") => {
            let key: [u8; 32] = rand::random();
            write_private(Path::new(&need("out")?), hex::encode(key).as_bytes())?;
            return Ok(());
        }
        Some("ssh-key") => {
            let seed: [u8; 32] = rand::random();
            let k = russh::keys::PrivateKey::from(russh::keys::ssh_key::private::Ed25519Keypair::from_seed(&seed));
            let pem = k.to_openssh(russh::keys::ssh_key::LineEnding::LF)?;
            write_private(Path::new(&need("out")?), pem.as_bytes())?;
            println!("{}", reactorcide_egress::ssh::public_line(&k));
            return Ok(());
        }
        Some("credential") => {
            let key = read_key(Path::new(&need("job-key")?))?;
            write_private(Path::new(&need("out")?), proxy::credential(&key, &need("job")?).as_bytes())?;
            return Ok(());
        }
        Some("-h" | "--help" | "help") => {
            println!("{USAGE}");
            return Ok(());
        }
        _ => {}
    }
    let args: Vec<String> = args.into_iter().skip_while(|a| a == "serve").collect();
    serve(Settings::from_args(&args)?).await
}

fn read_key(p: &Path) -> anyhow::Result<Vec<u8>> {
    let k = std::fs::read(p).map_err(|e| anyhow::anyhow!("{}: {e}", p.display()))?;
    let k = String::from_utf8_lossy(&k).trim().as_bytes().to_vec();
    anyhow::ensure!(k.len() >= 32, "{}: the key must have at least 32 bytes", p.display());
    Ok(k)
}

async fn serve(cfg: Settings) -> anyhow::Result<()> {
    let policy = match &cfg.policy {
        Some(p) => Policy::load(p)?,
        None => {
            tracing::info!("no global policy file; jobs get only their own destinations");
            Policy::default_valid()
        }
    };
    let token = read_key(&cfg.control_token)?;
    let upstream = net::client(net::roots(cfg.upstream_ca.as_deref())?)?;
    let ask = match (&cfg.ask_url, &cfg.ask_token) {
        (Some(u), Some(t)) => Some(Arc::new(Ask::new(u, t.clone(), net::client(net::roots(None)?)?))),
        _ => None,
    };
    let secrets = match &cfg.secrets_dir {
        Some(d) => Arc::new(Secrets::load_dir(d)?),
        None => Arc::new(Secrets::default()),
    };
    let plugins = Plugins::load(&policy.plugins, ask.clone())?;
    let records = Arc::new(Records::new(5000));
    let jobs = Arc::new(Jobs::default());
    tracing::info!(destinations = policy.destinations.len(), secrets = secrets.len(), plugins = plugins.list.len(), "policy loaded");
    if cfg.allow_unregistered {
        tracing::warn!("REACTORCIDE_EGRESS_ALLOW_UNREGISTERED is on: a valid credential of a job that is not registered gets the global policy");
    }
    let p = Arc::new(Proxy {
        policy,
        secrets,
        plugins,
        ask,
        records: records.clone(),
        jobs: jobs.clone(),
        ca: Ca::load(&cfg.ca_cert, &cfg.ca_key)?,
        upstream,
        job_key: read_key(&cfg.job_key)?,
        allow_unregistered: cfg.allow_unregistered,
        ssh_key: match &cfg.ssh_host_key {
            Some(p) => {
                let k = russh::keys::PrivateKey::from_openssh(std::fs::read(p).map_err(|e| anyhow::anyhow!("{}: {e}", p.display()))?)?;
                tracing::info!(key = %reactorcide_egress::ssh::public_line(&k), "SSH proxy on");
                Some(k)
            }
            None => None,
        },
    });
    let api = tokio::net::TcpListener::bind(&cfg.listen).await?;
    let pl = tokio::net::TcpListener::bind(&cfg.proxy_listen).await?;
    tracing::info!(listen = %cfg.listen, proxy = %cfg.proxy_listen, "egress proxy started");
    let server = Arc::new(Server { records, jobs, token });
    match &cfg.control_tls_name {
        Some(name) => {
            // TLS with a certificate for the name from the proxy CA.
            let tls = TlsListener::new(api, p.ca.server_config(name)?)?;
            tracing::info!(name = %name, "control API uses TLS");
            tokio::select! {
                r = axum::serve(tls, router(server)).with_graceful_shutdown(shutdown()) => r?,
                r = proxy::serve(p, pl) => r?,
            }
        }
        None => {
            tracing::warn!("the control API is plain HTTP; restrict its port to the workers (NetworkPolicy) or set REACTORCIDE_EGRESS_CONTROL_TLS_NAME");
            tokio::select! {
                r = axum::serve(api, router(server)).with_graceful_shutdown(shutdown()) => r?,
                r = proxy::serve(p, pl) => r?,
            }
        }
    }
    tracing::info!("egress proxy stopped");
    Ok(())
}

/// PID 1 in a container gets no default SIGTERM action, so handle it.
async fn shutdown() {
    let mut term = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).expect("SIGTERM handler");
    tokio::select! {
        _ = term.recv() => {}
        _ = tokio::signal::ctrl_c() => {}
    }
}
