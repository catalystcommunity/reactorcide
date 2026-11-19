# Reactorcide System Design

This document describes the implemented Reactorcide architecture. Use the
linked operator guides for installation procedures.

## Purpose

Reactorcide runs CI/CD jobs in isolated containers. It supports these use
cases:

- Run a job from a developer workstation.
- Run jobs on a single VM.
- Run jobs as Kubernetes Jobs.
- Start jobs from VCS webhooks.
- Run trusted CI definitions against source from an outside contributor.

The local and remote paths use the same job format. This makes a job easier to
test before you submit it to a coordinator.

## Components

### CLI

The `reactorcide` binary provides these functions:

- Run a job locally with `run-local`.
- Submit a job to a coordinator.
- Read job logs.
- Manage API tokens, secrets, and secret grants.
- Operate the coordinator: organizations, projects, workers, pools, image
  aliases, CI policy, approvals, and the other management operations.
- Build, publish, and pull VM images (`vm-image`).
- Start the coordinator.
- Start a worker, or install the Windows worker service.

See [CLI Reference](./docs/cli-reference.md).

### Coordinator

The coordinator is the control plane. It:

- Provides the REST API and the management API.
- Stores users, projects, jobs, workflows, workers, and access rules in
  PostgreSQL.
- Submits ready work to Corndogs.
- Enrolls workers and gives work to authenticated workers.
- Receives job logs and results from workers.
- Resolves approved secrets and VCS credentials.
- Processes supported VCS webhooks.
- Updates VCS statuses and pull-request comments.
- Evaluates workflow dependencies after each job result.

The coordinator is the only component that must connect to PostgreSQL,
Corndogs, and the object store.

### Worker

A worker runs and monitors job execution. It:

- Enrolls with a coordinator.
- Advertises its operating system, architecture, and custom
  characteristics.
- Requests work from the coordinator.
- Starts and stops job containers or guest VMs.
- Sends logs, status, and results to the coordinator.

A worker does not connect directly to PostgreSQL, Corndogs, or the object
store.

The worker supports these execution backends:

- Docker
- containerd through nerdctl
- Kubernetes Jobs
- A VM backend on supported macOS and Windows builds

Each VM job runs in a clone of a base image in an ephemeral scratch
directory. The worker deletes the directory after the job. At start and
before each boot, the worker removes each scratch directory (and, on
Windows, each job VM) that no live worker owns. Windows base images are not
generalized (no Sysprep).

A VM guest has no network device. The worker reaches the guest daemon
(`vmguest`) through a hypervisor socket: vsock on macOS, Hyper-V sockets on
Windows. Each guest TCP flow goes through the same socket to the host egress
forwarder as an HTTP `CONNECT` request. The forwarder connects directly, or
sends the flow to the egress proxy in `REACTORCIDE_VM_EGRESS_PROXY`. See
[macOS VM Runners](./docs/vm-runners-macos.md) and
[Windows VM Runners](./docs/vm-runners-windows.md).

See [Worker Operation](./docs/workers.md) for enrollment, queue matching, and
worker administration.

### Egress proxy

The egress proxy (`egress_proxy/`, Rust, copied from the Magi egress proxy)
is an optional component. It ends TLS with its own CA, permits only policy
destinations, scans traffic for secret values, runs WASM filter plugins, and
puts proxy-delivered secrets in place of their placeholders. The network
helper (`egress_proxy/netd`, copied from Magi `envd`) installs an nftables
redirect in the job network namespace, so all job TCP goes through the proxy
and other traffic is dropped; new inbound connections are dropped too. An
egress job container has no capabilities and no-new-privileges, and cannot
run as root (raw packets skip nftables). Workers register each job with egress control
over the proxy control API; the job authenticates with a per-job credential.
See [Egress Proxy](./docs/egress-proxy.md).

### Runnerlib

Runnerlib is a Python library and command-line program in a job image. It:

- Prepares source and trusted CI checkouts.
- Runs a job command.
- Resolves local job configuration.
- Masks registered secret values in its output.
- Reads VCS event context.
- Evaluates repository job and workflow files.
- Writes or submits follow-up job triggers.

The worker owns the container lifecycle. Runnerlib operates in the job
container.

See [Runnerlib Design](./runnerlib/DESIGN.md).

### Web application

The web application provides the browser interface. It is a Go bridge and
a Zest web-component single-page application, compiled into one binary. The
bridge forwards CSIL-RPC calls to the coordinator UI service with the
session of the browser. It holds no coordinator API token, and it discards
any credential that the client supplies. The bridge and the event stream
proxy (`/app/ws`) refuse a browser request from another origin. The web
application is optional.

See [UI Authentication and Authorization](./docs/ui-auth.md).

