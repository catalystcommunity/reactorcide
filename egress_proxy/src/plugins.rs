//! The WASM plugin host (wit/egress.wit). Each flow gets a new
//! instance in a new store, with fuel, memory, and time limits. A plugin
//! gets an empty WASI context: no files, no network, no environment, and
//! no output. A plugin that fails or goes over a limit stops the flow.

use std::sync::Arc;
use std::time::Duration;

use sha2::{Digest, Sha256};
use wasmtime::component::{Component, HasSelf, Linker, ResourceTable};
use wasmtime::{Config, Engine, Store, StoreLimits, StoreLimitsBuilder};
use wasmtime_wasi::{WasiCtx, WasiCtxView, WasiView};

use crate::ask::Ask;
use crate::policy::{PluginGrant, host_match};

wasmtime::component::bindgen!({
    path: "wit/egress.wit",
    world: "filter",
    imports: { default: async },
    exports: { default: async },
});

pub use reactorcide::egress::types::{Direction, Header, SecretUse};

struct State {
    wasi: WasiCtx,
    table: ResourceTable,
    limits: StoreLimits,
    grant: Arc<PluginGrant>,
    ask: Option<Arc<Ask>>,
    flow: Flow,
}

impl WasiView for State {
    fn ctx(&mut self) -> WasiCtxView<'_> {
        WasiCtxView { ctx: &mut self.wasi, table: &mut self.table }
    }
}

impl reactorcide::egress::types::Host for State {}

impl reactorcide::egress::host::Host for State {
    async fn ask(&mut self, op: String, request: Vec<u8>) -> Result<Vec<u8>, String> {
        if !self.grant.ask_ops.contains(&op) {
            return Err(format!("operation {op:?} is not in the grant of plugin {}", self.grant.name));
        }
        let Some(m) = self.ask.clone() else { return Err("the ask service is not configured".into()) };
        m.ask(&op, &self.grant.name, &self.flow, &request).await
    }

    async fn log(&mut self, message: String) {
        let m: String = message.chars().take(2000).collect();
        tracing::info!(plugin = %self.grant.name, flow = %self.flow.id, message = %m, "plugin log");
    }
}

pub struct Plugin {
    pub grant: Arc<PluginGrant>,
    pre: FilterPre<State>,
}

pub struct Plugins {
    engine: Engine,
    pub list: Vec<Plugin>,
    ask: Option<Arc<Ask>>,
}

impl Plugins {
    /// Loads each plugin in the order of the grants. A file whose SHA-256
    /// is not the one in the grant stops the start.
    pub fn load(grants: &[PluginGrant], ask: Option<Arc<Ask>>) -> anyhow::Result<Self> {
        let mut cfg = Config::new();
        cfg.consume_fuel(true);
        cfg.wasm_component_model(true);
        let engine = Engine::new(&cfg)?;
        let mut linker: Linker<State> = Linker::new(&engine);
        wasmtime_wasi::p2::add_to_linker_async(&mut linker)?;
        Filter::add_to_linker::<_, HasSelf<_>>(&mut linker, |s| s)?;
        let mut list = Vec::new();
        for g in grants {
            let bytes = std::fs::read(&g.file).map_err(|e| anyhow::anyhow!("plugin {}: {}: {e}", g.name, g.file))?;
            let got = hex::encode(Sha256::digest(&bytes));
            if !got.eq_ignore_ascii_case(&g.sha256) {
                anyhow::bail!("plugin {}: SHA-256 is {got}, the grant says {}", g.name, g.sha256);
            }
            let component = Component::new(&engine, &bytes).map_err(|e| anyhow::anyhow!("plugin {}: {e}", g.name))?;
            let pre = FilterPre::new(linker.instantiate_pre(&component).map_err(|e| anyhow::anyhow!("plugin {}: {e}", g.name))?)?;
            tracing::info!(plugin = %g.name, sha256 = %got, stages = ?g.stages, "plugin loaded");
            list.push(Plugin { grant: Arc::new(g.clone()), pre });
        }
        Ok(Plugins { engine, list, ask })
    }

    pub fn empty() -> Self {
        Plugins { engine: Engine::default(), list: Vec::new(), ask: None }
    }

    /// The plugins that see this flow, in order.
    pub fn for_flow<'a>(&'a self, f: &Flow) -> impl Iterator<Item = &'a Plugin> + 'a {
        let stage = match f.direction {
            Direction::Out => "out",
            Direction::In => "in",
        };
        let host = f.destination.rsplit_once(':').map_or(f.destination.clone(), |(h, _)| h.to_string());
        self.list.iter().filter(move |p| {
            p.grant.stages.iter().any(|s| s == stage) && (p.grant.hosts.is_empty() || p.grant.hosts.iter().any(|h| host_match(h, &host)))
        })
    }

    /// Runs one plugin on one flow. An error means: stop the flow.
    pub async fn run(&self, p: &Plugin, flow: &Flow) -> anyhow::Result<Vec<Action>> {
        let g = &p.grant;
        let state = State {
            wasi: WasiCtx::builder().allow_ip_name_lookup(false).allow_tcp(false).allow_udp(false).build(),
            table: ResourceTable::new(),
            limits: StoreLimitsBuilder::new().memory_size(g.memory_mb << 20).instances(4).trap_on_grow_failure(true).build(),
            grant: g.clone(),
            ask: self.ask.clone(),
            flow: flow.clone(),
        };
        let mut store = Store::new(&self.engine, state);
        store.limiter(|s| &mut s.limits);
        store.set_fuel(g.fuel)?;
        // Yield to the runtime now and then, so the time limit works.
        store.fuel_async_yield_interval(Some(10_000_000))?;
        let work = async {
            let f = p.pre.instantiate_async(&mut store).await?;
            f.call_check(&mut store, flow).await
        };
        match tokio::time::timeout(Duration::from_millis(g.timeout_ms), work).await {
            Ok(Ok(actions)) => Ok(actions),
            Ok(Err(e)) => Err(anyhow::anyhow!("plugin {} failed: {e}", g.name)),
            Err(_) => Err(anyhow::anyhow!("plugin {} took more than {} ms", g.name, g.timeout_ms)),
        }
    }
}
