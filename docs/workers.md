# Workers, Characteristics & Queues

This is the operator-facing guide to Reactorcide's coordinator-mediated worker model: how
jobs get routed to workers, how workers enroll and authenticate, what resources a job can
request, and how to manage all of it from the admin UI. This doc describes the
shipped behavior.

## Topology

All job execution flows through the coordinator — a worker never talks to Corndogs,
Postgres, or object storage directly. A worker authenticates to the coordinator over
CSIL-RPC (`POST /csil/v1/rpc`, the same dispatcher the management UI uses, mounted by
`coordinator_api/internal/workerapi`'s `ReactorcideWorker` service) and pulls work through
it:

```
worker ──CSIL-RPC(worker session)──▶ coordinator ──▶ Corndogs (UUID queues), Postgres, object store
worker runs and monitors job execution, then sends logs to the coordinator
```

This lets the coordinator authenticate every worker, keeps Corndogs off worker-reachable
networks entirely, and scales horizontally: any coordinator pod can serve any worker from
shared DB state, so a dead coordinator pod just means the worker reconnects and loses
nothing (every piece of state — leases, sessions, job status — lives in Postgres). A worker
is a small binary (`reactorcide worker`) that loops: `Register` → `RequestJob` → run the job
→ `AppendLogs` → `ReportResult` → `Heartbeat`, sourcing all of its work from the coordinator
instead of polling a queue directly. The same protocol supports container and
native VM backends. The protocol does not depend on the execution backend.

## Characteristics and matching

A **characteristic** is a `key → value` pair. The key is always a string; the value is a
`string`, `int`, or `bool`. Job/queue characteristics are always scalar. Worker
characteristics may additionally be a **homogeneous list** (`[]string`, `[]int`, or
`[]bool` — never mixed types), which lets one worker satisfy several distinct queues for the
same key (for example a worker whose `gpu_model` custom characteristic is `["a100", "h100"]`
can serve queues asking for either value).

**Job characteristics** live under the job spec's `characteristics` key — a flat map, set
per-job (not per-workflow). `name` and other job fields are never characteristics, and
`resources` (below) is a completely separate concept that never participates in matching.

**The one default assumption:** if a job's characteristics omit `os`, the coordinator
injects `os: linux`. Nothing else is ever defaulted:

| Job submits | Coordinator resolves to |
|---|---|
| *(no `characteristics` block)* | `{os: linux}` |
| `{arch: amd64}` | `{os: linux, arch: amd64}` |
| `{os: windows}` | `{os: windows}` |
| `{os: debian}` | `{os: debian}` (no normalization — your literal string) |

Specificity never changes priority — a job with three characteristics doesn't preempt one
with zero.

**Worker characteristics** always include `os` and `arch`, plus any free-form `custom`
key/value pairs the operator configures:

- `os` defaults to `linux` if not set on the worker, otherwise it's auto-detected
  (`linux`/`macos`/`windows`); you may override it with any literal string (`fedora`,
  `windows11`) — no normalization, no capitalization fixups.
- `arch` is auto-detected (`amd64`/`arm64`); override with any string (`arm64v8`, ...).
- `custom` values may be lists (see above). The key `queue` is **not special** as a custom
  characteristic — it's matched like any other key.

**The satisfies predicate** (the only matching rule — filter operators like `ge()`/ranges are
future work): worker `W` satisfies queue `Q` when, for every `(k, v)` in `Q`'s
characteristics, `W` has that key and either `W[k] == v` or `W[k]` is a list containing `v`
(types must match: an `int` never equals a `string`). `W` may carry extra characteristics `Q`
doesn't name — that's fine, and is exactly how a GPU worker with `{os: linux, arch: amd64,
gpu: true}` ends up satisfying both the plain `{os: linux}` default queue *and* a `{os:
linux, gpu: true}` queue.

## Custom characteristics

A worker's `custom` characteristics come from two operator-facing input surfaces, both parsed
by the same rules:

- **`REACTORCIDE_WORKER_CUSTOM_<KEY>` env vars** — the characteristic key is
  `strings.ToLower(<KEY>)` (the part after the prefix). Since env var names are restricted to
  `[A-Z0-9_]`, a key containing `.` or `-` is not reachable this way; use `--custom` for those.
- **`--custom key=value`** (repeatable CLI flag).

**Type inference** (int → bool → string, in that order, decided by the value's **first**
comma-separated element only):

1. Split the value on commas, honoring backslash-escaping: `\,` is a literal comma inside an
   element and `\\` is a literal backslash; splitting itself happens only on *unescaped*
   commas. Each resulting element is then unescaped and trimmed of surrounding ASCII
   whitespace. One element → scalar; more than one → a homogeneous list.
2. If the first element parses as a base-10 integer, every element must — otherwise it's a
   loud error naming the offending element. One element → an int characteristic; several → an
   int list.
3. Otherwise, if the first element is a recognized YAML boolean token — **exactly** `true`,
   `True`, `TRUE`, `false`, `False`, `FALSE` (YAML 1.2 core schema; `yes`/`no`/`on`/`off` are
   **not** recognized and are treated as plain strings) — every element must be one too,
   again all-or-error. One element → a bool characteristic; several → a bool list.
4. Otherwise every element is a plain string (this always succeeds). One element → a string
   characteristic; several → a string list.

Because the type is keyed off the *first* element only, `zones=us,eu` is a string list but
`tiers=1,foo` is a **loud startup error** (the first element `1` is an integer, so `foo` must
be too) — reordering to `foo,1` would instead succeed as a string list. This asymmetry is
intentional, not a bug: pick the first element to match the type you want the whole list to
be.

`os` and `arch` are **reserved** — they're set via `--os`/`--arch` or
`REACTORCIDE_WORKER_OS`/`REACTORCIDE_WORKER_ARCH`, and using either as a custom key (via
either input surface) fails at startup. A key supplied by more than one source — or twice
within the same source — is likewise a loud startup error naming the key and both sources;
misconfiguration is never silently dropped or silently overwritten.

Examples:

```bash
export REACTORCIDE_WORKER_CUSTOM_GPU=true          # bool:  gpu = true
export REACTORCIDE_WORKER_CUSTOM_SLOTS=4           # int:   slots = 4
export REACTORCIDE_WORKER_CUSTOM_ZONES=us,eu       # string list: zones = [us, eu]
export REACTORCIDE_WORKER_CUSTOM_TIERS=1,2,3       # int list:    tiers = [1, 2, 3]
```

or equivalently via flags:

```bash
reactorcide worker --custom gpu=true --custom slots=4 --custom zones=us,eu --custom tiers=1,2,3 ...
```

## Queues

A queue is identified by a UUID — its Corndogs queue name is exactly that UUID, nothing
else. The coordinator's own DB row carries the queue's immutable **characteristics**
(canonicalized: sorted keys, type-tagged serialization, hashed), a mutable **display name**,
and metadata.

- **Find-or-create at submit.** When a job is submitted, the coordinator canonicalizes its
  (defaulted) characteristics, hashes them, and looks up a queue by that hash. If one exists,
  the job's Corndogs task goes there; if not, a new queue row is created (fresh UUID) and the
  task is submitted to it. Corndogs itself needs no pre-registration — queues are implicit
  there, created on first submit — the coordinator's `queues` table is the source of truth
  for matching.
- **The default queue** is seeded at coordinator startup with characteristics `{os: linux}`
  and a stable UUID. Every untagged, `os`-less job resolves here.
- **Characteristics are immutable** once a queue is created — a different characteristic set
  is always a different queue. There is no "edit characteristics" operation, only rename
  (display name).
- **Operators may pre-create queues** ahead of any job needing them (define characteristics +
  display name up front from the admin UI or CSIL), which is how you'd stand up a `{os:
  linux, gpu: true}` queue before the first GPU job is ever submitted.
- **Deleting a queue cancels its in-flight jobs.** Every non-terminal job routed to the queue
  is driven through the normal graceful-cancel path (the same one `cancel-job` uses) before
  the queue row is removed. The seeded default queue can never be deleted — refused outright,
  since untagged jobs would otherwise have nowhere to resolve to.
- **Matching at pull time.** A worker sends its full characteristics on every `RequestJob`
  call; the coordinator scans the (small, cacheable — queues are operator/coordinator-scale,
  not per-job) queue table for every queue the worker satisfies, collects their UUIDs, and
  calls Corndogs' `GetNextTaskGroup` across the whole group in one claim — so a worker with
  broad characteristics doesn't need a separate poll per queue, and priority/ordering is
  honored across the whole satisfied set.

## Resources

A job spec's `resources` key declares per-job compute limits, separate from characteristics
and never involved in queue routing:

- `cpu.request` / `cpu.limit` — Kubernetes-flavored quantity strings (`"1"`, `"2"`, `"1.5"`,
  `"500m"`).
- `memory.limit` — quantity string (`"4Gi"`, `"4GB"`, `"4G"`, `"512Mi"`). Memory is
  **limit-only** — there's no memory request, it's a pure ceiling.

Quantities are parsed by Reactorcide's own small grammar (`internal/resources`), not the
Kubernetes quantity parser — fewer dependencies, and the grammar also accepts `GB`/`MB`
decimal suffixes that Kubernetes itself rejects. Binary suffixes (`Ki`/`Mi`/`Gi`/`Ti`) are
1024-based; decimal suffixes (`K`/`M`/`G`/`T`, `KB`/`MB`/`GB`/`TB`) are 1000-based. CPU
quantities become millicores internally; memory quantities become bytes. The Docker/
containerd runners apply the parsed values directly (`--cpus`, `--memory`); the Kubernetes
runner builds `resource.Quantity` values from the same parsed numbers for the pod spec.

**Defaults when a job leaves a field unset:** `cpu.request = "1"`, `cpu.limit = "2"`,
`memory.limit = "4Gi"`. Applied at submit time, so every job row always carries an explicit
value even if the job spec never mentioned `resources` at all.

```yaml
# job spec excerpt
characteristics:
  gpu: true
