# Running Reactorcide Jobs Locally

## Problem Statement

Run Reactorcide CI/CD jobs locally without requiring the full Reactorcide API and Worker infrastructure. This enables:

1. **Dogfooding**: Deploy Reactorcide using Reactorcide job definitions, even before the system is fully operational
2. **Local development**: Test job definitions before pushing to CI
3. **Offline execution**: Run jobs when the CI system is unavailable
4. **Debugging**: Reproduce CI failures locally with identical execution
5. **Bootstrapping**: Get the CI system running when you need the CI system to deploy itself

The key insight: if you have the job definition file and the required secrets, you should be able to execute that job locally with the same behavior as the production worker.

## Prerequisites: CLI Restructuring

Before implementing local job execution, we need to restructure the CLI tools.

### Task 1: Rename `coordinator-api` to `reactorcide`

The main binary should be named `reactorcide` since that's what the project is called:

```bash
# Current
coordinator-api serve
coordinator-api worker
coordinator-api migrate

# After rename
reactorcide serve
reactorcide worker
reactorcide migrate
reactorcide run-local --file job.yaml  # New command
reactorcide secrets set path/to/secret KEY
```

**Changes needed:**
- Rename `coordinator_api/` directory to `reactorcide/` (or keep internal, just change binary name)
- Update `go build -o reactorcide .`
- Update Dockerfiles to use new binary name
- Update deployment scripts

### Task 2: Move Secrets Management from Python to Go

The secrets commands currently live in runnerlib but belong in the main CLI:

**Current (in runnerlib Python):**
```bash
uv run runnerlib secrets init
uv run runnerlib secrets set npm/tokens NPM_TOKEN
uv run runnerlib secrets get npm/tokens NPM_TOKEN
```

**After (in reactorcide Go):**
```bash
reactorcide secrets init
reactorcide secrets set npm/tokens NPM_TOKEN
reactorcide secrets get npm/tokens NPM_TOKEN
reactorcide secrets delete npm/tokens NPM_TOKEN
reactorcide secrets list npm/tokens
reactorcide secrets list-paths
```

**Implementation:**
- Port `secrets_local.py` logic to Go
- Use same storage format for compatibility (scrypt + Fernet, or migrate to Go-native crypto)
- Keep same file locations (`~/.config/reactorcide/secrets/`)
- Remove secrets commands from runnerlib CLI

### Task 3: Clarify Runnerlib Scope

Runnerlib is specifically for **inside job containers**. After restructuring:

**Runnerlib commands (container use only):**
```
runnerlib run              # Execute job command with source prep
runnerlib checkout         # Git clone to /job/src
runnerlib copy             # Copy directory to /job/src
runnerlib cleanup          # Clean up /job directory
runnerlib git files-changed # Get changed files (for job scripts)
runnerlib git info         # Repository information (for job scripts)
runnerlib config           # Show resolved config (debugging)
runnerlib validate         # Validate config (debugging)
```

**Removed from runnerlib (moved to reactorcide):**
- `secrets *` commands
- `run-job` (file-based execution)

---

## Design: Local Job Execution

### Command Interface

```bash
# Run a job from definition file
reactorcide run-local --file job.yaml

# With secrets password
REACTORCIDE_SECRETS_PASSWORD=mypass reactorcide run-local --file job.yaml

# Custom runner image
reactorcide run-local --file job.yaml --runner-image myregistry/runner:v1

# Or via environment variable
REACTORCIDE_RUNNER_IMAGE=containers.catalystsquad.com/public/reactorcide/runnerbase:dev \
  reactorcide run-local --file job.yaml

# Output triggered jobs to a directory
reactorcide run-local --file job.yaml --triggers-dir ./triggered-jobs/

# Dry-run: validate and show what would execute
reactorcide run-local --file job.yaml --dry-run

# Verbose logging
reactorcide run-local --file job.yaml --verbose
```

### Job Definition Format

```yaml
# job.yaml
job_command: "./scripts/build.sh"

# Container image (optional - defaults to REACTORCIDE_RUNNER_IMAGE or registry default)
container_image: "containers.catalystsquad.com/public/reactorcide/runnerbase:dev"

# Source preparation
source_type: "git"          # git, copy, or none
source_url: "https://github.com/org/repo"
source_ref: "main"

# For local directory copy:
# source_type: "copy"
# source_path: "."          # Copy current directory

# CI scripts source (separate trusted repo)
ci_source_type: "git"
ci_source_url: "https://github.com/org/ci-scripts"
ci_source_ref: "main"

# Working directories inside container
code_dir: "/job/src"        # Where source is checked out
job_dir: "/job/src"         # Working directory for command

# Environment variables - supports secret references
env:
  NPM_TOKEN: "${secret:npm/tokens:NPM_TOKEN}"
  GITHUB_TOKEN: "${secret:github/tokens:CI_TOKEN}"
  DEBUG: "true"

# Execution settings
timeout_seconds: 3600
```

