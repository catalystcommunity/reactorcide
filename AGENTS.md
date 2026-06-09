# Reactorcide - Minimalist CI/CD System

**Reactorcide** is a minimalist CI/CD system that aims to be fully-featured for serious engineering teams, supporting both open source and business needs with a focus on security for outside contributions. The system evolved from a simple set of bash utilities into a comprehensive, container-based job execution platform.

## Architecture

**IMPORTANT**: See `DESIGN.md` for the complete system architecture, design principles, and component interactions. This document provides implementation guidance; DESIGN.md provides the architectural foundation.

Key architectural documents:
- **`./DESIGN.md`** - System-level architecture, component responsibilities, deployment models
- **`./runnerlib/DESIGN.md`** - Detailed runnerlib design and implementation

## Philosophy

- **Isolation First**: Run jobs from a known state in isolated environments
- **Configuration Flexibility**: System configuration and job configuration are separate and configurable
- **VCS Agnostic**: No hard ties to specific VCS providers - works with git, mercurial, or any source control system
- **Local Development**: Run jobs from your laptop as easily as from the full system
- **Building Blocks**: Modular components that can be combined as needed
- **Security by Design**: Built with outside contributions and security considerations in mind

## System Components

### 🏃 **Runnerlib** (Python)
The core job execution library providing:

#### Configuration Management
- **Hierarchical configuration system**: Defaults → Environment Variables → CLI Arguments
- **Environment variable support**: `REACTORCIDE_*` variables for system configuration
- **Job environment handling**: Inline variables or secure file-based configuration
- **CLI overrides**: All configuration parameters can be overridden via command line

#### Source Preparation
- **Git repository checkout**: Clone repositories with specific refs (branches, tags, commits)
- **Directory copying**: Copy local directories to job workspace
- **Secure directory management**: Fixed `./job` → `/job` mount structure
- **Directory structure validation**: Automatic creation and verification

#### Container Execution
- **nerdctl integration**: Secure container execution with nerdctl runtime
- **Environment injection**: Controlled environment variable passing
- **Real-time output streaming**: Live stdout/stderr forwarding
- **Working directory control**: Configurable job execution context

#### Comprehensive Validation
- **Configuration validation**: Required fields, path formats, security checks
- **Container image verification**: Check image availability before execution
- **Runtime validation**: Verify nerdctl availability and functionality
- **File system validation**: Directory existence and permission checks

#### Dry-Run Capabilities
- **Pre-flight validation**: Complete execution preview without running
- **Configuration display**: Resolved values after hierarchy processing
- **Environment analysis**: Variable inspection with security masking
- **Directory inspection**: Structure and content analysis
- **Command preview**: Exact nerdctl command generation

#### Security Features
- **Path traversal protection**: Prevents `../` attacks in file paths
- **Controlled environment**: Job files must be within `./job/` directory
- **Sensitive data masking**: Automatic masking of secrets in logs
- **Container isolation**: No privileged containers or arbitrary mounts

#### Git Operations
- **File change detection**: Identify changed files from git references
- **Repository information**: Branch, commit, and status details
- **Git validation**: Repository integrity and accessibility checks

#### CLI Interface
Commands available:
- `run` - Execute jobs with full configuration support
- `run-job` - Run a job from a JSON/YAML definition file
- `checkout` - Clone git repositories to workspace
- `copy` - Copy local directories to workspace
- `cleanup` - Clean up job directories
- `config` - Display resolved configuration
- `validate` - Validate configuration without execution
- `git files-changed` - Show changed files from git ref
- `git info` - Display repository information

#### Advanced Features
- **Plugin System**: Lifecycle hooks for extending job execution at various phases (pre/post validation, source prep, container execution, error handling)
- **Dynamic Secret Masking**: Value-based secret masking with runtime registration via Unix domain socket
- **Secret Registration Server**: Allows running jobs to dynamically register secrets for masking
- **Job Isolation**: Secure container execution with controlled mounts and no privileged access

### 🌐 **Coordinator API** (Go)
A REST API service for job management and orchestration:

