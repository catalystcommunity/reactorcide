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

## Linux worker node configuration

We recommend `fs.inotify.max_user_instances = 8192` on each Linux worker
node. Keep a higher value if one is already configured. This setting limits
the number of inotify instances per real user ID. Concurrent builds can
reach a lower limit and fail to create file system watchers.

For Kubernetes, apply this setting to each node that can run job Pods.
Include the setting in node images or startup configuration so that new
and replacement nodes have the same limit. The Helm chart does not set
this host setting.

Create `/etc/sysctl.d/99-reactorcide-inotify.conf` on each worker node with
the following content. Use the existing value if it is higher than `8192`.

```ini
fs.inotify.max_user_instances = 8192
```

Apply the file and check the active value:

```bash
sudo sysctl --load=/etc/sysctl.d/99-reactorcide-inotify.conf
sysctl fs.inotify.max_user_instances
```

Check the value again after a node restart. Other system configuration
can override this file.

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

- `os` is auto-detected from the OS of the worker binary (`linux`, `macos`, or
  `windows`). You can override it with any literal string (`fedora`,
  `windows11`). The worker does not normalize the value.
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

## Scheduling state and runtime

A current worker claims a job in the `scheduling` state. The coordinator sets
`scheduling_started_at` and leaves `started_at` empty. The job changes to
`running` when the main container starts. A sidecar start does not start the
job runtime. The worker keeps its lease during scheduling. Cancellation,
kill requests, and lost-worker recovery also apply in this state.

Kubernetes workers report the main container's start and finish timestamps.
The result includes these timestamps even if the job finishes between
heartbeats. Runtime is `completed_at - started_at`. A job that never starts
has no runtime. Scheduling time ends at `started_at`, or at `completed_at`
if startup fails. The UI and `submit --wait` show the two times separately.
Other backends record the time after spawn and the time when execution ends.

Apply database migration `000039` and update the coordinator before the
workers. Older workers retain their claim-time `running` behavior. Existing
job timestamps are unchanged. Coordinator, worker, and node clocks must be
synchronized; the coordinator permits up to five minutes of clock difference.

## Kubernetes scheduling decisions

A Pod stays `Pending` when other jobs use the CPU or memory that it needs.
The default startup timeout is 30 minutes. Set
`REACTORCIDE_K8S_JOB_STARTUP_TIMEOUT` to a positive duration, such as `45m`,
to change it. In Helm, set `worker.jobStartupTimeout`. The job deadline still
includes startup and can expire first.

Set `REACTORCIDE_K8S_JOB_AUTOSCALING` or Helm `worker.jobAutoscaling`:

| Value | Early failure rule |
| --- | --- |
| `auto` (default) | Require a recent Cluster Autoscaler refusal that identifies a capacity or placement limit. An active scale-up prevents early failure. |
| `disabled` | The operator confirms that no node autoscaler will add capacity. Node capacity and scheduler events can establish an early failure. |

When scale-up is ruled out, the worker rejects a request that exceeds every
matching current node's allocatable CPU or memory. It checks the admitted
Pod requests, including init containers, native sidecars, pod-level requests,
and Pod overhead. Resources used by other jobs do not reduce the capacity
value. See the [Kubernetes resource rules](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).

The worker also checks `FailedScheduling` events for placement constraints.
After a 30-second startup grace period, it can return `SchedulingBlocked`
for node selector or node affinity mismatch, volume node affinity conflict,
or a custom taint that the Pod does not tolerate. Every reported node reason
must be a recognized placement constraint. The current scheduling condition
must agree with the event. In `auto` mode, the autoscaler refusal must be at
least as recent as the scheduler event.

