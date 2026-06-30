# Runtime Behavior

Reactorcide treats local execution as a first-class path. The same job definition should work when run locally, on a VM worker, or as a Kubernetes Job, with only the execution substrate changing.

## Execution Modes

| Mode | Runtime | Code preparation | Logs |
|---|---|---|---|
| `run-local` | Docker or containerd/nerdctl | Bind-mounts `--job-dir` by default, or clones `--code-url` / `--pr` into a temp checkout | Streams directly from the local container |
| VM worker | Docker or containerd/nerdctl | Worker prepares a fresh workspace and source checkout | Streams from the runtime through the worker |
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

`run-local` bind-mounts the local `--job-dir` directly at `code_dir`. This is intentional: local jobs should see the working tree exactly as the user has it. A job can also run without local code, or use `--code-url`, `--code-ref`, or `--pr` to clone a separate checkout.

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

## Capabilities

Capabilities describe runtime services, not the uid the job runs as.

| Capability | Meaning |
|---|---|
| `docker` | Provide Docker-style container access for jobs that run nested containers |
| `builder` | Provide a BuildKit sidecar and `BUILDKIT_HOST` for image builds |
| `gpu` | Reserved for GPU access |

If a job with `docker` or `builder` needs root inside its container, declare that explicitly:

```yaml
capabilities:
  - builder
run_as:
  user: root
```

## Kubernetes Notes

The Kubernetes runner creates Kubernetes Jobs and streams their logs. It honors `working_dir`, prepares the configured `code_dir` and `job_dir`, and can be configured with:

- `REACTORCIDE_K8S_JOB_NAMESPACE`
- `REACTORCIDE_K8S_JOB_SERVICE_ACCOUNT`
- `REACTORCIDE_K8S_JOB_IMAGE_PULL_SECRETS`
- `REACTORCIDE_DIND_IMAGE`

Helm values expose the same settings under the worker configuration.