### Secret Resolution

Secrets are stored in local encrypted storage managed by the `reactorcide secrets` commands:

```bash
# Initialize (one-time)
reactorcide secrets init

# Store secrets
reactorcide secrets set npm/tokens NPM_TOKEN
reactorcide secrets set github/tokens CI_TOKEN
reactorcide secrets set deployment/credentials DB_PASSWORD

# List stored secrets
reactorcide secrets list-paths
reactorcide secrets list npm/tokens
```

The `${secret:path:key}` syntax in job files is resolved before container execution:

1. Parse secret references from environment variables in job file
2. Resolve using local encrypted storage with `REACTORCIDE_SECRETS_PASSWORD`
3. Replace references with actual values
4. Register all secret values with SecretMasker
5. Inject resolved values as container environment variables

### Workflow Output (Triggered Jobs)

Jobs can trigger follow-up jobs. In production, these are submitted to the API. For local execution, they're written to files.

**Inside a running job:**
```python
from workflow import trigger_job, flush_triggers

trigger_job(
    "deploy-staging",
    env={"DEPLOY_TARGET": "staging", "IMAGE_TAG": os.environ["BUILD_TAG"]},
    depends_on=["build"],
    condition="success"
)

flush_triggers()  # Writes to /job/triggers.json
```

**After job completes:**
```bash
$ reactorcide run-local --file build.yaml --triggers-dir ./next/

[INFO] Job completed with exit code 0
[INFO] Triggered jobs written to ./next/:
  - ./next/deploy-staging.yaml
  - ./next/notify-slack.yaml

# Run the next job manually
$ reactorcide run-local --file ./next/deploy-staging.yaml
```

---

## Implementation Plan

### Phase 1: CLI Restructuring

1. **Rename binary to `reactorcide`**
   - Update build commands in Dockerfiles, scripts
   - Update `main.go` and cmd structure if needed
   - Update deployment scripts

2. **Implement `reactorcide secrets` in Go**
   - Create `cmd/secrets.go` with subcommands
   - Port scrypt + Fernet encryption logic (or use Go-native equivalent)
   - Same storage location: `~/.config/reactorcide/secrets/`
   - Commands: `init`, `set`, `get`, `delete`, `list`, `list-paths`

3. **Remove secrets from runnerlib**
   - Delete `secrets_app` typer commands from `cli.py`
   - Keep `secrets_local.py` for now (resolution still happens in container)
   - Or: have Go resolve secrets before passing to container

### Phase 2: Local Job Execution

4. **Implement `reactorcide run-local`**
   - Create `cmd/run_local.go`
   - Job file parsing (YAML/JSON)
   - Secret reference resolution (using Go secrets storage)
   - Reuse existing `JobRunner` interface (docker_runner.go)

5. **Stdout log streaming with masking**
   - Create `internal/localrunner/output_stream.go`
   - Apply SecretMasker to all output
   - Support colorized terminal output

6. **Triggered job capture**
   - Read `/job/triggers.json` from workspace after execution
   - Convert to standalone job YAML files
   - Write to `--triggers-dir`

### Phase 3: Polish

7. **Dry-run mode**
   - Show resolved configuration
   - Display docker command that would run
   - Validate image availability

8. **Documentation and examples**
   - Update AGENTS.md with new CLI structure
   - Create example job files in `examples/jobs/`

---

## Architecture

### Execution Flow

```
reactorcide run-local --file job.yaml
              │
              ▼
┌─────────────────────────────────────┐
│ cmd/run_local.go                    │
│ - Parse job file (YAML/JSON)        │
│ - Resolve ${secret:path:key} refs   │
│ - Build JobConfig                   │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│ internal/localrunner/executor.go    │
│ - Create workspace directory        │
│ - Initialize SecretMasker           │
│ - Orchestrate container execution   │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│ internal/worker/docker_runner.go    │
│ (existing code, reused)             │
│ - SpawnJob()                        │
│ - StreamLogs()                      │
│ - WaitForCompletion()               │
│ - Cleanup()                         │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│ Container runs runnerlib            │
│ - Source checkout (git/copy)        │
│ - Execute job_command               │
│ - Write triggers.json if needed     │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│ Output + Triggered Jobs             │
│ - Stream stdout/stderr (masked)     │
│ - Read triggers.json                │
│ - Write triggered job files         │
│ - Return exit code                  │
└─────────────────────────────────────┘
```