resources:
  cpu:
    request: "2"
    limit: "4"
  memory:
    limit: "8GB"
```

## Worker enrollment

Auth reuses the coordinator's session model; enrollment reuses its credential-rotation shape.
**Pools** are an admin grouping — display and future permission scoping only. Pools do not
participate in job matching; matching is purely characteristic-based, independent of which
pool a worker belongs to.

- **Enrollment token**: durable, per-pool (or global), created by an admin, stored
  **hashed** (never recoverable after creation), and rotatable — a pool can have several
  active tokens at once so you can roll one out and retire the old one without a flag day.
  This is the only human-handled worker credential.
- **Worker session**: ephemeral, minted on `Register`, **process-lifetime only** (never
  persisted by the worker, never shown to a human), hash-only in the coordinator's
  `worker_sessions` table, refreshed by each `Heartbeat`. Rides the CSIL envelope's `auth`
  field on every subsequent call.
- **Worker identity across restarts**: a worker generates and persists its own
  `worker_key` locally (a UUID akin to a host GUID, not a secret) so the coordinator
  recognizes a reconnecting worker as the *same* worker row rather than creating a new one
  every restart. `Register` upserts the `workers` row by this key.

The enrollment flow end to end: an admin creates a pool and mints a token for it (shown
exactly once, at creation — the coordinator only ever stores it hashed) → the token value is
handed to the worker via its deployment's secret/env, never checked into a repo or job spec
→ the worker calls `Register(enrollment_token, worker_info)` and gets back a worker session
+ its assigned `worker_id` + a heartbeat interval → from then on the worker polls
`RequestJob` with its full characteristics, runs whatever it's leased, and reports back.

## Admin UI and CSIL ops

Every worker/pool/queue admin action requires `authz.Caps.ManageWorkers` — the same
org-admin-of-the-target's-org (or global-admin) tier as `ManageSecrets`/
`ManageGroupsRoles` (see `docs/ui-auth.md`'s permission matrix). There is no separate
"view-only" capability for this surface: if you can see it in the admin UI, you can manage
it. Because queues have no owning org today, every queue op (`create-queue`, `rename-queue`,
`delete-queue`, `list-queues`) requires a true **global** admin.

**Webapp pages** (`webapp/`, server-rendered, capability-gated, every action re-authorized by
the coordinator — the webapp itself never decides who's allowed to do what):

- `/app/workers` — pools (create/rename/delete), enrollment tokens (create — token shown
  once — and deactivate), and the worker list (status, last-seen, active lease count, plus
  quarantine/disable/re-activate and drain controls).
- `/app/workers/queues` — queue list (characteristics read-only, backlog count via Corndogs'
  per-queue task counts), create (pre-create a queue with its characteristics up front),
  rename (display name only), and delete (with confirmation, since it cancels in-flight
  jobs).

**CSIL ops** (`ReactorcideUi`, same `POST /csil/v1/rpc` dispatcher as everything else in the
management API): `list-queues`, `create-queue`, `rename-queue`, `delete-queue`,
`list-workers`, `set-worker-status`, `drain-worker`, `list-pools`, `create-pool`,
`update-pool`, `delete-pool`, `create-enrollment-token`, `list-enrollment-tokens`,
`deactivate-enrollment-token`. `drain-worker` is modeled as the same `quarantined` status
`set-worker-status` can also set directly (there's no separate fourth persisted worker
state) — `RequestJob` already refuses any further work to a non-`active` worker, and
`Heartbeat`'s response carries a `draining` flag so a worker still finishing existing leases
learns to shut itself down cleanly.

## Operator setup flow

1. **Create a pool and an enrollment token** — `/app/workers` in the webapp, or the
   `create-pool`/`create-enrollment-token` CSIL ops directly. Copy the token value out
   immediately; it is never shown again.
2. **Store the token as a secret**, never in a committed file — a Kubernetes `Secret`, a VM's
   `.env`, or wherever your deployment target's secret store lives. See "Deploying workers"
   below for the concrete mechanics per deployment shape.
3. **Deploy the worker(s)** pointed at your coordinator URL and that token/secret reference,
   with whatever `os`/`arch`/`custom` characteristics distinguish that worker pool (a GPU
   node pool might set `--custom gpu=true`).
4. **Pre-create any non-default queues** your jobs will target (`/app/workers/queues` or
   `create-queue`) — for example the `{os: linux, gpu: true}` queue a GPU job's
   `characteristics: {gpu: true}` will resolve to. This step is optional: submitting a job
   with novel characteristics before any matching queue exists still works (find-or-create
   makes the queue on the fly) — pre-creating is purely for operator visibility/control
   before the first job lands.
5. **Submit jobs** with whatever `characteristics`/`resources` they need; the coordinator
   handles queue resolution and worker matching from here on.

## Deploying workers

### Zero-touch by default

A fresh deploy — Helm or VM — enrolls its worker with **no manual
secret/pool/kubectl step**. Both deployment shapes generate an enrollment
token **once** at deploy time and keep it **stable across redeploys**, wire the
**coordinator** to seed its default worker pool from that same token
(`REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN` → `EnsureDefaultWorkerPool`,
which hashes and registers it), and wire the **worker** to enroll with it
(`REACTORCIDE_WORKER_ENROLLMENT_TOKEN`). Because both sides read the same
generated value, the worker registers against a pool the coordinator already
knows about the first time it comes up. The token value is never echoed,
logged, or committed.

You only fall back to the manual "create a pool + mint a token" flow when you
want an **operator override** (a specific admin-minted per-pool token instead
of the auto-generated one) — see each shape below.

### Operator flow (production, optional override)

1. Create a **worker pool** and an **enrollment token** for it, via the coordinator admin UI
   or CLI (see "Operator setup flow" above). The token is shown once at creation; the
   coordinator only ever stores it hashed.
2. Store that token value in a **Kubernetes Secret** (or, for a VM/Docker Compose
   deployment, in the target host's `.env`) — never in a committed values file, manifest, or
   job YAML.
3. Point the worker deployment at that secret. It never needs the token value to appear in
   `values.yaml`, `docker-compose.yml`, or any job spec — only a Secret/file *reference*.

### Helm

**Default (zero-touch):** deploy with nothing extra. The chart's
`templates/secret-worker-enrollment.yaml` generates a stable enrollment token
into a managed Secret (`<release>-worker-enrollment`, key `token`,
`randAlphaNum`), annotated `helm.sh/resource-policy: keep` so it survives
upgrades and uninstalls. Both the coordinator (app) deployment
(`REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN`) and the worker deployment
(`REACTORCIDE_WORKER_ENROLLMENT_TOKEN`) reference that same Secret via
`secretKeyRef`, so the coordinator seeds its default pool from exactly the
value the worker enrolls with. Stability is preserved across `helm upgrade` by
a `lookup` that reuses the already-applied Secret's value instead of
re-generating.

> Note: `helm template` / `--dry-run` render a *fresh* random value each time
> (there is no live cluster for `lookup` to read); the value that actually
> lands and stays stable is the one from a real `helm install`/`upgrade`.

**Operator override:** to enroll with your own admin-minted per-pool token,
create the Secret yourself and point `worker.enrollmentTokenSecret.name` at it.
The chart then does **not** create the managed Secret, and both deployments
read yours instead:

```bash
kubectl create secret generic reactorcide-worker-enrollment \
  --from-literal=token="$(cat /path/to/enrollment-token)" \
  -n reactorcide