Events must belong to the current Pod UID and the expected scheduler or
Cluster Autoscaler. Failure evidence expires after two minutes. A
`TriggeredScaleUp` event for the current Pod keeps the job waiting under the
startup timeout. A `NotTriggerScaleUp` event alone is not sufficient: backoff,
quota failures, unknown reasons, and readiness failures do not prove that
placement cannot recover. See the [Cluster Autoscaler event definitions](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#what-events-are-emitted-by-ca).

CPU or memory contention, mixed placement and capacity failures, and
unavailable event data keep the job waiting. Other autoscalers use the
timeout fallback unless they emit the supported Cluster Autoscaler events.
Image and container configuration errors still fail at once. Errors include
the scheduler diagnosis when available.

The worker needs `list` permission on cluster-scoped `nodes` and on `events`
in the job namespace. The Helm chart grants these permissions. Deploy the
worker image and Helm changes together.

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

The worker sets the custom characteristic `image_digest` itself, from its
image set. An operator value adds digests to that list. See "Image sets
and pre-seed" below.

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

## Image sets and pre-seed

An image set is the list of images that a worker must have before it can
claim the jobs that use them (owner decisions I4, I5, I8, I12, I13 in
`VM_IMAGES_PLAN.md`). The coordinator makes the set from the image aliases
that the worker can serve. Each entry has a pinned reference, a digest, a
kind (`vm` or `container`), and the alias names. Each set has a version.

An entry can have `pending: true`. Such an entry is only the pending pin of
a rollout: no alias that the worker serves has it as its active pin, and no
job that is not terminal uses it. New jobs do not get a pending digest
until every serving worker has it (see
[Image Aliases](./image-aliases.md#rollout-of-a-new-pin)). An entry with no
`pending` value is not pending. An older coordinator sends no value.

### What the worker does

1. The worker registers. The register response can contain the image set.
2. The worker pulls each entry of its kind. A `vm` worker pulls `vm`
   entries. A `docker`, `containerd`, or `kubernetes` worker pulls
   `container` entries. The worker ignores the other entries. The worker
   pulls each entry independently: when one entry fails, the worker
   continues with the other entries.
3. While it pulls, each heartbeat reports `updating` with the set version.
4. The worker does not call `RequestJob` until each entry that is not
   pending, and each `--vm-image-prefetch` image, is ready or failed
   permanently (the startup gate). Before the gate opens, the worker pulls
   the entries that are not pending first, in a pass of their own. It then
   pulls the pending entries. A pending entry does not keep the gate
   closed.
5. When every entry is ready, the worker reports `updated`. Each
   `RequestJob` sends the custom characteristic `image_digest` as a list of
   the ready digests. A digest that failed is not in the list, so the
   worker gets no job for it.

The coordinator puts the characteristic `image_digest: "sha256:..."` on
each job that uses an alias. The usual match rule then sends the job only
to a worker that has that digest in its list. The job uses the pinned
reference of the lease. The worker does not change it.

### Report states

Each heartbeat carries an image set report:

| Field | Value |
| --- | --- |
| `version` | the set version that the state is for (`0` when the worker has no set) |
| `state` | `updating`, `updated`, or `failed` |
| `detail` | a short failure reason, for `failed` |
| `ready_digests` | the digests that the worker has now |
| `free_bytes`, `required_bytes` | the free space and the space that the missing images need, when the backend can measure them |
| `failed` | each entry that failed: `digest`, `permanent` (true or false), and `detail`. An older worker does not send it |

The state is `updated` when every entry is ready, and `failed` when one or
more entries failed. The coordinator stores the `failed` list. The worker
list, `get-worker-image-state`, and the rollout status show it.

### Live updates

A heartbeat response can contain a newer set. The worker then:

- Pulls the new set in the background, and reports `updating` with the
  new version.
- Continues to claim jobs for the digests that it has. A digest becomes
  part of `image_digest` when its pull is complete.
- Reports `updated` when every image of the new set is ready. The list
  then has only the digests of the new set.
- Runs one update at a time. A new set stops only the pulls of the digests
  that are not in the new set. A pull of a digest that is also in the new
  set continues, and its result counts for the new set. The worker then
  starts the new set at once. The worker does not report the stopped
  version as `failed`.
- Accepts a set with a lower version when its content is different (the
  version of a worker starts again at 1 after a new enrollment). A set
  with the same content and another version changes only the version
  number. The coordinator sends the set when the version of the report is
  not the version that it wants (older or newer).

### Failures

One bad image does not stop the other images. The worker pulls each entry
independently, and puts each failed entry in the `failed` list of the
report with its class:

| Class | Causes | What to do |
| --- | --- | --- |
| Permanent (`permanent: true`) | The manifest or a blob is not found, the content does not agree with the digest, the registry refuses the credentials (unauthorized or denied), or the reference is not valid. | A retry with the same reference cannot help. The alias owner must fix the image or the access to it, or set the alias to another reference. |
| Transient (`permanent: false`) | A network error, a timeout, a 5xx answer from the registry, or no disk space. | The worker tries again. |

The state is then `failed` with a short detail. The worker keeps its old
images and claims jobs for the digests that it has. It tries the set again
after 30 seconds, and doubles the delay each time, to a maximum of 15
minutes. A newer set starts at once.

The startup gate (owner decision I4):

- The gate opens when each entry that is not pending is ready or failed
  permanently. A permanent failure cannot be fixed by a retry. The worker
  never sends a digest that it does not have, so it gets no job for that
  digest. Thus one bad alias does not stop the jobs of the other aliases.
- If an entry that is not pending fails with a transient failure, the gate
  stays closed. The worker reports `failed`, tries again with the backoff
  above, and does not call `RequestJob`.
- A pending entry does not keep the gate closed. If it fails, the worker
  reports `failed`, and the rollout status shows the failure. Fix the
  reference with `images alias set`, or set the active reference again to
  cancel the pending pin.
- After the gate opens, it stays open. A failed live update does not stop
  the worker.

The rollout of an alias looks only at the pending digest of that alias in
the ready digests of each serving worker. A failed image of another alias
(on a hosted pool, maybe of another organization) does not stop the
rollout.

Before a pull, the worker compares the free space with the estimated size.
If the space is not sufficient, the pull does not start, and the state is
`failed`. Only the `vm` backend can measure both values. For a VM image,
the estimate is the size of the stored blobs plus the bundle layer size
again, because the worker extracts the bundle next to its blob. An
extracted bundle can be larger than its compressed layer, so the estimate
is a lower limit. The `docker`, `containerd`, and `kubernetes` backends do
not report `free_bytes` or `required_bytes`.

### Removal of old images

The worker records each image that it pulled for a set in
`<data-dir>/image-set-owned.json`. When an image leaves the set and no
running job uses it, the worker removes it. The worker does this after
each completed update, and again at each `--vm-image-prune-interval`
(default 24 hours; one hour when the interval is zero). The worker never
removes:

- An image of the current set or of the set that is in progress.
- An image of the `--vm-image-prefetch` list.
- An image that the operator or another tool pulled before the set named
  it (`docker` and `containerd`).
- An image that a running job uses. The worker tries again later.

Per backend:

| Backend | Pull | Removal |
| --- | --- | --- |
| `vm` (macOS, Windows) | The VM image cache (`REACTORCIDE_VM_IMAGE_SOURCE=oci` or `s3`). The pull checks every blob digest. | The cache belongs to the worker, so every set image is owned. Retention (`--vm-image-max-unused`) never removes an image of the set or of a running job. |
| `docker` | The Docker pull API, with the same call that a job uses. | `docker image rm` without force. Docker refuses to remove an image that a container uses. |
| `containerd` | `nerdctl pull` in the `reactorcide` namespace, with the same credentials that a job uses. | `nerdctl rmi` without force. |
| `kubernetes` | The pre-seed DaemonSet (see below). | The kubelet image garbage collector. |

A VM worker with `REACTORCIDE_VM_IMAGE_SOURCE=local` cannot pull a set. It
reports `failed` for a set with `vm` images.

### Operator digests

`REACTORCIDE_WORKER_CUSTOM_IMAGE_DIGEST` (or `--custom image_digest=...`)
is not necessary. If you set it, the worker adds its values to the ready
digests: the list is the union. Use it only for an image that the worker
cannot pull, for example a local VM image. The operator value cannot
remove a digest from the list.

### Kubernetes image pre-seed

The Kubernetes worker keeps one DaemonSet in the job namespace for each
image set. The name is a prefix and a short hash of the sorted image
references. The prefix is `REACTORCIDE_K8S_IMAGE_PRESEED_NAME` (default
`reactorcide-image-preseed`, 52 characters or less; the Helm chart uses
`<worker name>-image-preseed`). Workers that have the same set use the
same DaemonSet. Different sets do not write over each other, also when
workers of different pools use the same prefix in one namespace.

Labels and annotations of the DaemonSet:

| Name | Value |
| --- | --- |
| label `reactorcide.io/component` | `image-preseed` |
| label `reactorcide.io/image-preseed` | the prefix |
| label `reactorcide.io/image-preseed-set` | the short hash (in the selector) |
| label `app.kubernetes.io/managed-by` | `reactorcide-worker` |
| annotation `reactorcide.io/image-preseed-hash` | the full hash of the image list |
| annotation `reactorcide.io/image-preseed-used-at` | the time of the last use (RFC 3339) |

The DaemonSet pod has:

- One init container from a small tools image
  (`REACTORCIDE_K8S_IMAGE_PRESEED_TOOLS_IMAGE`, default
  `busybox:1.36.1-musl`). It copies the static busybox binary into a shared
  `emptyDir`.
- One init container for each image of the set. It runs the copied binary
  as `true`, which exits at once. Thus an image with no shell and no
  binaries also works.
- A pause main container (`REACTORCIDE_K8S_IMAGE_PRESEED_PAUSE_IMAGE`,
  default `registry.k8s.io/pause:3.10`).
- The node selector, tolerations, and affinity of job pods
  (`REACTORCIDE_K8S_JOB_NODE_SELECTOR`, `REACTORCIDE_K8S_JOB_TOLERATIONS`,
  `REACTORCIDE_K8S_JOB_AFFINITY`). The DaemonSet also selects
  `kubernetes.io/os=linux` when the job selector does not set it.
- The image pull secrets of `REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS` and
  `REACTORCIDE_K8S_JOB_ALLOWED_IMAGE_PULL_SECRETS`.
- A non-root user, a read-only root file system, and no capabilities.

The worker reads the status only when the hash annotation is the hash of
its set. The set is ready when all of these are true:

- The controller saw the current generation (`observedGeneration` is
  `generation`).
- At least one pod is scheduled (`desiredNumberScheduled` is more than
  0). If no node matches the job node selector, the worker reports a
  transient failure for each image.
- The number of updated pods and the number of ready pods is the number of
  desired pods.

If a node cannot pull an image and a retry cannot help (for example, the
manifest is not found, the access is denied, or the name is not valid),
the worker reports a permanent failure for that image. It then writes a
DaemonSet for the other images. If the set is not ready in
`REACTORCIDE_K8S_IMAGE_PRESEED_TIMEOUT` (default `30m`), the worker reports
a transient failure with the pull errors of the pods for each image that
is not ready, and tries again later. An empty set does not delete a
DaemonSet.

Removal of old DaemonSets: each worker writes the time of use in the
`used-at` annotation of its DaemonSet (at most one time in 5 minutes). A
worker deletes a DaemonSet only when all of these conditions are true:

- It has the same prefix and the label
  `app.kubernetes.io/managed-by: reactorcide-worker`.
- No runner of this worker process uses its hash.
- Its creation time and its time of use are both more than 30 minutes
  ago.

Thus a DaemonSet that another live worker uses stays, also during a
rolling update.

A job pod with an image that is pinned by digest uses
`imagePullPolicy: IfNotPresent`, so it uses the image that the DaemonSet
pulled. A job image without a digest keeps `Always`.

**Limit:** a node that joins the cluster after the update gets the images
when its DaemonSet pod starts. A job can be scheduled on that node before
the pull is complete. That job then pulls the image itself, and its start
is slower.

Helm values: `worker.jobNodeSelector` (a map), `worker.jobTolerations` (a
list), `worker.jobAffinity` (an object), and `worker.imagePreseed.name`,
`.toolsImage`, `.pauseImage`, `.timeout`. The DaemonSet pods use the job
service account, so image pull secrets that are attached to that account
also apply.

**RBAC:** the worker service account needs `create`, `get`, `list`,
`watch`, `update`, `patch`, and `delete` on `apps/daemonsets` in the job
namespace. The Helm chart Role (`templates/rbac-worker.yaml`) has this
rule.

## Region, zone, and cloud

`region`, `zone`, and `cloud` are standard worker characteristics. They are
custom characteristics on the wire, so the satisfies rule above applies to
them without change:

- A job that does not require a location matches a worker in any location,
  and a worker with no location. During a capacity stockout in one region,
  workers in other regions take the work.
- A job that requires a location (for example `region: us-east-1` for data
  locality or a license) matches only workers with that value.

Require a location only when the job must run there.

| Flag | Env var | Value |
| --- | --- | --- |
| `--region` | `REACTORCIDE_WORKER_REGION` | region name, for example `us-east-1` |
| `--zone` | `REACTORCIDE_WORKER_ZONE` | zone name, for example `us-east-1a` |
| `--cloud` | `REACTORCIDE_WORKER_CLOUD` | free-form provider or site name |
| `--detect-topology` | `REACTORCIDE_WORKER_DETECT_TOPOLOGY` | `true` (default) or `false` |

A key set by one of these flags and also by `--custom` or
`REACTORCIDE_WORKER_CUSTOM_<KEY>` is a startup error. A worker that sets
`REACTORCIDE_WORKER_CUSTOM_REGION` only continues to work as before.

**Detection.** A Kubernetes worker with `--detect-topology` (the default)
reads the `topology.kubernetes.io/region` and `topology.kubernetes.io/zone`
labels of the eligible nodes at startup. It adds a value only when every
eligible node has the same value, and only when the operator did not set
that key. The runner does not pin a job Pod to a zone, so a worker whose
nodes span many zones advertises no zone. A detection failure is logged and
the worker starts without the values. Other backends do not detect a
location; set it with the flags.

The worker list in the admin UI shows the location of each worker. The
queue page shows the backlog by characteristic, for example by region. See
"Backlog and autoscaling" below.

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

**Kubernetes: one budget for the job and its sidecars.** The Kubernetes runner puts CPU and
memory in the pod's `spec.resources` (pod-level resources, beta and on by default since
Kubernetes 1.34). The job container and its `builder` (buildkitd) or `docker` (DinD) sidecar
share that budget. A build in the sidecar and the job container cannot use more than the
job's `memory.limit` together. The sidecars have no limits of their own. The GPU limit stays
on the job container, because pod-level resources accept only CPU, memory, and hugepages.

`REACTORCIDE_K8S_JOB_RESOURCE_SCOPE` (Helm `worker.jobResourceScope`) selects the scope:

| Value | Result |
|---|---|
| `auto` (default) | At the first job, the worker creates one server-side dry-run Job with pod-level resources. If the API server keeps the field, the worker uses `pod`; otherwise `container`. |
| `pod` | Always pod-level. Use only when the `PodLevelResources` feature gate is on: a server with the gate off drops the field without an error, and the pod runs with no limits. |
| `container` | CPU and memory on the job container only. The sidecars are not limited. |

The job's resource metrics follow the scope. With pod-level resources, the job roll-up
series (no `component` label) carry the pod values, and no per-container limit series exist.

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

An optional `storage` part sets the ephemeral (scratch) storage of the job
container (`request` and `limit`, memory grammar). Kubernetes applies it as
`ephemeral-storage`. Docker, containerd, and the VM runner do not enforce it.

## Priority and lanes

Priority is the only capacity mechanism. Corndogs gives the next task of a
worker's queues by priority (higher first), then by age. The coordinator
computes the priority of each job on every submit path: the base of the first
**lane** that matches the trusted facts of the job (ref classes, event type,
draft, eval or work, retry), plus the largest **priority shift** of the
groups of the actor (-50 to 50), plus the request of the job specification
(-50 to 50). A retry keeps the priority of its original job.

The built-in lanes put release work first (100), then the evals (10 and 5),
the default branch and manual work (0), ready pull requests and other
branches (-100), draft pull requests (-200), schedules (-300), and backfill
(-400). An organization or a project can replace a lane or add one. See
[Priority and Lanes](./priority.md).

## Resource-aware admission

A worker decides when to request more work. Set the mode with
`--admission` or `REACTORCIDE_WORKER_ADMISSION` (Helm
`worker.admission.mode`):

| Mode | Rule |
| --- | --- |
| `concurrency` (default) | The worker holds at most `--concurrency` leases. The job size does not matter. |
| `resources` | The worker requests work only when its headroom has room for a job. `--concurrency` is an upper bound only; when it is not set, the bound is 64. In Helm the bound is `worker.admission.maxLeases`. |

The default is `concurrency`, so a deployment that does not set the mode
works as before.

**Capacity and headroom.** In `resources` mode the worker measures its
capacity before each `RequestJob` call. Headroom is capacity less the
resources in use. CPU is compared by request (millicores). Memory is
compared by limit (bytes), because a job has no memory request and
Kubernetes uses the limit as the request.

| Backend | Capacity | In use |
| --- | --- | --- |
| Kubernetes | Sum of allocatable CPU and memory of the eligible nodes | Requests of every Reactorcide job Pod in the job namespace that is not finished, plus the leases of this worker that have no Pod yet |
| Docker, containerd | Host CPU count and memory (`/proc/meminfo`) | Leases of this worker |
| VM | No measurement: set a budget | Leases of this worker |

An eligible node is Ready, is not cordoned, has no `NoSchedule` or
`NoExecute` taint, and matches `REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR`
(Helm `worker.admission.capacityNodeSelector`) when it is set. Many worker
replicas in one cluster count the same job Pods, so they share one capacity.
Other workloads on the same nodes are not counted. Give jobs a dedicated
node pool and a node selector, or set a budget.

**Budget.** `--admission-cpu` / `REACTORCIDE_WORKER_ADMISSION_CPU` and
`--admission-memory` / `REACTORCIDE_WORKER_ADMISSION_MEMORY` (Helm
`worker.admission.cpu` and `worker.admission.memory`) replace the measured
capacity, one resource at a time. Use quantity strings such as `64` and
`256Gi`. Set a budget in these cases:

- A node autoscaler adds nodes. Nodes grow only when Pods wait, so the
  current node total never lets the worker start the Pods that cause a
  scale-up. Set the budget to the largest size of the node pool.
- The host runs other work.
- The host memory is unknown (not Linux). The worker does not start in
  `resources` mode without a known memory size.

The worker also fails at startup in `resources` mode when it cannot measure
capacity, for example without permission to list nodes or Pods. Later
measurement errors are logged, and the worker waits and tries again.

**Selection on the coordinator.** Corndogs claims the best task across a
set of queues: highest priority first, then oldest. The coordinator cannot
claim one specific task, so it selects the set of queues:

1. It reads the head job of each satisfied queue from the database (the
   same order as Corndogs).
2. It keeps a queue whose head job fits the headroom. It also keeps a queue
   with no known head; the claim-time check decides.
3. A head job that is larger than the worker's capacity never blocks that
   worker. Other workers can take it.
4. After the claim, the coordinator checks the claimed job again. A job
   that does not fit goes back to the queue (a race, or a task that is not
   the head that the coordinator read). The requeued task moves to the end
   of its priority in Corndogs.

**Anti-starvation.** A large job at the head blocks only its own queue, so
first in, first out stays true in a queue. Small jobs in other queues
continue. A stream of small jobs can keep the headroom of every worker
below a large job forever. To prevent this, a head job that fits the
worker's capacity but not its headroom, and that has waited at least
`REACTORCIDE_ADMISSION_RESERVE_AFTER` (coordinator, default `10m`), is
*starving*. The worker then reserves room for the first starving head in
Corndogs order: it gets only work that runs before that head (higher
priority, or same priority and older). In most cases it gets no new work,
its running jobs finish, its headroom grows, and the large job fits. Every
worker that could run the job reserves room, so the job starts on the first
worker that drains enough. Set the value to `0` to turn the reservation
off. Priority stays the only capacity mechanism; there are no per-tenant
caps or fair share.

**What the coordinator stores.** Each `RequestJob` call carries the report.
The coordinator stores the newest report in `workers.admission` (migration
`000059`) at most every 30 seconds, for display. The admin UI worker list
shows the headroom of a resources-mode worker, and "fixed" for a
concurrency-mode worker. Claims always use the report of the current call.

**Compatibility.** An older coordinator ignores the report and offers work
as in `concurrency` mode. Update the coordinator first.

### Right-sizing suggestions

Job resource history (`job_resource_summaries`) can suggest a CPU request
and a memory limit for one job: every finished run with the same
organization, project, and name.

- CPU request: the 95th percentile of the per-run CPU p95, plus 20 percent,
  rounded up to 50m, at least 100m.
- Memory limit: the 95th percentile of the per-run memory peak, plus 25
  percent, rounded up to 64Mi, at least 128Mi. When a run ran out of memory
  (failure class `oom`), the limit is at least 1.5 times the largest peak of
  such a run, because that peak is only the old limit.
- At least 5 measured runs are necessary. Cancelled runs are not used.

Read it with `GET /api/v1/jobs/{job_id}/resource-suggestion?limit=50`
(`jobs:read`, the same access as the job). The response gives the values as
job spec strings (`cpu_request`, `memory_limit`) and the current values of
the newest run. The History tab of a job in the UI shows the same
suggestion (`get-job-resource-history` field `suggestion`). Reactorcide
never changes job resources by itself.

## Backlog and autoscaling

The backlog is the demand signal of each queue: the jobs that wait for a
worker (status `submitted` or `queued`). For each queue it gives:

- the job count,
- the wait time of the oldest job,
- the sum of the CPU requests and of the memory limits,
- the worker characteristics that the queue requires.

It also gives the same sums for each characteristic value, for example
`region=us-east-1`, so operators see where capacity is short.

**Prometheus gauges** on `/api/v1/metrics`. The labels are `queue` (the
queue UUID) and `queue_name` (the display name) only, so the series count is
the queue count. A queue with no waiting work reports 0.

| Metric | Value |
| --- | --- |
| `reactorcide_queue_backlog_jobs` | waiting jobs |
| `reactorcide_queue_backlog_oldest_age_seconds` | wait time of the oldest waiting job |
| `reactorcide_queue_backlog_cpu_request_cores` | sum of CPU requests, in cores |
| `reactorcide_queue_backlog_memory_limit_bytes` | sum of memory limits |
| `reactorcide_queue_backlog_updated_timestamp_seconds` | time of the last successful read |

Each coordinator replica reads the database every 15 seconds and exports
the same values. Use `max by (queue)` in a query, not `sum`.

**REST.** `GET /api/v1/backlog` returns the full snapshot as JSON
(`queues`, `by_characteristic`, `totals`). Without `?org`, the token needs
`workers:read` or `workers:manage` for all organizations, and the result has
every queue. With `?org=<name>`, the token needs one of these capabilities
for that organization, and the result has only its queues. `workers:read` is
the least-privilege capability for an autoscaler service principal.

```bash
curl -fsS -H "Authorization: Bearer $(cat autoscaler-token)" \
  https://coordinator.example.com/api/v1/backlog
```

**UI.** `list-queues` (global admin) gives the oldest wait and the resource
sums of each queue, and `backlog_by_characteristic`. The `/app/workers/queues`
page shows them.

**Autoscaling.** Workers pull work, so many clusters, regions, and clouds
can drain one queue. The backlog says how much work waits, not where it
must run. Each cluster scales with its own autoscaler:

1. Read the backlog of the queues that the cluster's workers satisfy. Use
   the Prometheus gauges through a Prometheus adapter or an external metrics
   provider, or poll the REST endpoint from a scaler that supports HTTP JSON
   metrics.
2. Scale on a target per worker or per node: for example
   `ceil(reactorcide_queue_backlog_cpu_request_cores / cores_per_node)`
   nodes, or worker replicas for `reactorcide_queue_backlog_jobs`.
3. Use `oldest_age_seconds` to scale up faster when work waits too long.
4. Keep a minimum of 0 or 1 per cluster. Scale down only when the backlog
   is 0 and the workers have no leases: a worker drains its leases before
   it stops (`terminationGracePeriodSeconds`).

With `resources` admission and a node autoscaler, give the worker a budget
(the largest node pool size). The worker then claims jobs that wait as
Pending Pods, and the node autoscaler adds nodes for those Pods. A
provisioner that creates a VM for each job can use the same API.

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

## Claims, results, and recovery

A database outage must not leave a job at `running` with no worker. These rules apply.

**Claim (`RequestJob`).**

1. The coordinator resolves the job's secrets and its VCS checkout credential first. No
   write occurs before this step.
2. The coordinator then writes three rows in one transaction: the job status, the job
   token, and the `worker_leases` row. All three commit, or none commit. The job goes to
   `scheduling` for a worker that reports execution timing (each current worker), and to
   `running` for an older worker. See "Scheduling state and runtime".
3. If the transaction fails, the job stays at `submitted` and the Corndogs task goes back to
   `submitted`. The next `RequestJob` can lease the job.
4. A denied secret or credential sets the job to `failed`. The coordinator fails the Corndogs
   task only after that write. If the write fails, the task goes back to the queue.
5. A store error during step 1 is not a denial. The job stays at `submitted` and the task
   goes back to the queue.
6. A task that is not the job's current Corndogs task is cancelled at claim. It does not
   start the job.

**Session and lease errors.** A worker call returns `unauthorized` only when the session is
absent, unknown, expired, or revoked. A store error returns `unavailable`, which is
retryable. A lease or job lookup returns `not_found` only when the row does not exist.

**Result delivery (worker).** The worker retries `ReportResult` with backoff (1 s to 30 s)
for up to 15 minutes. Before each retry it sends the telemetry spool again, so the last log
and metric batches also get retries. On `unauthorized` the worker registers again before the
next attempt. The worker stops on `not_found`, `invalid_argument`, `forbidden`, or
`conflict`. The lease stays in the heartbeat list during the retries.

**Lost-job reconciler (coordinator).** The lease reaper loop runs every 60 s. It also:

- finds each job at `running` with no open lease for 2 minutes (after the job started, or
  after its last lease was released). It cancels the old Corndogs task, sets the job to
  `submitted`, increments `retry_count`, and submits a new task. After 2 requeues it sets
  the job to `failed` with `last_error` "lost: no lease after coordinator error; retries
  exhausted", and advances the workflow and the job's VCS check.
- finds each terminal job whose workflow node is still `submitted` or `running`, and gives
  the node its completion again.

A result that arrives after a requeue does not change the job. The coordinator releases the
old lease with outcome `superseded`.

**Coordinator database pool.** Each coordinator replica has its own pool. Set these on the
coordinator:

| Env | Default | Meaning |
|---|---|---|
| `DB_MAX_OPEN_CONNS` | `25` | maximum open connections |
| `DB_MAX_IDLE_CONNS` | `2` | idle connections kept |
| `DB_CONN_MAX_IDLE_SECONDS` | `300` | close an idle connection after this time |

## Admin UI and CSIL ops

Every worker, pool, queue, and worker class operation requires an org admin of the
organization of the target, or a global admin. This is the same role tier as
`ManageSecrets` and `ManageGroupsRoles` (see the permission matrix in `docs/ui-auth.md`).
An API token also needs a capability:

| Operation | Token capability |
| --- | --- |
| `list-workers`, `list-pools`, `list-queues`, `list-worker-classes` | `workers:read` or `workers:manage` |
| All other worker, pool, token, queue, and class operations (also `list-enrollment-tokens`) | `workers:manage` |

A queue has no owning organization. Thus each queue operation requires a global admin, or
an instance token with no roles and the capability above. A global pool has the same rule.
The REST backlog (`GET /api/v1/backlog`) also accepts `workers:read` (see "Backlog and
autoscaling").

**CLI.** `reactorcide workers` sends these CSIL operations with an API token
(`--api-url`, `--token`, or `REACTORCIDE_API_URL` and `REACTORCIDE_API_TOKEN`). The
subcommands are `list`, `set-status`, `drain`, `pools` (`list`, `create`, `update`,
`delete`), `tokens` (`create`, `list`, `deactivate`), `queues` (`list`, `create`,
`rename`, `delete`), and `classes` (`list`, `put`, `delete`, `set-pool`). Each subcommand
that shows data accepts `--format table|json|yaml`. The table columns are:

| Command | Columns |
| --- | --- |
| `workers list` | `ID`, `HOST`, `OS/ARCH`, `STATUS`, `IMAGES`, `POOL`, `TOKEN`, `LEASES`, `LAST SEEN` |
| `workers pools list` | `ID`, `NAME`, `ORG`, `DESCRIPTION` |
| `workers tokens list POOL_ID` | `ID`, `NAME`, `ACTIVE`, `ACTIVE WORKERS`, `LAST USED`, `CREATED` |
| `workers queues list` | `ID`, `NAME`, `CHARACTERISTICS`, `BACKLOG`, `DEFAULT` |
| `workers classes list` | `NAME`, `PROTECTED`, `POOLS` |

`IMAGES` is the image set state of the worker (`updating`, `updated`, or `failed`), with
the failure detail. `TOKEN` is the enrollment token ID and name, with `DEACTIVATED` for a
worker on a deactivated token.

**Webapp pages** (`webapp/`, a single-page application, capability-gated; the coordinator
authorizes each action again, so the webapp never decides who can do what):

- `/app/workers` — pools (create/rename/delete), enrollment tokens (create — token shown
  once — and deactivate permanently, with a confirmation dialog; each token shows its ID,
  name, and active worker count), and the worker list (status, platform, location,
  enrollment token, active lease count, admission headroom, last-seen, plus
  quarantine/disable/re-activate and drain controls, and a filter for the workers on a
  deactivated token).
- `/app/workers/queues` — queue list (characteristics read-only, backlog count via Corndogs'
  per-queue task counts, oldest wait and requested CPU and memory from the database, and the
  backlog by characteristic), create (pre-create a queue with its characteristics up front),
  rename (display name only), and delete (with confirmation, since it cancels in-flight
  jobs).
- `/app/workers/classes` — worker classes and their pool grants (see "Organization Pools
  and Worker Classes").
- `/app/workers/<worker-id>` — one worker, with its image set state.

**CSIL ops** (`ReactorcideUi`, same `POST /csil/v1/rpc` dispatcher as everything else in the
management API): `list-queues`, `create-queue`, `rename-queue`, `delete-queue`,
`list-workers`, `set-worker-status`, `drain-worker`, `list-pools`, `create-pool`,
`update-pool`, `delete-pool`, `create-enrollment-token`, `list-enrollment-tokens`,
`deactivate-enrollment-token`, `list-worker-classes`, `put-worker-class`,
`delete-worker-class`, `set-worker-class-pool`, `get-worker-image-state`. `drain-worker` is modeled as the same `quarantined` status
`set-worker-status` can also set directly (there's no separate fourth persisted worker
state) — `RequestJob` already refuses any further work to a non-`active` worker, and
`Heartbeat`'s response carries a `draining` flag so a worker still finishing existing leases
learns to shut itself down cleanly.

## Enrollment token rotation

A pool can have more than one active enrollment token. Each token has an ID
and an optional name. Only the ID and the name are shown after creation,
never the token value, so you can do an audit with no exposure of a token.

At each `Register`, the coordinator records the ID of the token on the worker
(migration `000066`). The worker list (`reactorcide workers list`, the
`/app/workers` page, and `list-workers`) shows the token ID and name of each
worker. `reactorcide workers tokens list POOL_ID` shows the active worker
count of each token: the workers with an active session that last registered
with it.

To rotate a token:

1. Create a new token with `reactorcide workers tokens create POOL_ID --name
   NAME --output-file FILE`.
2. Put the new token on each host and restart its worker. The worker
   registers with the new token, and its token ID changes.
3. Make sure that no worker uses the old token:
   `reactorcide workers list --enrollment-token OLD_TOKEN_ID`. The active
   worker count of the old token must be 0.
4. Deactivate the old token:
   `reactorcide workers tokens deactivate OLD_TOKEN_ID --confirm-permanent`.

**Deactivation is permanent.** No operation activates a token again, and a
database trigger refuses the change. The CSIL operation
`deactivate-enrollment-token` requires `confirm_permanent: true`, the CLI
requires `--confirm-permanent`, and the web page shows a confirmation dialog
with the count of workers that still use the token.

A worker that runs when you deactivate its token keeps its session. It works
until it stops, but then it cannot register again with that token. Such a
worker shows a "deactivated token" warning in the worker list. To find these
workers:

- `reactorcide workers list --on-deactivated-token`, or the filter on the
  `/app/workers` page (`on_deactivated_token` in `list-workers`).
- The gauge `reactorcide_workers_on_deactivated_enrollment_token` (labels
  `pool_id`, `enrollment_token_id`). Alert when it is above 0.

Give those hosts an active token before they restart. To stop such a worker
at once, disable it (`reactorcide workers set-status WORKER_ID disabled`).

A worker that has not registered since migration `000066` has no recorded
token. It gets one at its next registration.

## Operator setup flow

1. Create a pool with `reactorcide workers pools create NAME` or use
   `/app/workers` in the web application.
2. Create an enrollment token with `reactorcide workers tokens create POOL_ID
   --output-file FILE`. The command writes the raw token to the new protected
   file. The coordinator does not show the raw token again.
3. Store the token in the secret store for the deployment target. Do not store
   the token in a committed file. See the deployment instructions below.
4. Deploy the workers. Set the coordinator URL and the token file. Set the
   `os`, `arch`, and `custom` characteristics for the worker pool.
5. If necessary, create non-default queues before you submit a job. Use
   `/app/workers/queues` or `reactorcide workers queues create`. This step is
   optional. The coordinator can create a queue for new characteristics.
6. Submit jobs with the necessary `characteristics` and `resources`. The
   coordinator selects a queue and a matching worker.

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
into a managed Secret (`<worker name>-enrollment`, by default
`reactorcide-worker-enrollment`, key `token`,
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
  # region: ""             # optional; detected from node labels otherwise
  # admission:
  #   mode: "concurrency"  # or "resources" (see "Resource-aware admission")
  # kubeconfig:
  #   secretName: ""       # Secret with a kubeconfig for a remote cluster
```

**Rotation:** to rotate the auto-generated token, delete the managed Secret and
re-run `helm upgrade` (a new value is generated and re-seeded); or switch to an
operator-override Secret and rotate it via the admin UI's token
create/deactivate flow (a pool can hold several active tokens at once, so you
can roll a new one out before retiring the old).

See `helm_chart/values.yaml` (`worker:` section) and
`helm_chart/DEPLOYMENT.md` ("Worker Configuration") for the full set of knobs,
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

**Postgres resources.** With `REACTORCIDE_PROVISION_POSTGRES=true`, the deploy writes the
Zalando `postgresql` resource with pod resources. Without them the operator applies a 500Mi
memory limit, and a burst of parallel jobs can OOM-kill Postgres. Defaults: request
`100m`/`256Mi`, limit `1`/`1536Mi`. Override with `REACTORCIDE_POSTGRES_CPU_REQUEST`,
`REACTORCIDE_POSTGRES_CPU_LIMIT`, `REACTORCIDE_POSTGRES_MEMORY_REQUEST`, and
`REACTORCIDE_POSTGRES_MEMORY_LIMIT`. The production resource is in
`deployment/k8s/reactorcide-postgres.yaml`.

### Remote Kubernetes cluster (kubeconfig)

The Kubernetes runner uses the in-cluster service account by default. To
drive another cluster from one worker host, give the worker a kubeconfig
file:

| Env var | Purpose |
| --- | --- |
| `REACTORCIDE_K8S_KUBECONFIG` | path of a kubeconfig file; empty uses the in-cluster config |
| `REACTORCIDE_K8S_CONTEXT` | context in that file; empty uses the file's current context; needs `REACTORCIDE_K8S_KUBECONFIG` |

Set `--container-runtime kubernetes` (`REACTORCIDE_CONTAINER_RUNTIME`)
on a host outside a cluster: `auto` selects Kubernetes only when the Pod
service account is present.

Every runner call uses this one connection: Jobs, Pods, logs, exec, events,
nodes, and the kubelet stats proxy (`nodes/{node}/proxy/stats/summary`) for
job metrics. When `REACTORCIDE_K8S_JOB_NAMESPACE` is empty, the job
namespace is the namespace of the kubeconfig context, or `default`.

Job Pods in the remote cluster must reach the coordinator API to submit
triggers. The Helm chart sets `REACTORCIDE_JOB_API_URL` to the in-cluster
Service address of the coordinator. For a remote cluster, set Helm `worker.jobApiUrl` to an
address that the remote Pods can reach.

The kubeconfig identity needs the same permissions that the Helm chart
grants to the worker service account (`helm_chart/templates/rbac-worker.yaml`):
Jobs, Pods, `pods/log`, `pods/exec`, events, and short-lived Secrets in the
job namespace, `metrics.k8s.io` Pods, and cluster-scoped `list nodes` and
`get nodes/proxy`. Do not put the kubeconfig in a committed file. In Helm,
store it in a Secret and set `worker.kubeconfig.secretName` (and optionally
`worker.kubeconfig.key`, default `config`, and `worker.kubeconfig.context`).
The chart mounts the Secret read-only at `/etc/reactorcide/kubeconfig`.

```bash
kubectl create secret generic reactorcide-remote-kubeconfig \
  --from-file=config=/path/to/remote-kubeconfig -n reactorcide
```

## Worker environment reference

Everything the `reactorcide worker` binary reads, as of the coordinator-mediated
worker (`coordinator_api/cmd/worker.go`):

| Flag | Env var | Purpose |
| --- | --- | --- |
| `--coordinator-url` | `REACTORCIDE_COORDINATOR_URL` | required; base URL of the coordinator |
| `--allow-insecure-transport` | — | explicit development-only exception for a connection without TLS |
| `--enrollment-token-file` | — (file path) | preferred way to supply the enrollment token |
| — | `REACTORCIDE_WORKER_ENROLLMENT_TOKEN` | fallback if no `--enrollment-token-file` |
| `--data-dir` | `REACTORCIDE_WORKER_DATA_DIR` | directory persisting `worker_key` |
| `--worker-key-file` | — | overrides the default `<data-dir>/worker_key` path |
| `--hostname` | `REACTORCIDE_WORKER_HOSTNAME` | identifying hostname sent on Register |
| `--os` | `REACTORCIDE_WORKER_OS` | worker `os` characteristic (auto-detected otherwise) |
| `--arch` | `REACTORCIDE_WORKER_ARCH` | worker `arch` characteristic (auto-detected otherwise) |
| `--custom` (repeatable) | `REACTORCIDE_WORKER_CUSTOM_<KEY>` | free-form `key=value` characteristics, typed/list-inferred — see "Custom characteristics" above |
| `--worker-version` | `REACTORCIDE_WORKER_VERSION` | optional build/version string |
| `--container-runtime` / `-r` | `REACTORCIDE_CONTAINER_RUNTIME` | `docker`, `containerd`, `kubernetes`, `vm` (macOS and Windows only), or `auto` (default; Kubernetes in a Pod, otherwise Docker) |
| `--workspace-dir` | `REACTORCIDE_WORKER_WORKSPACE_DIR` | base directory of each job workspace; empty uses the OS temp directory |
| `--concurrency` / `-c` | `REACTORCIDE_WORKER_CONCURRENCY` | concurrent leases per worker process (an upper bound in `resources` mode) |
| `--admission` | `REACTORCIDE_WORKER_ADMISSION` | `concurrency` (default) or `resources` — see "Resource-aware admission" |
| `--admission-cpu` | `REACTORCIDE_WORKER_ADMISSION_CPU` | CPU budget in `resources` mode |
| `--admission-memory` | `REACTORCIDE_WORKER_ADMISSION_MEMORY` | memory budget in `resources` mode |
| `--region`, `--zone`, `--cloud` | `REACTORCIDE_WORKER_REGION`, `REACTORCIDE_WORKER_ZONE`, `REACTORCIDE_WORKER_CLOUD` | standard location characteristics |
| `--detect-topology` | `REACTORCIDE_WORKER_DETECT_TOPOLOGY` | detect region and zone from Kubernetes node labels (default `true`) |
| `--metrics-interval` | `REACTORCIDE_WORKER_METRICS_INTERVAL` | CPU and memory sample interval from `1s` through `1m` |
| `--storage-metrics-interval` | `REACTORCIDE_WORKER_STORAGE_METRICS_INTERVAL` | storage sample interval from `1s` through `1m` |
| `--telemetry-send-interval` | `REACTORCIDE_WORKER_TELEMETRY_SEND_INTERVAL` | maximum interval between telemetry batches |
| `--telemetry-buffer-batches` | `REACTORCIDE_WORKER_TELEMETRY_BUFFER_BATCHES` | retained unsent batches for each lease |
| `--vm-image-registry-plain-http` | — | explicit development-only HTTP registry host; repeat for more hosts |
| `--vm-image-prefetch` (repeatable) | `REACTORCIDE_VM_IMAGE_PREFETCH` | VM images to pull before Register. The worker adds them to each image set and never removes them. See "Image sets and pre-seed" |
| `--vm-image-max-unused` | `REACTORCIDE_VM_IMAGE_MAX_UNUSED` | VM cache retention (default `720h`). Images of the image set, of the prefetch list, and of running jobs stay |
| `--vm-image-prune-interval` | `REACTORCIDE_VM_IMAGE_PRUNE_INTERVAL` | interval of VM retention and of the removal of images that left the image set (default `24h`) |

The worker process also still reads a few runner/job-execution env vars that
are unrelated to the coordinator protocol:
`REACTORCIDE_JOB_API_URL` / `REACTORCIDE_API_TOKEN` (let job containers
submit triggers), `REACTORCIDE_K8S_JOB_NAMESPACE` /
`REACTORCIDE_K8S_JOB_SERVICE_ACCOUNT` / `REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS` /
`REACTORCIDE_K8S_JOB_ALLOWED_IMAGE_PULL_SECRETS`
(KubernetesRunner, see [Job Image Pull Secrets](#job-image-pull-secrets)),
`REACTORCIDE_K8S_KUBECONFIG` / `REACTORCIDE_K8S_CONTEXT` (see "Remote
Kubernetes cluster" above), `REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR` (see
"Resource-aware admission"), `REACTORCIDE_K8S_JOB_NODE_SELECTOR`
(`key=value` pairs, comma separated) / `REACTORCIDE_K8S_JOB_TOLERATIONS`
(a JSON list) / `REACTORCIDE_K8S_JOB_AFFINITY` (a JSON object) for job
pods and the pre-seed DaemonSet, `REACTORCIDE_K8S_IMAGE_PRESEED_NAME` /
`_TOOLS_IMAGE` / `_PAUSE_IMAGE` / `_TIMEOUT` (see "Kubernetes image
pre-seed"), `REACTORCIDE_K8S_JOB_RESOURCE_SCOPE` (see "Resources"),
`REACTORCIDE_K8S_JOB_STARTUP_TIMEOUT` / `REACTORCIDE_K8S_JOB_AUTOSCALING`
(see "Kubernetes scheduling decisions"), `REACTORCIDE_EGRESS_*` (see
[Egress Proxy](./egress-proxy.md)),
and `REACTORCIDE_BUILDER_*` (builder sidecar config, see
[Builds, Caches, and Job Settings](./builds-and-caches.md)).

**Removed** (do not set these on a worker anymore — the worker no longer
polls corndogs or touches Postgres/object storage directly):
`REACTORCIDE_WORKER_QUEUE`, `REACTORCIDE_WORKER_POLL_INTERVAL`,
`REACTORCIDE_WORKER_DRY_RUN`, `REACTORCIDE_WORKER_SHUTDOWN_TIMEOUT`,
`REACTORCIDE_DB_URI`, `REACTORCIDE_CORNDOGS_BASE_URL`,
`REACTORCIDE_OBJECT_STORE_*`, `REACTORCIDE_MASTER_KEYS`,
`REACTORCIDE_SECRETS_STORAGE_TYPE`.

## Job Image Pull Secrets

Four separate settings control private image pulls. Each setting holds
Kubernetes Secret NAMES only, never credential values:

- Top-level Helm `imagePullSecrets` lets the Reactorcide service pods
  (coordinator, worker, web) pull their own images.
- `worker.jobImagePullSecrets` adds the listed Secrets to every generated
  job pod (`REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS`).
- `worker.allowedJobImagePullSecrets` permits a job to request the listed
  Secrets with its own `image_pull_secrets` list
  (`REACTORCIDE_K8S_JOB_ALLOWED_IMAGE_PULL_SECRETS`). A name in the global
  `worker.jobImagePullSecrets` list is also an approved name. A name that is
  only in `allowedJobImagePullSecrets` is attached only when a job requests
  it.
- Job `image_pull_secrets` selects secrets for one job. See
  [Job Definitions](./job-definitions.md#job-image-pull-secrets).

The allowlist is operator configuration on the worker deployment, not
coordinator state. The worker enforces it before it creates a Kubernetes
Job, so a compromised coordinator or job definition cannot attach an
arbitrary namespace Secret to a job pod. With an empty allowlist, the worker
rejects every job-level request. This is the secure default: a pod-level
image pull secret reference makes the kubelet send that Secret's registry
credentials to the registry that hosts the job image, and the job author
controls the image reference.

Each referenced Secret must exist in the configured Kubernetes job
namespace, have type `kubernetes.io/dockerconfigjson`, and contain
credentials for the registry in the selected image. Create the Secret and
add its name to the allowlist in one operator step:

```bash
kubectl create secret docker-registry regcred \
  --docker-server=containers.example.com \
  --docker-username=ci-pull \
  --docker-password-stdin \
  -n reactorcide-jobs < /path/to/registry-password
```

```yaml
# values.yaml
worker:
  allowedJobImagePullSecrets:
    - regcred
```

Kubernetes reports a missing Secret or a bad credential as a pod image pull
error. Reactorcide does not read Secret data to improve that error.

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
an untrusted execution profile must not select it: the coordinator refuses a
job whose profile denies secrets, unless the profile names the class in
`allowed_worker_classes`. The token needs `workers:manage`.

See [Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md)
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