### File Structure

```
coordinator_api/  (or rename to reactorcide/)
├── cmd/
│   ├── root.go
│   ├── serve.go          # API server
│   ├── worker.go         # Corndogs worker
│   ├── migrate.go        # Database migrations
│   ├── run_local.go      # NEW: Local job execution
│   └── secrets.go        # NEW: Secrets management
├── internal/
│   ├── worker/
│   │   ├── docker_runner.go      # Reused for local execution
│   │   ├── job_processor.go      # Production job processing
│   │   └── ...
│   ├── localrunner/              # NEW
│   │   ├── executor.go           # Local execution orchestration
│   │   ├── job_file.go           # Job YAML/JSON parsing
│   │   ├── output_stream.go      # Stdout streaming with masking
│   │   └── trigger_capture.go    # Read and convert triggered jobs
│   └── secrets/
│       ├── masker.go             # Existing secret masking
│       └── storage.go            # NEW: Local encrypted storage
└── main.go
```

---

## Usage Examples

### Example 1: Test a Build Locally

```yaml
# build.yaml
job_command: "./scripts/build.sh && ./scripts/test.sh"
source_type: "copy"
source_path: "."
env:
  NODE_ENV: "test"
  NPM_TOKEN: "${secret:npm/tokens:NPM_TOKEN}"
```

```bash
cd my-project
REACTORCIDE_SECRETS_PASSWORD=mypass reactorcide run-local --file build.yaml
```

### Example 2: Multi-Stage Pipeline

```yaml
# build.yaml - triggers deploy on success
job_command: |
  ./ci/build.sh
  ./ci/push-image.sh
source_type: "git"
source_url: "https://github.com/myorg/myapp"
source_ref: "main"
env:
  REGISTRY_PASSWORD: "${secret:registry/creds:PASSWORD}"
  IMAGE_TAG: "v1.0.0"
```

```bash
# Run build, capture triggered deploy job
reactorcide run-local --file build.yaml --triggers-dir ./pipeline/

# Run the triggered deploy
reactorcide run-local --file ./pipeline/deploy.yaml
```

### Example 3: Dogfooding - Deploy Reactorcide

```yaml
# jobs/deploy-reactorcide.yaml
job_command: "./deployment/deploy-vm.sh"
source_type: "copy"
source_path: "."
env:
  REACTORCIDE_DEPLOY_HOST: "${secret:reactorcide/deploy:HOST}"
  REACTORCIDE_DEPLOY_USER: "${secret:reactorcide/deploy:USER}"
  REACTORCIDE_DB_PASSWORD: "${secret:reactorcide/deploy:DB_PASSWORD}"
  REACTORCIDE_JWT_SECRET: "${secret:reactorcide/deploy:JWT_SECRET}"
```

```bash
# Deploy Reactorcide using its own job format
cd /path/to/reactorcide
REACTORCIDE_SECRETS_PASSWORD=mypass reactorcide run-local --file jobs/deploy-reactorcide.yaml
```

---

## Configuration Priority

### Runner Image Selection
1. `--runner-image` flag (highest priority)
2. `container_image` in job file
3. `REACTORCIDE_RUNNER_IMAGE` environment variable
4. Default: `containers.catalystsquad.com/public/reactorcide/runnerbase:dev`

### Secrets Password
1. `REACTORCIDE_SECRETS_PASSWORD` environment variable
2. Interactive prompt (if terminal attached)

---

## Verification

The primary verification is deploying Reactorcide itself using local job execution:

1. Store required deployment secrets:
   ```bash
   reactorcide secrets init
   reactorcide secrets set reactorcide/deploy HOST
   reactorcide secrets set reactorcide/deploy USER
   reactorcide secrets set reactorcide/deploy DB_PASSWORD
   reactorcide secrets set reactorcide/deploy JWT_SECRET
   ```

2. Create job definition `jobs/deploy-reactorcide.yaml`

3. Run the deployment:
   ```bash
   REACTORCIDE_SECRETS_PASSWORD=mypass reactorcide run-local --file jobs/deploy-reactorcide.yaml
   ```

4. Verify the deployment succeeds with identical behavior to running via CI.

---

## Future Enhancements

1. **Auto-run triggered jobs**: `--auto-continue` flag to automatically run triggered jobs
2. **Pipeline file**: Define multi-job pipelines in a single file
3. **Watch directory for triggered jobs**: Worker watches a directory and runs new job files automatically
4. **Parallel execution**: Run independent triggered jobs in parallel
5. **Result caching**: Skip jobs if inputs haven't changed