```

```yaml
# values.yaml
worker:
  enrollmentTokenSecret:
    name: "reactorcide-worker-enrollment"   # set = override; empty = auto-generate
    key: "token"          # default
  # coordinatorUrl: ""     # optional override; defaults to this release's
  #                         in-cluster coordinator Service URL
  # os: ""                 # optional override; auto-detected otherwise
  # arch: ""                # optional override; auto-detected otherwise
  # custom: ["pool=gpu"]   # optional free-form characteristics
```

**Rotation:** to rotate the auto-generated token, delete the managed Secret and
re-run `helm upgrade` (a new value is generated and re-seeded); or switch to an
operator-override Secret and rotate it via the admin UI's token
create/deactivate flow (a pool can hold several active tokens at once, so you
can roll a new one out before retiring the old).

See `helm_chart/values.yaml` (`worker:` section) and
`helm_chart/DEPLOYMENT.md` ("Deploying Workers") for the full set of knobs,
including the optional persistent data directory for `worker_key`.

The chart has three worker data directory modes. The default `emptyDir` and
the generic ephemeral PVC mode survive a container restart in the same Pod.
They do not survive Pod deletion. The generic ephemeral PVC mode can use a
cloud network storage class. The retained PVC mode can survive Pod deletion,
but it is only safe for one worker replica.

The worker writes unsent log and metric batches under its data directory. It
keeps 12 batches for each lease by default. Set
`REACTORCIDE_WORKER_TELEMETRY_BUFFER_BATCHES` to change this limit. When the
buffer is full, the worker removes the oldest batch and reports a telemetry
gap. A restarted worker sends retained batches again. The coordinator accepts
them only while the lease is active.

The worker samples CPU and memory every two seconds. It samples storage every
10 seconds because storage collection can require an extra runtime request.
Use `REACTORCIDE_WORKER_METRICS_INTERVAL` and
`REACTORCIDE_WORKER_STORAGE_METRICS_INTERVAL` to change these intervals. Each
value must be from one second through one minute. The worker sends a batch at
least every 10 seconds. Use `REACTORCIDE_WORKER_TELEMETRY_SEND_INTERVAL` to
change the send interval.

### Docker Compose (dev bootstrap)

The local dev stack (`docker-compose.yml`) needs a running coordinator
before a worker can enroll, so the coordinator seeds a **default worker
pool + enrollment token** from `REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN`
if no pool exists yet — purely a local convenience, not something to rely on
in production. The dev compose file pairs the two sides with the same
obviously-fake value:

```yaml
coordinator-api:
  environment:
    REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN: ${REACTORCIDE_WORKER_ENROLLMENT_TOKEN:-dev-worker-enroll-token-change-me}