### Corndogs

Corndogs stores ready job leases. The coordinator uses its CSIL-RPC TCP
interface. Workers do not connect to Corndogs.

### PostgreSQL

PostgreSQL stores control-plane records. Database migrations run when the
coordinator starts. You can also run migrations with `reactorcide migrate`.

### Object store

The coordinator stores job logs, job telemetry, and job artifacts in the
configured object store.

Implemented backends are:

- `filesystem`
- `memory` for tests
- `s3`, including compatible S3 services

The Kubernetes filesystem backend uses ephemeral pod storage. Do not use it
for durable production logs. The GCS configuration keys are reserved, but a
GCS backend is not implemented.

## Control Flow

### Local job

1. The user runs `reactorcide run-local JOB_FILE`.
2. The CLI loads the job file and overlays.
3. The CLI creates a workspace.
4. The selected backend starts the job container.
5. The job runs against a bind mount or a requested Git checkout.
6. The CLI returns the job exit status.

`run-local` uses the host user for a direct bind mount by default. Use
`--as-runner`, `--user`, `run_as`, or `run_local` when the job needs a
different user.

### Local workflow

1. The user runs `reactorcide run-local WORKFLOW_FILE`.
2. The CLI makes a temporary trusted CI tree for the selected workflow.
3. Runnerlib resolves the workflow and its job files.
4. The shared workflow engine expands nodes and selects ready nodes.
5. The local executor runs ready nodes with the selected backend.
6. Each node receives the current workflow variables in a protected file.
7. The shared engine merges node outputs and rejects conflicting values.
8. The CLI writes `reactorcide-workflow-summary.json` in the workspace.

The engine uses a storage adapter and an execution adapter. The coordinator
uses a database storage adapter. Local execution uses an in-memory storage
adapter. Both modes use the same fan-out, dependency, condition, variable
conflict, and final-status rules.

### Remote job

1. A user, VCS event, or parent job creates a job in the coordinator.
2. The coordinator records the job and sends ready work to Corndogs.
3. An authenticated worker requests a matching lease.
4. The worker creates a clean workspace.
5. The worker gets approved secrets and VCS checkout credentials from the
   coordinator.
6. The coordinator records `scheduling` while the worker prepares execution.
   The worker starts a container or a Kubernetes Job. It reports `running`
   with the main container start time. Runtime excludes scheduling time.
7. The worker sends logs and state changes to the coordinator.
8. The coordinator stores the result and evaluates dependent workflow nodes.
9. The coordinator submits newly ready nodes.
10. The coordinator updates the VCS workflow status when applicable.

## Source and CI Separation

A job can have two Git sources:

- `SourceURL` and `SourceRef` identify the code that the job tests.
- `CISourceURL` and `CISourceRef` identify trusted CI content.

The normal paths are:

```text
/job/src   application source
/job/ci    trusted CI source
```

For a pull request from a fork, the application source URL points to the fork.
For an open or updated pull request, the CI source URL points to the upstream
repository at a trusted base commit. For a merged pull request, it points to the
trusted revision on the target branch.
This prevents the pull request from replacing its own CI definition.

This separation is not a complete sandbox. Source code and CI code can still
run in the same job container. Do not give a secret to a job that executes
untrusted source unless the job design makes that access acceptable. Use
separate jobs and secret grants to reduce the scope.

See [Security Model](./docs/security-model.md).

## Job Model

A job is one container execution. The canonical worker job fields include:

- Image and command
- Environment
- Source type, URL, and ref
- Code, job, and working directories
- Timeout
- Container user
- CPU and memory limits
- Runtime capabilities
- Worker characteristics

The image of a job can be an image alias. The coordinator resolves the
alias to its pinned reference when it creates the job. A retry or a re-run
resolves the alias again and uses its current pin. The job shows the
image in the display form `alias (ref)` (`image_display`). When a retry
gets a different image, the job also records the earlier image
(`image_retry_of`). See
[Image Aliases and Worker Image Sets](./docs/image-aliases.md).