- **Job Management**: Submit, monitor, and control job execution
- **Authentication & Authorization**: Token-based authentication with user management
- **VCS Integration**: GitHub and GitLab webhook support with status updates
- **Worker Management**: Distributed job processing with retry logic
- **Workflow Engine**: Multi-step workflow execution support
- **Priority Scheduling**: Job prioritization and queue management
- **Secret Masking**: Built-in secret value masking for logs
- **Metrics & Monitoring**: Prometheus metrics integration
- **Object Storage**: Support for S3, MinIO, GCS, and filesystem storage

### 🐕 **Corndogs Integration** (gRPC)
Distributed task queue system for job distribution:

- **Task Queue Management**: Submit and retrieve tasks from distributed queues
- **Worker Coordination**: Manage task state transitions and worker assignments
- **Retry Logic**: Automatic retry with exponential backoff
- **Timeout Handling**: Clean up timed-out tasks automatically

### 🎯 **Deployment & Infrastructure**

#### Docker Compose (Development)
- PostgreSQL database for state management
- MinIO for S3-compatible object storage
- Database migrations with automatic execution
- Health checks and service dependencies

#### Kubernetes Deployment (Production)
- **Helm Chart**: Complete Kubernetes deployment configuration
- **Autoscaling**: HPA support for API and workers
- **High Availability**: Multi-replica deployments with rolling updates
- **Secret Management**: Integration with external secret managers
- **Network Policies**: Pod-to-pod communication restrictions
- **Monitoring**: Prometheus metrics and health endpoints

#### Local Development
- **Skaffold**: Hot-reloading development environment
- **Configuration Examples**: Sample YAML/JSON job definitions
- **Test Utilities**: Integration and unit test suites

## Security Considerations

- **Path Traversal Protection**: Prevents `../` attacks in file paths
- **Container Isolation**: No privileged containers or arbitrary mounts
- **Sensitive Data Masking**: Automatic masking of secrets in logs
- **RBAC Support**: Kubernetes service accounts with minimal permissions
- **Image Scanning**: Support for vulnerability scanning
- **Pod Security Standards**: Appropriate security policies enforcement

## Getting Started

1. **Local Development**: Run with `docker-compose up` for a complete environment
2. **Job Execution**: Use `uv run python -m src.cli run` or submit jobs via the API
3. **Kubernetes Deployment**: Deploy with Helm chart for production use
4. **Integration**: Connect to existing Corndogs deployment or deploy alongside

## Common Operations

### Prerequisites

These operations assume you have:
- A secrets password file (e.g., `~/.reactorcide-pass`)
- Environment configuration files for builds and deployments stored in a config directory (e.g., `~/.config/reactorcide/`)
- The `reactorcide` CLI built and available at `./coordinator_api/reactorcide`

### Building and Pushing All Docker Images

Build all containers and push them to the configured registry:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat <secrets-password-file>)" ./coordinator_api/reactorcide run-local -i <path-to-builds-env-config>.yaml --job-dir ./ ./jobs/build-all.yaml
```

### Deploying Reactorcide to a VM

Deploy or update Reactorcide on a VM environment (idempotent):

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat <secrets-password-file>)" ./coordinator_api/reactorcide run-local --job-dir ./ -i <path-to-vm-deploy-config>.yaml ./jobs/deploy-to-vm.yaml
```

### Submitting a Job to a Remote Coordinator

**Preferred: Use the CLI tool** for submitting jobs to a remote Reactorcide instance:

```bash
# Submit a job using the CLI (recommended)
REACTORCIDE_SECRETS_PASSWORD="$(cat <secrets-password-file>)" \
  ./coordinator_api/reactorcide submit \
    --api-url "http://<api-host>:<port>" \
    --token "$(./coordinator_api/reactorcide secrets get <secret-path> api_key)" \
    --overlay <optional-overlay-file>.yaml \
    ./path/to/job.yaml

# Or with --wait to block until completion
./coordinator_api/reactorcide submit \
    --api-url "http://<api-host>:<port>" \
    --token "$API_TOKEN" \
    --wait \
    ./jobs/my-job.yaml
```