worker:
  environment:
    REACTORCIDE_COORDINATOR_URL: http://coordinator-api:8080
    REACTORCIDE_WORKER_ENROLLMENT_TOKEN: ${REACTORCIDE_WORKER_ENROLLMENT_TOKEN:-dev-worker-enroll-token-change-me}
```

Override `REACTORCIDE_WORKER_ENROLLMENT_TOKEN` in your shell/`.env` if you
want a non-default value; both services read the same variable so they stay
in sync. **Never reuse this pattern for a real deployment** — production
workers enroll with a real per-pool token created via the admin UI/CLI,
stored only in a Secret/file, and the coordinator's default-bootstrap token
should be left unset once real pools exist.

### VM deploy (`jobs/deploy-to-vm.yaml`)

**Default (zero-touch):** the deploy job generates a stable
`REACTORCIDE_WORKER_ENROLLMENT_TOKEN` into the target host's `.env` if one
isn't already present (`openssl rand -hex 32`), exactly the way it generates
`DB_PASSWORD`/`JWT_SECRET`. It is only generated when absent, so it's preserved
across redeploys. In `deployment/docker-compose.prod.yml` the **worker** reads
that `.env` var to enroll and the **coordinator** reads the same var as
`REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN` to seed its default pool — so both
sides match with no manual step. (`REACTORCIDE_COORDINATOR_URL` is hardcoded to
the in-compose coordinator service `http://coordinator-api:6080`.)