Each job command goes through a shell prefix: `sh -c` by default,
PowerShell for a Windows job, or the value of `REACTORCIDE_JOB_SHELLCMD`.
The value `none` runs the words of the command directly. See
[Command Execution](./docs/job-definitions.md#command-execution).

Job YAML can use a flat format for direct execution. Repository event files
can use a nested `job` block. See [Job Definition
Reference](./docs/job-definitions.md).

### Runtime capabilities

Capabilities request runtime features. They do not change the container user.

- `docker` gives a supported job access to a Docker-compatible runtime.
- `builder` starts a BuildKit sidecar or equivalent builder service. The
  coordinator selects the cache namespace and the write mode of the sidecar
  (profile and ref classes). The operator can select a rootless sidecar.
- `gpu` requests GPU access where the backend supports it.

Use `run_as.user` when a job must use `root` or a numeric user.

### Builds, caches, and job settings

Reactorcide has no build-tool awareness. It gives jobs generic parts: the
reserved facts `REACTORCIDE_CACHE_NAMESPACE`, `REACTORCIDE_CACHE_WRITABLE`,
`REACTORCIDE_ARTIFACT_NAMESPACE`, and `REACTORCIDE_REF_CLASSES`; named links
in the workflow output (UI, PR comment, REST, CSIL); non-secret environment
defaults per organization, worker class, and project (job > project > worker
class > organization); and an optional organization image policy (allowed
prefixes, digest pinning) that the coordinator checks at submit and at claim.
The coordinator computes all of these and sends them in the lease. See
[Builds, Caches, and Job Settings](./docs/builds-and-caches.md) and
[Runner Images](./docs/runner-images.md).

### Artifacts, test results, and provenance

A job publishes files when it writes them under `/job/artifacts`
(`REACTORCIDE_ARTIFACTS_DIR`). After the job, the worker uploads each
regular file to the coordinator in chunks on its authenticated connection; it
does not follow symbolic links and never connects to the object store. The
coordinator checks the limits (per file, per job, number of files), stores
the chunks under `artifacts/<artifact namespace>/<job id>/`, and computes the
size and SHA-256 digest itself. Reads follow the visibility of the job.
Artifacts follow the telemetry retention.

JUnit XML reports (`*junit*.xml`, or `test_reports` in the workflow output)
become per-test results with a stable test ID (a hash of the project, suite,
class, and name). The parser streams, limits size and depth, and refuses
entity declarations. The PR comment and the job page show the failing tests
with the first lines of their messages. A test that passed and failed on the
same commit in the detection window is flaky. A test quarantine has an owner
principal, a reason, and a required expiry. The worker gives the job the
quarantined tests in `REACTORCIDE_QUARANTINED_TESTS_FILE`. The job exit code
is never changed: a node with `quarantine_policy: ignore_quarantined`
succeeds when its job failed only by quarantined tests.

For each completed job with subjects (its artifacts, and digests that it
declares in `provenance_subjects`), the coordinator writes an in-toto SLSA
Provenance v1 statement from its own records (source and trusted CI commits,
workflow, node, ref, event, ref classes, actor, CI origin, profile, policy,
approval, worker class), signs it as a DSSE envelope with a provenance key
(the OIDC key management, a separate key table), and stores it as an
artifact. The public keys are at `GET /api/v1/provenance/keys`.
`reactorcide provenance verify` checks a statement and its ref class and CI
origin for a deploy-time admission policy. See
[Artifacts and Test Results](./docs/artifacts-and-tests.md) and
[Provenance](./docs/provenance.md).

### Failure classes

Each job that does not complete gets a failure class. The class tells who
must act: the pipeline owner (`job`, `timeout`, `oom`), the CI owner
(`infra.image_pull`, `infra.scheduling`, `infra.worker_lost`,
`infra.platform`), or nobody (`cancelled`). The runner that knows the cause
sets the class. The worker reports it with the result. The coordinator sets
the class for failures that it finds (a lost worker, a queue submission
error). The worker stops a job that runs longer than its timeout and
reports the status `timeout`.

The coordinator retries `infra.platform` and `infra.worker_lost` failures
automatically, up to the `infra_retries` setting of the organization or
project. It retries `infra.platform` only when the worker proves that the
main container never started, because a retry runs the job command again.
It never retries a pipeline failure. See "Failure Classes" in
[Runtime Behavior](./docs/runtime-behavior.md).

## Workflow Model

A workflow contains jobs and dependency edges. The coordinator owns workflow
state and scheduling.

Runnerlib can create triggers with:

- Job name
- Dependencies
- A condition
- Environment
- Job configuration
- `for_each` values

The coordinator records all nodes. It submits only nodes whose dependencies
are ready. A blocked node does not occupy a worker.

A workflow job can start a child workflow. The coordinator stores the parent
workflow, the root workflow, the job that started the child, and the trigger
operation ID. A root workflow does not set `root_workflow_id`. Each child sets
`root_workflow_id` to the root ID. This rule prevents a self-reference on the
root row.

The coordinator calculates the root status from all nodes in the workflow
graph. The event aggregates its root workflows into one VCS status,
`Reactorcide Checks`. The root
status stays pending while a node in the graph is not complete. A failed or
timed-out node fails the root. The root can report success only after all
required nodes in the graph complete successfully.

Runnerlib adds an operation ID to each trigger request. The coordinator uses
this ID with the parent job and workflow name to process a repeated request
only one time. For an eval job, the operation ID is the event ID. Thus a
retried eval cannot create a second copy of a workflow.

A job can have an `if` block with declarative predicates. A job whose `if` is
false becomes a skipped node. Each node stores structured decision reasons.
Runtime triggers from a running job (dynamic jobs) are allowed unless the
organization or project `dynamic_jobs` setting denies them. A new
organization denies them (the `secure` defaults profile). Only a global
administrator can set the project `default_job_command`, which replaces the
eval command. Workflow files stay declarative.

Supported dependency conditions are:

- `all_success`
- `any_failed`
- `always`

A workflow can name an owner (`owners: {group, subjects}`) and a contact
(`contact: {channel, help_url, email}`). The coordinator reads them from the
trusted CI definition only: the base definition when policy grants head CI,
and an earlier trusted snapshot when the eval read the pull request head
itself. A pull request cannot change the owner of the workflow it runs. The
coordinator stores the owner on each workflow and job at creation, and the
snapshot never changes. It feeds the UI, the pull-request report, the
`owner_group` OIDC claim and outbound event field, the owner group priority
shift, the `workflow_owner` approver subject, and workflow roles.

See [Workflow Design](./docs/workflow-design.md).

## Ref Classes

The coordinator classifies the ref of each event with labels, the ref
classes. Built-in rules give `default` (a push to the default branch), `tag`,
`pr`, `fork`, `schedule`, and `manual`. Organization and project rules add
classes such as `production` or `release`. Rules only add classes; no rule
removes one. The coordinator uses trusted facts only: the signed event, the
schedule, the authenticated submit, and its stored rules. Workflow code
cannot set a class.

The event stores its classes. Workflows and jobs inherit them through a
database trigger, so child jobs, retries, and reruns keep them. An update
never changes stored classes. A manual event never gets `default` or `tag`,
and a ref condition of a rule applies to it only when the rule names the
event type `manual`. Each job gets
`REACTORCIDE_REF_CLASSES`. Secret grants, CI policy rules and defaults, and
worker classes can read the classes. Nothing restricts by class until an
operator configures it. See [Ref Classes](./docs/ref-classes.md).

## Required Set

The required set of a project names the workflow security IDs that block a
merge. Only those workflows count toward `Reactorcide Checks`. Other
workflows are advisory: they run and report in the pull request comment only.
They post no commit status, also not their `status.context`. The set is off
by default, so every workflow blocks. When an org admin turns it on, every
known workflow goes into the set. A promotion or a demotion is a proposal
that an approver decides. The approvers come from a branch rule, the
approver setting, or the org admins. A quarantine removes an entry until an
expiry. The coordinator computes the state each time it reads the set, so a
quarantine ends with no background work. At a shed level with
`skip_advisory`, the eval skips advisory workflows. See
[Required Set](./docs/required-set.md).

## Reporting and Ownership

A report mode of a project tells which VCS writes it gets: `full` (statuses
and the pull request comment, the default), `statuses_only`, or `silent` (no
VCS write; the results are only in the UI and the API). The mode applies to
each status, the shared comment, the legacy comments, and `Reactorcide
Reviews`. The agreement report compares the final `Reactorcide Checks`
result of each commit with the statuses and check runs of another CI system
on the same commit. A lease-guarded background pass reads them with a
backoff, because the other CI system can finish later. With the code owners
import on (off by default), the coordinator reads the code owners file of
the trusted base SHA, caches it per repository and SHA, and makes one derived
required review per owner set of the changed files, with the GitHub
semantics. Each required review has its own line in the pull request comment.
With path acceptance on, admission drops (`enforce`) or flags (`warn`) a
matched file in an area of another owner that has no acceptance; a workflow
with no file left is skipped. The settings inherit from the organization.
See [Reporting and Ownership](./docs/reporting-and-ownership.md).

## Retries, Re-runs, and Manual Runs

A retry or a re-run keeps the authority of the original run: the source and
CI SHAs, the CI origin, the execution profile, the policy revision, rule and
approval, the node authority, the ref classes, and the secret grants. It can
change only the shifts: the CPU request and limit, the memory limit, the
ephemeral storage request and limit, and the timeout (owner decisions D5,
D18, D25). It refuses every other change. A shift needs only the retry
permission. The `rerun_shift_limits` trust setting (organization, with a
project override) and the execution profile ceilings bound the values. Each
job records its shifts (`jobs.shifts`) and its actor. `internal/runshifts`
parses and applies shifts for every channel; `internal/jobcontrol` does the
retry and the re-run.

A change that is not a shift needs a new run. A principal with permission
starts a new evaluated run on any branch, tag, or pull request head
(`POST /api/v1/projects/{id}/runs`, `reactorcide run`, CSIL `start-run`). The
coordinator resolves the commit through the VCS API, records a `manual`
event with the principal as its actor, and evaluates it like a webhook
delivery of that ref, with the normal run gate and policy. See
[Runtime Behavior](./docs/runtime-behavior.md#retries-re-runs-and-shifts) and
[Events](./docs/events-and-schedules.md#manual-runs-on-a-ref).

## Event Model

The coordinator records each webhook delivery, schedule fire, and manual eval
submit as an event (`ci_events`) before it starts work. The delivery ID makes a
redelivery a no-op. The `duplicate_events` setting controls a new delivery of
the same event. Each event starts one eval job named `Eval event ...`. The eval
job reports a decision with structured reasons for each workflow file. The
coordinator stores the decisions and creates the workflows that must run.

Schedules are coordinator records that point to a workflow definition on a
branch. One replica runs the scheduler under a database lease. A unique fire
row makes each fire occur one time.

A draft pull request follows the `draft_prs` setting (`run`, `skip`,
`skip_when_shedding`). GitHub `ready_for_review` is a `pull_request_updated`
event with `draft=false`. A skipped event has the status `skipped` and a skip
reason, and "Reactorcide Checks" is success with the reason.

Overload protection does not give one tenant more capacity than another
(owner decision D10). A shed level (`normal`, `degraded`, `critical`) is
global and for each organization; the higher level is in force. Each level
maps to actions: skip drafts, pause schedules, lower pull request priority,
skip advisory workflows, and refuse manual runs from non-administrators
(`internal/shedding`). Token bucket rate limits in each replica limit webhook
deliveries for each project and REST requests for each principal. A webhook
over the limit gets HTTP 429 and is recorded as `rate_limited`, so a
redelivery can evaluate it.

See [Events, Checks, and Schedules](./docs/events-and-schedules.md).

### Outbound events

The coordinator sends phase events to outbound sinks: `node.pre_start`,
`node.started`, `node.post_complete`, `node.skipped`, `event.evaluated`,
`event.admitted`, `event.not_admitted`, and `workflow.completed`. Database
triggers write each event to `event_outbox` in the transaction of its status
change. One replica (lease `outbound-events`) dispatches the outbox to the
matching subscriptions and submits one Corndogs task for each delivery on an
internal queue that no worker reads. Only one delivery of a workflow is in
flight for a subscription, so the events of a workflow arrive in order. A
delivery loop on each replica calls the sink. The first sink is a signed
HTTPS webhook. Delivery is at least once, with retries, dead letter, and
replay. A sink cannot delay or stop a run.

See [Outbound Events](./docs/outbound-events.md).

### Failure diagnosis

Reactorcide gives primitives for an external diagnosis tool. It does not
diagnose failures itself. The tool is a service principal. A subscription
sends it `node.post_complete` events with a failure class (`job`, `oom`,
`timeout`). The tool reads logs and artifacts with its own grants and posts
one diagnosis entry for each job, node, or workflow (`diagnosis:write`, with
a role in the scope). A new post of the same principal replaces its entry.
The job and workflow pages and the rolling pull request comment show the
entries with the name and the kind of the author. The comment form is
sanitized: no HTML, no images, no mentions that notify. Log masking replaces
literal values only, so a job that a tool reads should get its secrets
through the egress proxy.

See [Failure Diagnosis](./docs/diagnosis.md).

### Observability

Reactorcide records run data in two stores. Prometheus gets low-cardinality
metrics for alerts: job duration and queue wait histograms by queue, lane,
failure class, owner group (bounded), and principal kind; eval latency
(webhook to first commit status); infrastructure failures by class. No metric
has a workflow, project, or job label. PostgreSQL gets hourly and daily
rollups for each org, project, workflow security ID, owner group, lane, ref
class bucket, and actor kind: runs, failures by class, p50 and p95 duration
and queue wait, and CPU and memory hours from the resource summaries. One
replica (lease `observability-rollups`) computes the rollups incrementally
after a watermark and does a backfill on its first start. The same loop
evaluates the pipeline owner alert rules (pass rate on the default branch,
p95 duration against a baseline) with hysteresis, and sends `alert.firing`
and `alert.resolved` through the outbound event outbox. Only the classes
`job`, `timeout`, and `oom` count for a pipeline owner. The CI owner owns the
`infra.*` classes, queue wait, eval latency, and availability: SLOs with error
budgets for each lane, and example Prometheus rules. The Insights UI page
shows the pipeline, infrastructure, and usage dashboards, with the visibility
of each project.

See [Observability](./docs/observability.md).

## VCS Model

GitHub is the operational webhook provider. The GitLab client can parse
payloads and call provider APIs, but it does not set generic Reactorcide events.
GitLab webhook events do not start jobs in the current implementation.

A complete provider adapter:

- Validates its webhook.
- Converts provider events to Reactorcide event types.
- Supplies repository, push, and pull-request data.
- Updates commit status.
- Creates or updates pull-request comments.

Supported generic events are:

- `push`
- `pull_request_opened`
- `pull_request_updated`
- `pull_request_merged`
- `pull_request_closed`
- `tag_created`
- `schedule` (from a coordinator schedule, not from a provider)

Projects select accepted events and target branches. A project can use
provider-specific secret references for webhook validation and API access.

A deployment can hold a pool of many GitHub Apps. Each installation belongs
to one App, and each repository binds to one installation. A project whose
repository is in an installation uses installation tokens and posts
`Reactorcide Checks` and `Reactorcide Reviews` as check runs. For these
projects, check runs supersede decision D4 of `EVENT_CI_PLAN.md` (Commit
Status API). A project with no App keeps its tokens and the Commit Status API.
Installation tokens are system credentials. They are never in a job
environment. A checkout token is only for a job of the project of the
binding, and an account token is only for an organization with a bound
project on that account (or the account in its VCS owner allowlist). An App
delivery is trusted only for a repository bound to an installation of that
App, with the same GitHub repository ID. A project that self-service
registration makes is disabled until an administrator approves it, unless an
account mapping or open registration of the App applies. See
[GitHub App](./docs/github-app.md).

See [Connect a VCS Repository](./docs/vcs-setup.md).

## Secret Model

The local CLI can store encrypted secrets for local jobs. A deployed
coordinator stores encrypted secret values in PostgreSQL.

Job files contain references such as:

```text
${secret:path:key}
```

They do not contain secret values.

The coordinator authorizes a remote job with:

- Its built-in job or project scope.
- An explicit secret grant.

VCS credentials are system credentials. The coordinator and worker use them
for webhook validation, checkout, status, and comments. They are not normal
job environment variables.

A secret grant delivers its secrets as environment values (`delivery: env`,
the default) or through the egress proxy (`delivery: proxy`). With `proxy`,
the coordinator puts a random placeholder in the job environment and the
real value in `Lease.egress`. The worker registers the job with the egress
proxy, which puts the value only in the named header and only for the
grant hosts. Egress control (per organization, project, or execution
profile, off by default) sends all job egress through the proxy. See
[Egress Proxy](./docs/egress-proxy.md).

Coordinator API clients, workers, workflow trigger clients, the webapp, VCS API
clients, and OCI VM image clients send credentials and secret values only on an
encrypted, peer-authenticated transport. An operator can permit an insecure
development connection with an explicit command-line option. No environment
variable can enable this exception. This rule applies to HTTP and to other RPC
carriers.

See [Secrets](./docs/secrets.md) and [VCS Credentials and Secret
Grants](./docs/vcs-credentials-and-secret-grants.md).

## Worker Selection and Resources

A queue can require worker characteristics. A worker is eligible when all
required values match. Standard values include `os` and `arch`. Operators can
also define typed custom values.

Job resources and worker characteristics are separate:

- Characteristics select an eligible worker.
- Resources set CPU and memory requests or limits for the job.

`region`, `zone`, and `cloud` are standard worker characteristics. A job
that does not require them matches a worker in any location. The control
plane runs in one region; jobs run in any region or cloud.

A worker admits work in one of two modes. In `concurrency` mode (the
default) it holds a fixed number of leases. In `resources` mode it reports
its capacity and headroom on each `RequestJob` call, and the coordinator
offers only a job whose CPU request and memory limit fit. The coordinator
selects the queues whose head job fits; a starving large job makes the
worker reserve room for it. Priority stays the only capacity mechanism.

The priority of a job comes from **lanes** and **group shifts** (owner
decision D10). A lane is a coordinator rule on trusted facts of the job: its
ref classes, its event type and source, the draft state, the job kind (eval
or work), and retry. The first lane that matches gives the base. The built-in
lanes order release work > evals > default branch and manual work > ready pull
requests > draft pull requests > schedules > backfill. An organization or a
project can replace or add lanes. The largest shift of the groups of the
event actor (and of the owner group of the workflow) adds -50 to 50. A
requested priority from a job specification moves the job at most 50 inside
its lane. A shed level with `lower_pr_priority` moves pull request work one
lane down. Every submit path calls `internal/priority.Assign`, and each job
records the lane and a reason `{kind: "priority", lane, base, shifts, final}`.
A retry keeps the priority of its original job; the groups of the person who
retries do not apply. See [Priority and Lanes](./docs/priority.md).

An image alias gives a stable name to a pinned image reference, in an
organization or a project. Each job whose image is an alias (VM or
container), and each VM job with a pinned reference, gets the job
characteristic `image_digest`. The coordinator sends each worker an image
set: the images of the aliases that the worker can serve. The worker pulls
them and sends the ready digests in its `image_digest` characteristic. Thus
only a worker that has the image claims the job. A worker does not claim
work until its startup images are ready (the startup gate). When an alias
gets a new pin, the new pin is pending until each online worker that serves
the alias has the image. Then new jobs use it, and the old image drains. A
Kubernetes worker pre-seeds its image set on the nodes with a DaemonSet.
See [Image Aliases and Worker Image Sets](./docs/image-aliases.md) and
[Workers](./docs/workers.md#image-sets-and-pre-seed).

The coordinator exports the backlog of each queue (count, oldest wait, CPU
and memory sums) as Prometheus gauges and as `GET /api/v1/backlog`. Each
cluster scales with its own autoscaler from that signal. See
[Worker Operation](./docs/workers.md).

## Deployment Shapes

### Developer workstation

Use the development compose stack for the full service set. Use `run-local`
when you only need local job execution.

### Single VM

The VM deployment uses Compose. It starts PostgreSQL, Corndogs, the
coordinator, and one worker. The worker uses the host Docker or containerd
runtime.

### Kubernetes

The Helm chart starts the coordinator and worker deployments. The worker uses
the Kubernetes backend and creates one Kubernetes Job for each Reactorcide
job. With `REACTORCIDE_K8S_KUBECONFIG`, a worker creates the Jobs in a remote
cluster. The chart can install PostgreSQL and Corndogs for evaluation. Use managed
PostgreSQL and durable S3 storage for production.

See [Installation and Deployment](./docs/getting-started.md).

## Trust Boundaries

Operators must preserve these boundaries:

- Only the coordinator can access database master keys.
- Only the coordinator connects to control-plane storage services.
- Workers use authenticated coordinator sessions.
- A webhook must have a project or deployment signing secret.
- A VCS API token must have only the permissions that Reactorcide needs.
- Fork source remains separate from trusted CI definitions.
- Secret grants must be narrow.
- A job with a host runtime socket has strong control of that runtime.
- A `root` job has more risk than a non-root job.

## Organizations and Authentication

An organization owns each project, workflow, job, secret, worker class, and
queue. The database uses an organization UUID. The API and CLI use the stable
organization name. A suspended organization cannot submit new work. A disabled
organization also cannot submit new work. Other operations use token
capabilities and roles.

API credentials have one subject type: instance, service, user, or job. A
credential has an organization selector and a control-plane capability set.
A user credential also uses the current user roles. Its effective authority is
the intersection of its token limits and its current roles. A job credential
can submit triggers only for its parent job.

A browser session and an API token are equal clients of the UI service and
of the event stream. Each operation does the same role check for both. For
a token, the coordinator also checks the organization list and the
capability of the operation. A read accepts the read capability (for
example `organizations:read` or `workers:read`). A change needs the manage
capability. On a no-login instance (`REACTORCIDE_UI_AUTH_MODE=none`), a
service principal token issued with no capability list gets all
capabilities and all organizations. Do not use this mode in production. See
[Sessions and API tokens](./docs/ui-auth.md#sessions-and-api-tokens).

A principal is anything that can act: a human, a service, or the system
principal "Reactorcide". A principal has identities: a Reactorcide user, VCS
logins, LinkKeys identities, API tokens, a VCSStar identity, and federated
subjects. The principal resolver maps one identity to the principal and all
linked identities. Approvals, CI policy, and required reviews match subjects
through the resolver, so the identities of one person agree. A subject list
can be `*` (anyone); an omitted list never means anyone.

The smallest role scope is one workflow of a project (`scope_type
workflow`, role `owner`). A workflow owner is a workflow role holder or a
member of the trusted owner group of the workflow. An owner can cancel,
retry, re-run, quarantine, and propose required-set changes for that one
workflow only. A workflow role never widens a project or organization scope,
and tokens intersect with it as usual. The approver subject `workflow_owner`
in `override_approvers` and `head_ci` approval lists stands for these owners,
per workflow. See [Organizations and CI policy](./docs/organizations-and-ci-policy.md#workflow-owners-and-workflow-roles).

A service principal belongs to one organization. It gets authority from
roles and groups, the same as a user. A token issued for it intersects its
capabilities with those roles. Tokens made before principals keep their
capabilities alone, so the admin token works with no logins. Every job,
workflow, event, approval, and audit record stores its actor principal. A
schedule fire has the system principal, which has no privileges. See
[Principals](./docs/principals.md).

A group can link to one or more VCS teams. A sync loop (one coordinator
replica, under the `group-vcs-sync` lease) reads each team from the VCS and
stores its members as synced group members. A `team` or `membership` webhook
makes the sync run soon. The VCS is the authority: the webhook payload is only
a hint. A sync never removes a manual member. Membership changes go to
`group_membership_history` for attribution over time.

A service principal can authenticate through inbound federation. An org admin
registers a trusted OIDC issuer and claim mappings. A workload exchanges its
external token at `POST /api/v1/federation/token` for a short-lived token of
the mapped service principal. The coordinator verifies the signature with the
cached JWKS of the issuer (HTTPS only), the issuer, the audience, the times,
and exactly one matching mapping. The issued token intersects the mapping
capabilities with the roles of the principal. See
[Inbound Federation](./docs/federation.md).

## OIDC Issuer for Jobs

When `REACTORCIDE_OIDC_ISSUER_URL` is set, the coordinator is an OIDC issuer.
It serves a public discovery document and JWKS under the issuer path. A
running job uses its job token to request a token for one audience
(`runnerlib oidc-token`). The coordinator takes every claim from its own
records: organization, project, repository, workflow security ID, node, ref,
SHA, event type, ref classes, actor principal, CI origin, execution profile,
and policy revision. A pull request job always has the ref
`refs/pull/N/head`, so a fork cannot get the claims of a production job.
A manual event has the event type `manual`. A submitter states the ref of a
manual eval submit or an API submit, so that ref goes only to
`submitted_ref` and the `ref` claim stays empty.
Signing keys stay in the coordinator, encrypted under the master key. A
rotation publishes the new key before it signs, and keeps the old key in the
JWKS for an overlap. An organization or project can turn the issuer off. See
[OIDC Tokens for Jobs](./docs/oidc.md).

## CI Admission and Reports

CI policy applies when the project stores a policy. A pull request of a
project with no stored policy in an organization with the `secure` defaults
profile gets the built-in policy: CI from the base, and head CI only through
`override-ci` or a stored `head_ci` rule. New organizations get the `secure`
profile (run gate `outside_contributors`, `dynamic_jobs: deny`, built-in
policy). Organizations from before migration 000055 get `legacy`. Without a
stored or built-in policy, the
eval runs the project's configured CI source, or the CI code of the event's
own commit (a fork pull request still uses the base). With a stored policy,
for a pull request or a push that changes `.reactorcide/`, the evaluator
prepares exact base and head views. For a push, the base is the `before`
commit. The
coordinator gives it an exact policy snapshot, revision, and active approval
snapshot. Triggered jobs inherit these values. Runnerlib does not evaluate
this policy. The `isolated` checkout mode uses separate Git clones.
The `shared` mode uses one non-shallow, blob-filtered Git object store and
stages only `.reactorcide` files for the two CI views. Both modes keep the
trust boundary.

Each workflow has a stable security ID. The admission decision records the CI
origin, exact SHA, profile, worker class, policy revision, rule, and approval.

A head-CI policy rule can also name trusted base nodes. After evaluation, the
coordinator verifies the policy snapshot and loads active approvals. It selects
the allowed workflow candidate and applies node authority. Trusted base nodes
use their workflow specification and executable CI content from the exact base
CI SHA. Other nodes use head CI with the untrusted profile. The coordinator
stores the effective authority on each workflow node and fails closed on any
mismatch. Retries replay the recorded node authority. See
[Organizations and Coordinator CI Policy](./docs/organizations-and-ci-policy.md).

The coordinator stores VCS report entries as structured database records. One
reconciler owns the shared pull-request comment. It uses a database advisory
lock and revision counters. A VCS comment error does not change a workflow
result.

The coordinator processes each workflow from an evaluation result as an
independent transaction. It publishes the workflow VCS status after the
transaction commits. A profile refusal creates a durable `not_admitted`
workflow outcome with the exact reason. It does not stop sibling workflows.
The event status `Reactorcide Checks` fails for a refused workflow and for a
refusal by a stored policy. When the required set of the project is on, a
refused advisory workflow does not fail it.

## Source of Truth

Use these documents with this design:

- [Documentation Index](./docs/README.md)
- [Runtime Behavior](./docs/runtime-behavior.md)
- [Worker Operation](./docs/workers.md)
- [Priority and Lanes](./docs/priority.md)
- [Observability](./docs/observability.md)
- [Workflow Design](./docs/workflow-design.md)
- [Job Definition Reference](./docs/job-definitions.md)
- [Organizations and Coordinator CI Policy](./docs/organizations-and-ci-policy.md)
- [GitHub App](./docs/github-app.md)
- [VCS Credentials and Secret Grants](./docs/vcs-credentials-and-secret-grants.md)