The CLI handles:
- Job YAML parsing and validation
- Overlay file merging (environment-specific config)
- Secret reference resolution
- Proper error reporting

**Alternative: Direct API call** (for scripts or when CLI isn't available):

```bash
# Get API key from secrets
API_KEY=$(REACTORCIDE_SECRETS_PASSWORD="$(cat <secrets-password-file>)" \
  ./coordinator_api/reactorcide secrets get <secret-path> api_key)

# Submit job via curl
curl -s -X POST "http://<api-host>:<port>/api/v1/jobs" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "job-name",
    "source_type": "git",
    "source_url": "https://github.com/owner/repo.git",
    "source_ref": "main",
    "job_command": "echo Hello from Reactorcide!",
    "runner_image": "docker.io/library/alpine:latest"
  }'
```

## Job Types

Reactorcide supports two main job execution patterns:

### Simple Command Jobs

Run any command in any container image. No source code checkout required.

```yaml
name: simple-echo
job_command: echo "Hello from Reactorcide!"
runner_image: alpine:latest
source_type: git
source_url: https://github.com/example/repo.git  # Required but can be minimal
source_ref: main
```

Use cases:
- Running arbitrary scripts or binaries
- Container-based utilities (linters, formatters, etc.)
- Quick tests or one-off commands

### Runnerlib Jobs

Full CI/CD job execution with source preparation, environment management, and structured output.

```yaml
name: build-project
image: containers.catalystsquad.com/public/reactorcide/runnerbase:dev
command: runnerlib run --job-command "python /job/src/scripts/build.py"
source:
  type: git
  url: https://github.com/org/project.git
  ref: main
environment:
  BUILD_ENV: production
  DEPLOY_TARGET: staging
```

Use cases:
- Full CI/CD pipelines
- Jobs requiring source code checkout
- Jobs with complex environment configuration
- Jobs using the runnerlib Python framework

### Key Differences

| Feature | Simple Command | Runnerlib Job |
|---------|---------------|---------------|
| Container image | Any | runnerbase or custom with runnerlib |
| Source checkout | Optional (but source_type required) | Handled by runnerlib |
| Working directory | Container's WORKDIR | Configurable via code_dir |
| Environment | Passed directly | Managed by runnerlib with secret resolution |
| Output handling | Basic stdout/stderr | Structured logs with masking |

### Fork PRs and Cross-Repo Code Sources

Reactorcide distinguishes two sources for a job:

- **Code source** (`SourceURL`/`SourceRef`): the code being checked out at `/job/src`. For a PR opened from a fork, this points at the **fork** — that's where the branch lives.
- **CI source** (`CISourceURL`/`CISourceRef`): the trusted location of CI definitions (`.reactorcide/jobs/*.yaml`). Always upstream — never the fork. Fork content is never executed as a CI definition.

Environment variables made available to jobs:

| Variable | Meaning |
|---|---|
| `REACTORCIDE_SOURCE_URL` | Code-source URL. For fork PRs, this is the fork's clone URL. |
| `REACTORCIDE_HEAD_URL` | Explicit head repo URL (same as `SOURCE_URL` for PRs; convenience). |
| `REACTORCIDE_HEAD_REF` | PR head branch name. |
| `REACTORCIDE_BASE_URL` | Upstream/base repo URL. Useful for jobs that need both remotes (e.g. `git log base..head`). |
| `REACTORCIDE_BASE_REF` | PR target branch. |
| `REACTORCIDE_IS_FORK_PR` | `"true"` when the PR head lives on a different repo than the base. |
| `REACTORCIDE_CI_SOURCE_URL` | Always upstream (or `project.DefaultCISourceURL`). Never the fork. |
| `REACTORCIDE_CI_SOURCE_REF` | For PRs, the base branch SHA. For pushes, the pushed SHA. |
| `REACTORCIDE_PR_REF`, `REACTORCIDE_PR_BASE_REF`, `REACTORCIDE_PR_NUMBER` | Back-compat: kept for older job YAMLs. |

For a raw-command job that needs both remotes (e.g. to inspect commits added by the PR), the recommended pattern is:

```bash
git clone --branch "${REACTORCIDE_HEAD_REF}" "${REACTORCIDE_HEAD_URL}" /job/src
cd /job/src
git remote add upstream "${REACTORCIDE_BASE_URL}"
git fetch upstream "${REACTORCIDE_BASE_REF}"
# Now `git log upstream/${REACTORCIDE_BASE_REF}..HEAD` works for fork PRs too.
```

Jobs that wrap their command with `runnerlib run` automatically get both remotes set up — runnerlib reads `REACTORCIDE_BASE_URL`/`BASE_REF` during source preparation and adds `upstream` to the clone when the base differs from the cloned origin.

#### Testing a fork PR locally

Run-local supports cloning an arbitrary code source instead of bind-mounting your working copy:

```bash
# Test a specific URL + ref
./coordinator_api/reactorcide run-local \
  --code-url https://github.com/fork-owner/repo.git \
  --code-ref lilac/text-overflow \
  .reactorcide/jobs/conventional-commits.yaml

# Resolve a PR via the GitHub CLI (requires `gh` and a cwd inside the target repo)
./coordinator_api/reactorcide run-local --pr 60 .reactorcide/jobs/conventional-commits.yaml
```

Both flags clone into a tempdir, mount it at `/job/src`, and set the same `REACTORCIDE_*` env vars the worker would set for a fork PR — so jobs behave identically locally and remotely. When neither flag is set, run-local bind-mounts `--job-dir` (your local working copy) as before.

#### Matching the worker's runner uid (sudo / HOME parity)

By default `run-local` runs the job container as the **host uid** — the right choice when the job writes back into your bind-mounted working copy (compilers emitting into the tree, codegen, `helm dependency update`, anything you then `git diff`). The worker, however, runs jobs as the image's runner uid **1001** (`runner`, HOME `/home/runner`, with the image's `NOPASSWD: apt-get` sudoers entry). So a job doing `sudo apt-get install …` passes on a worker but fails under run-local as the host uid.

