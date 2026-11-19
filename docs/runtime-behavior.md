# Runtime Behavior

Reactorcide treats local execution as a first-class path. The same job definition should work when run locally, on a VM worker, or as a Kubernetes Job, with only the execution substrate changing.

## Execution Modes

| Mode | Runtime | Code preparation | Logs |
|---|---|---|---|
| `run-local` | Docker or containerd/nerdctl | Mounts the current repository, separate local source and CI directories, or a `--code-url` checkout | Streams directly from the local container |
| Host worker (Linux host or VM) | Docker or containerd/nerdctl | Worker prepares a fresh workspace and source checkout | Streams from the runtime through the worker |
| Native VM worker (macOS, Windows) | `vm` backend: one ephemeral guest VM for each job | The in-guest daemon prepares the workspace and source checkout | Streams from the guest through the worker |
| Kubernetes worker | Kubernetes Jobs | Job pod prepares a fresh workspace and source checkout | Worker streams pod logs through the Kubernetes API |

Tooling should prefer nerdctl/containerd when available and fall back to Docker. The `./tools` helper follows that order.

## CodeDir, JobDir, and WorkingDir

Jobs have three related paths:

| Field | Default | Meaning |
|---|---|---|
| `code_dir` | `/job/src` | Where source code is mounted or checked out inside the job container |
| `job_dir` | `code_dir` | The directory runnerlib treats as the job working directory |
| `working_dir` | `job_dir` | Raw container process working directory override |

For most jobs, leave these unset and use `/job/src`. Set `code_dir` and `job_dir` only when a job image or script expects a different layout.

`run-local` finds the repository that contains the current directory and
mounts it at `code_dir` by default. It also mounts that repository read-only
at `/job/ci`. Use `--source-dir` and `--ci-dir` to select different local
trees. This separation lets a job test
one checkout while it runs trusted control code from another checkout. Use
`--code-url` and `--code-ref` only when Reactorcide must clone another source.

Runnerlib reads `REACTORCIDE_CODE_DIR` and `REACTORCIDE_JOB_DIR`, so jobs should prefer those variables over hard-coded paths when practical.

## Run Identity

Container identity is explicit and independent from runtime capabilities.

```yaml
run_as:
  user: runner   # default on deployed workers; also accepts root or uid[:gid]
```

`run_as.user` applies to deployed workers and is also used by `run-local` as a fallback. Accepted values are:

| Value | Meaning |
|---|---|
| `runner` | Image runner uid `1001:1001` |
| `root` | uid `0:0` |
| `uid[:gid]` | Numeric uid and optional gid |

`host` is intentionally local-only and belongs under `run_local.user`.

`run_as.administrator` is an optional boolean. It applies only to Windows VM
jobs. When the guest image has no job account, the in-guest daemon creates
the account. This value sets whether the new account is in the
Administrators group. When you do not set it, the value is `true`. The daemon
does not change an account that the image has. Container backends (Docker,
containerd, Kubernetes) ignore it. A value that is not `true` or `false` is
an error when the job is parsed.

```yaml
run_as:
  administrator: false   # Windows VM jobs only: create a standard account
```

A job spec overlay merges `run_as` field by field. An overlay that sets only
`run_as.administrator` keeps the `run_as.user` of the base spec, and the
reverse.

```yaml
run_local:
  user: host     # default for run-local
```

Run-local precedence, highest first:

1. `--user`
2. `--as-runner`
3. `run_local.user`
4. `run_local.as_runner`
5. `run_as.user`
6. Host uid

`run-local` defaults to the host uid because it bind-mounts the user's working tree and should be able to write files as that user. Use `run_as.user: root` or `--user root` when a local job must run as root. Use `run_local.user: runner` or `--as-runner` when testing worker uid parity.

## Command Shell

Each job command goes through the shell prefix, also a single-line command.
The prefix is the first value that is set:

1. `command_prefix` of the job spec file (`run-local` and `submit` only).
2. `REACTORCIDE_JOB_SHELLCMD` in the job environment.
3. The default for the `os` characteristic: `sh -c`, or
   `powershell.exe -NoProfile -NonInteractive -Command` for `os: windows`.