**Operator override:** set `REACTORCIDE_WORKER_ENROLLMENT_TOKEN` in the deploy
job's host environment to an admin-minted per-pool token. The job's
`environment:` picks it up as an *optional* env reference and writes it into the
VM's `.env`, replacing the generated value:

```yaml
environment:
  # optional override; empty (unset) resolves to empty and the generated
  # token stands — never a plaintext default here
  REACTORCIDE_WORKER_ENROLLMENT_TOKEN: "${env:REACTORCIDE_WORKER_ENROLLMENT_TOKEN}"
```

**Rotation:** overwrite `REACTORCIDE_WORKER_ENROLLMENT_TOKEN` in the VM's `.env`
(or pass a new override) and restart the stack; or use an admin-minted token and
rotate it through the admin UI.

### Kubernetes deploy (`jobs/deploy-to-k8s.yaml`)

**Default (zero-touch):** set neither variable and the Helm chart
auto-generates the managed enrollment Secret and the coordinator seeds its
default pool from it (see "Helm" above) — no `kubectl create secret` step.

**Operator override:** pass a **Secret name/key reference** (never the token
value) and the deploy threads it through to the chart's override path:

```yaml
environment:
  REACTORCIDE_WORKER_ENROLLMENT_TOKEN_SECRET: "${env:REACTORCIDE_WORKER_ENROLLMENT_TOKEN_SECRET}"
  REACTORCIDE_WORKER_ENROLLMENT_TOKEN_KEY: "${env:REACTORCIDE_WORKER_ENROLLMENT_TOKEN_KEY}"
```