When you need that parity, opt in:

```bash
# Run as the image's runner uid 1001:1001, like the worker
./coordinator_api/reactorcide run-local --as-runner .reactorcide/jobs/test-postgres.yaml

# Or pin an explicit uid[:gid]
./coordinator_api/reactorcide run-local --user 1001:1001 ./jobs/my-job.yaml
```

A job can also declare this once in its YAML so every flag-less `run-local` is consistent. The `run_local` block is **run-local-only — the worker ignores it entirely**:

```yaml
run_local:
  as_runner: true      # run as 1001:1001 locally; or:
  user: runner         # symbolic; also accepts "host" or "1001:1001"
```

`--user` / `run_local.user` accept a numeric `uid[:gid]` or the symbolic names `runner` (the image runner uid 1001) and `host` (the invoking user).

Precedence (highest first): `--user` → `--as-runner` → `run_local.user` → `run_local.as_runner` → host uid (default). In parity mode run-local uses the image's real `/etc/passwd`/sudoers and lets HOME resolve from the image, matching the worker. **HOME is `/home/runner` and writable in both modes** — parity mode gets it from the image; the host-uid default mounts a fresh run-as-uid-owned scratch dir there — so `~`-reading tools (git, go, docker config) behave the same locally and remotely without `export HOME=…` boilerplate.

**Caveat (by design):** in parity mode the bind-mounted `--job-dir` at `/job/src` is still host-owned, so a parity job can't *write into the source tree*. run-local deliberately has no plumbing to chown your working copy to the parity uid — a job that needs a writable checkout as the runner uid should use `--code-url`/`--pr` (cloned into a writable tempdir), or do its own cloning/ownership inside the job.

## Project Status

The system is actively being developed with the core runnerlib functionality complete and the coordinator API providing job management capabilities. Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) to discuss and contribute.