Set the prefix to `none` to run the command words directly, with no shell.
An image with no shell (for example distroless) must do this. The worker
does not add the prefix to a command that starts with a shell (`sh -c`,
`bash -c`, `powershell.exe`, `pwsh`, `cmd /c`, and similar). See
[Command Execution](./job-definitions.md#command-execution).

## Capabilities

Capabilities describe runtime services, not the uid the job runs as.

| Capability | Meaning |
|---|---|
| `docker` | Provide Docker-style container access for jobs that run nested containers |
| `builder` | Provide a BuildKit sidecar and `BUILDKIT_HOST` for image builds. The sidecar uses the cache namespace of the job. The operator can select a rootless sidecar. See [Builds, Caches, and Job Settings](./builds-and-caches.md). |
| `gpu` | Reserved for GPU access |

If a job with `docker` or `builder` needs root inside its container, declare that explicitly:

```yaml
capabilities:
  - builder
run_as:
  user: root
```

## Job Facts and Defaults

The coordinator sets reserved values in each job environment. A job cannot
replace them. The cache values are `REACTORCIDE_CACHE_NAMESPACE`,
`REACTORCIDE_CACHE_WRITABLE`, and `REACTORCIDE_ARTIFACT_NAMESPACE`. The ref
classes are in `REACTORCIDE_REF_CLASSES`.

`run-local` sets the cache values from the default execution profile of the
local context (`reactorcide local-context`), or from the built-in `standard`
profile when you use no context. The namespaces of the context apply. A local
run has no ref classes. A local run is not a trusted build, so
`REACTORCIDE_CACHE_WRITABLE` is `false`. Use `--cache-writable` to set it to
`true`. `run-local` sets these values after the job variables, so a job file
or an overlay cannot replace them.

`run-local` stops a job at its `timeout_seconds`, as the workers do. It sends
the stop signal, waits 10 seconds, and then kills the container. The exit code
is 124. Use `--ignore-timeout` to let a local job run longer (for example,
when you debug it).

The coordinator then adds the non-secret environment defaults of the
project, the worker class, and the organization, for keys that the job does
not set. Precedence: job > project > worker class > organization. See
[Builds, Caches, and Job Settings](./builds-and-caches.md).

The worker also sets `REACTORCIDE_ARTIFACTS_DIR` (`/job/artifacts`) when the
coordinator accepts artifacts, and `REACTORCIDE_QUARANTINED_TESTS_FILE`
(`/job/quarantined-tests.json`) with the quarantined tests of the project.
The VM runner sets neither. See
[Artifacts and Test Results](./artifacts-and-tests.md).

The image contract (uid 1001, `HOME=/home/runner`, runnerlib optional, webi)
is in [Runner Images](./runner-images.md).

## Kubernetes Notes

The Kubernetes runner creates Kubernetes Jobs and streams their logs. It honors `working_dir`, prepares the configured `code_dir` and `job_dir`, and can be configured with:

- `REACTORCIDE_K8S_JOB_NAMESPACE`
- `REACTORCIDE_K8S_JOB_SERVICE_ACCOUNT`
- `REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS`
- `REACTORCIDE_K8S_JOB_ALLOWED_IMAGE_PULL_SECRETS`
- `REACTORCIDE_DIND_IMAGE`
- `REACTORCIDE_K8S_JOB_RESOURCE_SCOPE` (`auto`, `pod`, or `container`)
- `REACTORCIDE_K8S_JOB_STARTUP_TIMEOUT` and `REACTORCIDE_K8S_JOB_AUTOSCALING`
- `REACTORCIDE_K8S_JOB_NODE_SELECTOR`, `REACTORCIDE_K8S_JOB_TOLERATIONS`, and
  `REACTORCIDE_K8S_JOB_AFFINITY`
- `REACTORCIDE_K8S_KUBECONFIG` and `REACTORCIDE_K8S_CONTEXT`

Helm values expose the same settings under the worker configuration.

The runner puts job CPU and memory in the pod's `spec.resources` when the
cluster supports pod-level resources. The job container and its buildkit or
DinD sidecar then share one budget. See "Resources" in `docs/workers.md`.

The runner puts image pull secret references at pod level, in
`Job.spec.template.spec.imagePullSecrets`. They apply to the main container,
init containers, and sidecars. The final list is the operator's global list
(`REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS`) followed by the job's approved
`image_pull_secrets` names, with duplicates removed and order preserved. The
worker validates each job-level name against
`REACTORCIDE_K8S_JOB_ALLOWED_IMAGE_PULL_SECRETS` (plus the global list) and
rejects the job before Kubernetes Job creation when a name is not approved.
The worker never reads Secret data. Docker, containerd, and local execution
keep the field but do not read Kubernetes Secrets.

The Kubernetes job workspace is an `emptyDir`. The worker host cannot read
this volume. The runner adds a workflow output reader container to each
workflow job pod.
After the main container stops, the worker reads
`/job/workflow-output.json` from this container. The worker limits the file to
1 MiB and sends it through the authenticated worker result call. The worker
does not send this file through the job log stream. The worker Role must allow
`create` on `pods/exec`.

## Failure Classes

Each job that does not complete gets a failure class (`failure_class`). The
class tells who must act on the failure. The REST job response, the UI, the
VCS status, and the PR comment show the class and its owner. The Jobs page
filters on one class (CSIL `list-jobs` with `failure_class`).

| Class | Cause | Owner | Automatic retry |
|---|---|---|---|
| `job` | The job command exited with a non-zero code. A refused secret or checkout credential is also `job`. | Pipeline owner | No |
| `timeout` | The job ran longer than its `timeout_seconds`. The job status is `timeout`. | Pipeline owner | No |
| `oom` | The runtime stopped the container because it used more memory than its limit. | Pipeline owner (resource request) | No |
| `infra.image_pull` | The runtime could not pull the job image. | Pipeline owner if the image is missing. CI owner if the registry is down. | No |
| `infra.scheduling` | Kubernetes could not schedule the pod, or the main container did not start before the startup timeout. | CI owner | No |
| `infra.worker_lost` | No worker held the job, and the lost-job reconciler found it. | CI owner | Yes |
| `infra.platform` | The worker, the runtime, the coordinator, or Corndogs failed around the job. | CI owner | Only when the main container never started |
| `cancelled` | A user or a directive cancelled or killed the job. | None | No |

`failure_detail` gives a short reason, for example `OOMKilled`,
`ErrImagePull`, or `Evicted`.

### How the class is set

- The runner that knows the cause sets the class. Docker and containerd
  read the container state for an OOM kill. Kubernetes reads the
  termination reason `OOMKilled`, an evicted pod, and the pod startup
  reasons. Image pull errors at spawn are `infra.image_pull`. A runtime
  error at spawn or during the wait is `infra.platform`.
- The containerd runner pulls the image before `nerdctl run`. Thus an image
  failure is not shown as a non-zero exit of the job.
- The VM runner reports no OOM. A non-zero guest exit is `job`.
- The worker enforces `timeout_seconds`. The time starts when the main
  container starts, so time in a scheduling queue does not count. At the
  timeout, the worker stops the job with the cancel grace period and
  reports the status `timeout`. When the main container exits 0 before the
  worker stops it, the completion wins: the job is `completed`.
- Kubernetes: the Job `activeDeadlineSeconds` is `timeout_seconds` plus the
  startup timeout plus 300 seconds. The Job deadline counts from the Job
  start (scheduling and image pull included). It is only a backstop when the
  worker is lost, so the worker timeout fires first. When the Job controller
  fails the Job with the reason `DeadlineExceeded`, the class is `timeout`.
- A failure after the main container ran is never a retryable class. For
  example, when Kubernetes cannot copy the workflow output after an exit 0,
  the job is `failed` with `infra.platform`, and the worker reports that the
  main container started. The coordinator does not retry it.
- A worker older than failure classes sends no class. The coordinator then
  derives the class from the status, the exit code, and the error text. The
  coordinator does not retry a derived class.
- Migration `000047` adds the class to existing jobs only when the stored
  fields prove the cause. Other old jobs keep no class.

### Automatic infrastructure retries

The coordinator puts a job back in the queue after an `infra.platform` or
`infra.worker_lost` failure while the retry budget lasts. An `infra.platform`
failure is retried only when the worker proves that the main container never
started (`main_started: false` in the worker result). A retry runs the job
command again, so an eviction or a lost runtime after the start must not
cause one. Each retry adds 1
to `retry_count`, writes the reason to `last_error`, and writes a
`job.infra_retry` audit event.

| Setting | Where | Values | Default |
|---|---|---|---|
| `infra_retries` | Organization (`PUT /api/v1/organizations/{name}`) | 0 to 5 | 1 |
| `infra_retries` | Project (`PUT /api/v1/projects/{id}`) | 0 to 5, or `"inherit"` (or `null`) | `inherit` |

The organization update also sets `display_name` and `is_private`. Send
their current values with `infra_retries`.

UI: the "Infra retries" column of the organization event defaults on the
Admin page (CSIL `update-org`, a global admin), and the project Execution
page (CSIL `update-project` with `infra_retries` or
`infra_retries_inherit`, a project owner).

- `infra.platform` gets `infra_retries` retries.
- `infra.worker_lost` gets at least 2 retries (the fixed budget of the
  lost-job reconciler before this setting). A larger `infra_retries` value
  increases it.
- The coordinator never retries `job`, `timeout`, `oom`, `cancelled`,
  `infra.image_pull`, or `infra.scheduling`. A retry gives the same result,
  or the cause is in the pipeline.
- `main_started` of the worker result:
  - `false`: the worker prepared the job and failed before `SpawnJob`, or
    `SpawnJob` failed. On Kubernetes also: the pod exists and its main
    container never ran. The worker then stops the pod (grace 0) and reads
    the pod again before it reports `false`.
  - `true`: the main container started. Docker, containerd, and the VM
    runner return from `SpawnJob` only after the start.
  - Absent: the worker cannot prove it (for example, no pod is left, or the
    Kubernetes API failed), or the worker is older than this field. The
    coordinator does not retry.
- An evicted Kubernetes pod is `infra.platform` with the detail `Evicted`.
  It is retried only when the eviction came before the main container
  started.
- `infra.worker_lost` comes from the lost-job reconciler. It does not know
  if the main container started.
- A retried job can run its command again. Make deploy jobs safe to run
  again, or set `infra_retries` to 0 for their project.

## Retries, Re-runs, and Shifts

A retry or a re-run runs work again. It keeps the authority of the original
run. It can change only the shifts (owner decisions D5, D18, D25).

### Operations

| Operation | What it does | Allowed state |
|---|---|---|
| Job retry | Runs one job again as a new job in the same workflow node. | `failed`, `cancelled`, `timeout` |
| Workflow retry | Runs a workflow again as a new instance. The old instance stays. | `failed`, `cancelled` |
| Retry unsuccessful | Runs the failed and cancelled jobs of a workflow again in the same instance. | `failed`, `cancelled` |
| Workflow re-run | Runs a finished workflow again as a new instance. The old instance stays. | `completed`, `failed`, `cancelled` |
| `/reactorcide retry [SHIFTS]` | Retry unsuccessful, for each workflow of the newest evaluated event of the head. | |
| `/reactorcide rerun` | Evaluates the head again, as a push does. This is a new evaluation, with the policy in force now. | open pull request |
| `/reactorcide rerun SHIFTS` | Workflow re-run with the shifts, for each finished workflow of the newest evaluated event of the head. | open pull request |

### Shifts

A shift is one of these values. Each value replaces the value of the
original job. An empty value keeps the original value.

| Key | Value | Example |
|---|---|---|
| `cpu` (alias `cpu_limit`) | CPU limit | `4`, `2500m` |
| `cpu_request` | CPU request | `2` |
| `memory` (alias `memory_limit`) | Memory limit | `8Gi` |
| `storage` (alias `storage_limit`) | Ephemeral storage limit | `20Gi` |
| `storage_request` | Ephemeral storage request | `10Gi` |
| `timeout` | Timeout: a duration or whole seconds | `2h`, `90m`, `7200` |

- The values use the same grammar as the `resources` block of a job (see
  [Job Definitions](./job-definitions.md)).
- A request must not be more than its limit after the shift.
- The largest timeout is 7 days.
- Every channel uses the same keys: REST (`{"shifts": {...}}`), CSIL
  (`RunShifts`), the CLI (`--cpu`, `--cpu-request`, `--memory`,
  `--storage`, `--storage-request`, `--timeout`), and pull request comments
  (`key=value` words, for example `/reactorcide retry memory=8Gi timeout=2h`).
- A shift needs no extra permission. The retry permission is sufficient:
  `jobs:retry` or `workflows:control` for a token, the project owner or an
  administrator for a person, and `run_approvers` for a pull request command.

### Other changes are refused

A retry or a re-run refuses every change that is not a shift. Examples are the
image, the command, the environment, the capabilities, and the job
configuration. The REST body can have only the `shifts` object. Another field
gives `400` with the error `rerun_change_refused`. A comment with a word that
is not a shift does nothing, and the coordinator writes a
`ci_approval.refused` audit event. To make another change, submit a new run
(see [Manual Runs on a Ref](./events-and-schedules.md#manual-runs-on-a-ref)).
A new run gets its own evaluation and its own authority.

### Authority stays the same

The new run copies these values from the original run. A shift does not
change them:

- the source SHA and the CI SHA, the CI repository, and the CI origin
- the execution profile and the worker class
- the policy revision, the policy rule, and the approval
- the recorded node authority of each workflow node
- the ref classes
- the secret grants (they follow from the project, the profile, and the ref
  classes, which stay the same)

A retry or a re-run does not evaluate a newer policy. The execution profile
ceilings (`timeout_ceiling_seconds`, `resource_ceilings`) still apply to the
shifted values. A shift that is more than a ceiling is refused.

### Records

- `jobs.shifts` holds the shifts of a job. A retry of a job that has shifts
  keeps them, and new shifts replace them key by key.
- `workflow_instances.shifts` holds the shifts of a workflow re-run. Each job
  of the new instance gets them.
- The actor of the new run is the principal that asked for it (see
  [Principals](./principals.md)).
- The workflow event of a retried node says `job retried with shifts ...`.
- A retry or a re-run of a workflow keeps the CI event of the original, so
  its jobs keep the event (`jobs.event_id`), and records the first instance
  of its chain in `workflow_instances.rerun_of_workflow_id`. A root re-run
  instance keeps the event in `workflow_instances.rerun_event_id`, and its
  `event_id` is empty. Thus the unique index on `(event_id,
  workflow_security_id)` of root workflows stays the same as in v0.12.13,
  and older coordinators can write to the schema. The
  `Reactorcide Checks` status of the event counts only the newest instance
  of each chain. Thus a re-run updates the status, and a second re-run
  starts from the newest instances. A retry of a child workflow stays a
  child of the same parent.
- The worker class conditions (`ref_classes_any`, `protected`) apply again
  to a retried job. A worker class that now refuses the job gives `403`
  with the error `worker_class_refused`.

### Shift limits

The `rerun_shift_limits` setting bounds the shift values. It is part of the
CI trust settings (`reactorcide trust set`, CSIL `put-ci-trust-settings`).

```yaml
rerun_shift_limits:
  max_cpu: "16"          # bounds cpu and cpu_request
  max_memory: 64Gi
  max_storage: 200Gi     # bounds storage and storage_request
  max_timeout_seconds: 14400
```

- A key with no setting has a default bound: `max_cpu` 16 cores,
  `max_memory` `64Gi`, `max_storage` `200Gi`, and `max_timeout_seconds`
  86400 (24 hours). To permit more, set a larger value for the key. The
  worker class, the queue, and the execution profile also limit the job.
- A project value replaces the organization value key by key. A key that the
  project does not set uses the organization value.
- Only an organization administrator can raise a limit above the
  organization value for a project.
- A shift over a limit gives `400` with the error `shift_over_limit`.

### Ephemeral storage

`resources.storage.request` and `resources.storage.limit` set the ephemeral
(scratch) storage of the job container:

```yaml
resources:
  storage:
    request: 10Gi
    limit: 20Gi
```

- Kubernetes sets them as the `ephemeral-storage` request and limit of the
  job container. Pod-level resources do not include them.
- Docker, containerd, and the VM runner do not enforce them. They are
  best-effort values on these runners.
- A job with no storage values has no storage request or limit, as before.

### Release note: timeout enforcement

Docker, containerd, and VM workers now enforce `timeout_seconds`. Before
this release, only Kubernetes stopped a job at its timeout (with the Job
deadline). A job on these workers that runs longer than its declared
`timeout_seconds` now ends with the status `timeout`. Before you upgrade,
make sure that `timeout_seconds` of each job is large enough. A production
check found 7 of 7558 finished jobs that ran longer than their timeout.

### Metrics

| Metric | Labels |
|---|---|
| `reactorcide_jobs_finished_total` | `status`, `failure_class` (`none` for a completed job), `queue` |
| `reactorcide_job_infra_retries_total` | `failure_class` |

These metrics have no workflow or project label, because a large repository
can have thousands of workflows.