`jobs/scripts/deploy_k8s.py` turns these into
`--set worker.enrollmentTokenSecret.name=... --set worker.enrollmentTokenSecret.key=...`
on the `helm upgrade` it runs, which suppresses the managed Secret and points
both deployments at yours. If `REACTORCIDE_WORKER_ENROLLMENT_TOKEN_SECRET` is
unset, the chart's zero-touch default applies. You create the override Secret
yourself (`kubectl create secret ...`); the deploy job never creates or reads a
token value.

## Worker environment reference

Everything the `reactorcide worker` binary reads, as of the coordinator-mediated
worker (`coordinator_api/cmd/worker.go`):

| Flag | Env var | Purpose |
| --- | --- | --- |
| `--coordinator-url` | `REACTORCIDE_COORDINATOR_URL` | required; base URL of the coordinator |
| `--enrollment-token-file` | — (file path) | preferred way to supply the enrollment token |
| — | `REACTORCIDE_WORKER_ENROLLMENT_TOKEN` | fallback if no `--enrollment-token-file` |
| `--data-dir` | `REACTORCIDE_WORKER_DATA_DIR` | directory persisting `worker_key` |
| `--worker-key-file` | — | overrides the default `<data-dir>/worker_key` path |
| `--hostname` | `REACTORCIDE_WORKER_HOSTNAME` | identifying hostname sent on Register |
| `--os` | `REACTORCIDE_WORKER_OS` | worker `os` characteristic (auto-detected otherwise) |
| `--arch` | `REACTORCIDE_WORKER_ARCH` | worker `arch` characteristic (auto-detected otherwise) |
| `--custom` (repeatable) | `REACTORCIDE_WORKER_CUSTOM_<KEY>` | free-form `key=value` characteristics, typed/list-inferred — see "Custom characteristics" above |
| `--worker-version` | `REACTORCIDE_WORKER_VERSION` | optional build/version string |
| `--container-runtime` / `-r` | `REACTORCIDE_CONTAINER_RUNTIME` | `docker`, `containerd`, `kubernetes`, or `auto` |
| `--concurrency` / `-c` | `REACTORCIDE_WORKER_CONCURRENCY` | concurrent leases per worker process |
| `--metrics-interval` | `REACTORCIDE_WORKER_METRICS_INTERVAL` | CPU and memory sample interval from `1s` through `1m` |
| `--storage-metrics-interval` | `REACTORCIDE_WORKER_STORAGE_METRICS_INTERVAL` | storage sample interval from `1s` through `1m` |
| `--telemetry-send-interval` | `REACTORCIDE_WORKER_TELEMETRY_SEND_INTERVAL` | maximum interval between telemetry batches |
| `--telemetry-buffer-batches` | `REACTORCIDE_WORKER_TELEMETRY_BUFFER_BATCHES` | retained unsent batches for each lease |

The worker process also still reads a few runner/job-execution env vars that
are unrelated to the coordinator protocol:
`REACTORCIDE_JOB_API_URL` / `REACTORCIDE_API_TOKEN` (let job containers
submit triggers), `REACTORCIDE_K8S_JOB_NAMESPACE` /
`REACTORCIDE_K8S_JOB_SERVICE_ACCOUNT` / `REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS`
(KubernetesRunner), and `REACTORCIDE_BUILDER_*` (builder sidecar config).

**Removed** (do not set these on a worker anymore — the worker no longer
polls corndogs or touches Postgres/object storage directly):
`REACTORCIDE_WORKER_QUEUE`, `REACTORCIDE_WORKER_POLL_INTERVAL`,
`REACTORCIDE_WORKER_DRY_RUN`, `REACTORCIDE_WORKER_SHUTDOWN_TIMEOUT`,
`REACTORCIDE_DB_URI`, `REACTORCIDE_CORNDOGS_BASE_URL`, `CORNDOGS_API_KEY`,
`REACTORCIDE_OBJECT_STORE_*`, `REACTORCIDE_MASTER_KEYS`,
`REACTORCIDE_SECRETS_STORAGE_TYPE`.

## Note for personal/local redeploy scripts

If you have a personal deploy script outside this repo (for example, a
`~/redeploy-all-reactorcide.sh` that provisions the `reactorcide-worker`
deployment), this repo's automation will not touch it — update it yourself
with:

- **Add:** a coordinator URL for the worker to register against, and an
  enrollment-token secret reference (Kubernetes Secret `secretKeyRef`, or a
  file path via `--enrollment-token-file` / `REACTORCIDE_WORKER_ENROLLMENT_TOKEN`
  for a VM/Compose target). Create the pool + token first via the coordinator
  admin UI/CLI; never put the token value in the script itself.
- **Drop:** any corndogs URL/API key, `REACTORCIDE_DB_URI`, object-store
  config, or master-key config that the script currently passes to the
  worker deployment/container specifically — the worker doesn't use any of
  it anymore. (The coordinator deployment still needs its own copies of
  these; only remove them from the *worker*'s config.)
- **Drop:** `REACTORCIDE_WORKER_QUEUE`, `REACTORCIDE_WORKER_POLL_INTERVAL`,
  `REACTORCIDE_WORKER_DRY_RUN`, `REACTORCIDE_WORKER_SHUTDOWN_TIMEOUT` if the
  script sets any of them — all stale.

## Organization Pools and Worker Classes

A queue identity contains the organization, worker class, and canonical worker
characteristics. An organization pool can run only work for its organization.
A hosted pool has no organization owner. It can run only classes that an
administrator grants to it.

The coordinator checks the pool-to-class mapping when a worker claims a job.
The worker report can update recorded characteristics, but it cannot change
the pool boundary. A remote lease contains a short-lived job token. It does not
contain the coordinator instance token.

Manage worker classes and pool grants on `/app/workers/classes`. Every
organization starts with the `default` class. Mark a class as protected when
an untrusted execution profile must not select it. The token needs
`workers:manage`.

See [Organizations and Trusted CI Policy](./organizations-and-ci-policy.md)
for the REST paths and the relationship between policy, profiles, classes, and
pools.

## See also

- `docs/ui-auth.md` — the full RBAC permission matrix `ManageWorkers` sits in, plus login
  modes and session mechanics the worker-session model reuses.
- `docs/security-model.md` — the broader untrusted-code / secret-isolation security model
  (a job's `secrets` block, resolved by the coordinator into the lease response, is isolated
  from `env` and never touches the Corndogs task payload — the worker-protocol analog of the
  same secret-masking discipline runnerlib applies inside the container).
- `helm_chart/DEPLOYMENT.md` — how the Helm `worker:` values map to a Kubernetes deployment.
