# Runnerlib Design

> **System Architecture**: See `../DESIGN.md` for complete system architecture. This document focuses on runnerlib-specific implementation details.

## Purpose

Runnerlib is a **job execution framework and utilities library** for CI/CD systems. Its primary role is to provide a standardized, extensible runtime environment for executing CI/CD jobs with proper lifecycle management, security controls, and developer-friendly utilities.

**Key Distinction**: Runnerlib runs **INSIDE** job containers (in target architecture), not as a container orchestrator. The worker spawns containers; runnerlib provides utilities and execution logic within those containers.

## Core Responsibilities

### 1. Job Execution Runtime
Runnerlib provides the runtime environment where CI/CD job code executes. It handles:
- **Step execution**: Running individual steps within a job (sequential, parallel, conditional)
- **Environment setup**: Preparing the execution context within the container
- **Secret management**: Automatic masking of sensitive values in logs and outputs
- **Resource management**: Cleanup of temporary files and resources
- **Workflow orchestration**: Triggering follow-up jobs based on results and configuration

### 2. Source Code Preparation
Runnerlib manages the retrieval and preparation of source code for jobs with **optional and flexible strategies**:
- **Git operations**: Clone repositories, checkout specific refs (branches, tags, commits)
- **Directory management**: Copy local directories, create structured workspaces
- **Multiple source types**: `git`, `copy`, `tarball` (stub), `hg` (stub), `svn` (stub), or `none`
- **Optional preparation**: Source preparation can be completely skipped (`source_type=none`) for pre-mounted or no-source jobs
- **Dual source support**: Separate trusted CI code (`ci_source_*`) from untrusted source code (`source_*`) for secure PR execution

**Source Preparation Strategies**:
```python
# Strategy 1: No source preparation (pre-mounted or not needed)
config = get_config(
    job_command="echo 'hello'",
    source_type="none"  # or omit source_type entirely
)

# Strategy 2: Git source (most common)
config = get_config(
    job_command="make test",
    source_type="git",
    source_url="https://github.com/user/repo.git",
    source_ref="main"
)

# Strategy 3: Local directory copy
config = get_config(
    job_command="npm test",
    source_type="copy",
    source_url="/path/to/local/source"
)

# Strategy 4: Dual source (trusted CI + untrusted PR code)
config = get_config(
    job_command="python /job/ci/run_tests.py",
    # Trusted CI code
    ci_source_type="git",
    ci_source_url="https://github.com/company/ci-scripts.git",
    ci_source_ref="main",
    # Untrusted PR code
    source_type="git",
    source_url="https://github.com/attacker/fork.git",
    source_ref="pr-branch"
)
```

**Directory Layout**:
- Regular source: `/job/src/` (potentially untrusted)
- CI source: `/job/ci/` (trusted, has access to secrets)
- Artifacts: `/job/artifacts/`
- Workspace: `/job/`

### 3. Lifecycle Hook System
Runnerlib provides a **plugin-based lifecycle hook system** allowing developers to inject custom behavior at any phase:
- **Extensibility**: Add custom logic without modifying core code
- **Composability**: Multiple plugins can operate at the same phase
- **Priority control**: Execute plugins in specific order

**Lifecycle Phases**:
1. `PRE_VALIDATION` - Before configuration validation
2. `POST_VALIDATION` - After configuration validation passes
3. `PRE_SOURCE_PREP` - Before source code checkout/preparation
4. `POST_SOURCE_PREP` - After source code is ready
5. `PRE_EXECUTION` - Before job execution begins (formerly PRE_CONTAINER)
6. `POST_EXECUTION` - After job execution completes (formerly POST_CONTAINER)
7. `ON_ERROR` - When errors occur during any phase
8. `CLEANUP` - Final cleanup regardless of success/failure

**Note**: In the current transitional state, `PRE_EXECUTION`/`POST_EXECUTION` may still be named `PRE_CONTAINER`/`POST_CONTAINER` in code. These refer to the execution phase, not container spawning.

### 4. Configuration Management
Runnerlib uses **hierarchical configuration** allowing flexibility and override capabilities:
- **Defaults**: Sensible defaults for common scenarios
- **Environment Variables**: System-level configuration via `REACTORCIDE_*` variables
- **CLI Arguments**: Job-specific overrides for individual runs
- **File-based Config**: YAML/JSON job definitions

**Priority**: CLI Arguments > Environment Variables > Defaults

### 5. Security Features

#### Secret Masking
- **Value-based masking**: Masks secret values wherever they appear in logs
- **Dynamic registration**: Jobs can register new secrets at runtime via Unix domain socket
- **Command masking**: Secrets hidden in process command lines and arguments
- **Environment scanning**: Automatically masks values from secret environment variables

#### Isolation
- **Container boundaries**: All jobs run in isolated containers, not on host
- **Path validation**: Prevents path traversal attacks (`../` blocked)
- **Controlled mounts**: Only specific directories mounted into containers
- **No privileged access**: Containers run without elevated privileges

## Jobs, Steps, and Workflows

Understanding these concepts is critical to understanding runnerlib's role:

### Job
A **job** is a single container execution with one or more **steps**:
- All steps share the same container environment
- Steps can run sequentially or in parallel
- Steps can be conditional (run if previous step succeeded)
- Logs are scoped per step
- Job succeeds if all required steps succeed

**Example**:
```python
job = Job("test-and-build")
job.add_step("checkout", "git clone https://github.com/user/repo.git /job/src")
job.add_step("test", "pytest tests/", depends_on=["checkout"])
job.add_step("build", "python setup.py bdist_wheel", depends_on=["test"])
```

### Step
A **step** is a single command or operation within a job:
- Has a name for identification
- Can depend on other steps
- Can run in parallel with other steps
- Has its own exit code and logs

### Workflow
A **workflow** is a collection of multiple jobs:
- Each job runs in its own container
- Jobs can depend on other jobs completing
- Jobs can run in parallel if no dependencies
- **Runnerlib orchestrates workflows** by triggering follow-up jobs

**Example**:
```
Workflow: "ci-pipeline"
├── Job 1: "test" (independent)
├── Job 2: "lint" (independent, runs parallel with Job 1)
├── Job 3: "build" (depends on Job 1, Job 2)
└── Job 4: "deploy" (depends on Job 3, conditional on branch=main)
```

### Runnerlib's Role

**Within a Job** (single container):
- Runnerlib executes steps sequentially or in parallel
- Manages step dependencies and conditionals
- Provides lifecycle hooks at each phase
- Streams logs with step scoping

**Across Workflows** (multiple jobs):
- Job 1 finishes and determines what comes next
- Runnerlib provides utilities to trigger Job 2, Job 3
- Worker receives trigger message and submits next jobs
- Process repeats for entire workflow

See `../DESIGN.md` for comprehensive workflow orchestration details.

## Architecture Models

### Current State (Transitional)
```
┌─────────────────────────────────────────────┐
│ Worker (Go) - Job Lifecycle Manager         │
│ - Polls Corndogs for jobs                   │
│ - Calls: python -m runnerlib.cli run        │
└──────────────────┬──────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────┐
│ Runnerlib (Python) - Container Orchestrator │
│ - Prepares workspace                         │
│ - Checks out source code                     │
│ - Spawns job container via docker            │
└──────────────────┬──────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────┐
│ Job Container                                │
│ - User's code from git                       │
│ - Executes job_command                       │
└─────────────────────────────────────────────┘
```

**Issues with Current State**:
- Double container nesting (worker container → runnerlib spawns → job container)
- Runnerlib must be installed in worker container
- Worker depends on Python runtime
- Unclear separation of concerns

### Target State (Vision)
```
┌─────────────────────────────────────────────┐
│ Worker (Go) - Minimal Lifecycle Manager     │
│ - Polls Corndogs for jobs                   │
│ - Creates workspace directory                │
│ - Spawns job container directly              │
│ - Monitors execution, ships logs             │
│ - Watches for workflow triggers              │
└──────────────────┬──────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────┐
│ Job Container (reactorcide/runner:latest)   │
│ - Runnerlib installed as Python library     │
│ - Workspace mounted at /job/                 │
│                                              │
│ Runnerlib Inside Container:                 │
│ ┌────────────────────────────────────────┐  │
│ │ 1. Check out source code (if needed)   │  │
│ │    → /job/src/                         │  │
│ │ 2. Check out CI code (if needed)       │  │
│ │    → /job/ci/                          │  │
│ │ 3. Execute job steps                   │  │
│ │ 4. Mask secrets in logs                │  │
│ │ 5. Determine next jobs (workflow)      │  │
│ └────────────────────────────────────────┘  │
│                                              │
│ Two Execution Modes:                        │
│ ┌────────────────────────────────────────┐  │
│ │ Simple (Default):                      │  │
│ │ python -m runnerlib.cli run \          │  │
│ │   --git-url <url> --git-ref <ref> \    │  │
│ │   --job-command "make test"            │  │
│ └────────────────────────────────────────┘  │
│                                              │
│ ┌────────────────────────────────────────┐  │
│ │ Advanced (Python Script):              │  │
│ │ python /job/ci/my_pipeline.py          │  │
│ │                                        │  │
│ │ # my_pipeline.py:                      │  │
│ │ import runnerlib                       │  │
│ │ # Use lifecycle hooks, utilities, etc. │  │
│ │ # Trigger follow-up jobs               │  │
│ └────────────────────────────────────────┘  │
└─────────────────────────────────────────────┘
```

**Benefits of Target State**:
- **Single container nesting**: Worker spawns job container directly
- **Worker simplicity**: Small Go binary, no Python, no git operations
- **Worker flexibility**: Can spawn any docker image with any command
- **Clear separation**: Worker manages lifecycle, runnerlib provides utilities
- **Kubernetes-ready**: Maps cleanly to Kubernetes Jobs
- **Security**: Approved CI code separate from PR code
- **Standalone capable**: Runnerlib works without infrastructure

## Runnerlib's Role in Target Architecture

In the target architecture, runnerlib transitions from a **container orchestrator** to a **job execution library**:

### As a Library (Primary Role)
Job code imports runnerlib to access:
- **Lifecycle hooks**: Insert custom behavior at execution phases
- **Utilities**: Helper functions for common CI/CD tasks
- **Secret management**: Register secrets, access masking utilities
- **Git operations**: Query repository information, detect changed files
- **Environment access**: Read job configuration, environment variables

**Example Job Script**:
```python
#!/usr/bin/env python3
import runnerlib
from runnerlib.plugins import Plugin, PluginPhase, PluginContext

class CustomBuildPlugin(Plugin):
    def __init__(self):
        super().__init__("custom_build", priority=50)

    def supported_phases(self):
        return [PluginPhase.PRE_EXECUTION, PluginPhase.POST_EXECUTION]

    def pre_execution(self, context):
        # Custom setup before running tests
        print("Setting up custom build environment...")

    def post_execution(self, context):
        # Custom cleanup or reporting
        if context.exit_code == 0:
            print("Build succeeded! Uploading artifacts...")

# Register custom plugin
runnerlib.register_plugin(CustomBuildPlugin())

# Run the job with custom lifecycle
exit_code = runnerlib.run_job(
    command="make test && make build",
    source_dir="/job/src",
    artifacts_dir="/job/artifacts"
)
```

### As a CLI (Fallback/Simple Path)
For users who don't need custom plugins, runnerlib CLI provides a simple execution path:
```bash
# Worker passes this command to job container
python -m runnerlib.cli run \
  --git-url https://github.com/user/repo.git \
  --git-ref main \
  --job-command "npm install && npm test"
```

The CLI handles:
- Source preparation (git checkout, tarball download, etc.)
- Environment setup
- Step execution (sequential or parallel)
- Log streaming with secret masking
- Workflow triggers
- Cleanup

## Key Design Decisions

### 1. No VCS Lock-In
Runnerlib is **not tied to git** specifically. While git is the default:
- Source preparation is pluggable via `PRE_SOURCE_PREP` hooks
- Users can implement custom source handlers (mercurial, svn, tarball downloads, etc.)
- The system cares about "source code in a directory", not how it got there

### 2. Configuration Over Convention
Runnerlib prefers **explicit configuration** over implicit magic:
- All paths are explicit (code_dir, job_dir)
- Environment variables are namespaced (`REACTORCIDE_*`)
- No hidden global state or singletons
- Clear override hierarchy

### 3. Security by Default
Security is **not optional** in runnerlib:
- Secret masking is always active
- Containers never run privileged
- Path traversal protection is built-in
- All file operations validate paths

### 4. Extensibility Without Modification
Users extend runnerlib via **plugins**, not by forking:
- Plugin system covers all lifecycle phases
- Plugins can modify config, environment, and behavior
- No need to modify runnerlib source code
- Plugins are isolated from each other

## Runtime Modes

Runnerlib can run in different contexts, providing flexibility for various use cases:

### 1. Inside Job Container (Target Architecture)
**Context**: Worker spawns container with runnerlib installed

```bash
# Worker executes:
docker run --rm \
  -v /tmp/job-workspace:/job \
  -e REACTORCIDE_GIT_URL=https://github.com/user/repo.git \
  -e REACTORCIDE_GIT_REF=main \
  reactorcide/runner:latest \
  python -m runnerlib.cli run --job-command "make test"
```

**Capabilities**:
- ✅ Source code checkout (git, local, etc.)
- ✅ Job step execution
- ✅ Secret masking
- ✅ Lifecycle hooks
- ✅ Workflow triggers
- ❌ Container spawning (not needed)

### 2. Local Laptop (Standalone)
**Context**: Developer running jobs locally for testing/debugging

```bash
# On laptop:
python -m runnerlib.cli run \
  --git-url https://github.com/user/repo.git \
  --git-ref feature-branch \
  --job-command "make test"
```

**Capabilities**:
- ✅ Source code checkout
- ✅ Job step execution
- ✅ Secret masking
- ✅ Lifecycle hooks
- ✅ Workflow trigger logging (outputs what to run next)
- ❌ Workflow execution (manual - run next job yourself)

### 3. Worker Container (Current/Transitional)
**Context**: Worker calls runnerlib to spawn job containers

```bash
# Worker executes:
python -m runnerlib.cli run \
  --git-url https://github.com/user/repo.git \
  --git-ref main \
  --job-command "make test" \
  --runner-image alpine:latest
```

**Capabilities**:
- ✅ Source code checkout
- ✅ Container spawning (via docker)
- ✅ Job step execution (in spawned container)
- ✅ Secret masking
- ✅ Lifecycle hooks
- ⚠️ Double container nesting (issue - being phased out)

**Note**: This mode is **transitional** and will be replaced by mode #1 (worker spawns containers, runnerlib runs inside).

## Migration Path

### Current Usage Pattern (to be deprecated)
```bash
# Worker calls runnerlib from worker container
python -m runnerlib.cli run \
  --git-url https://github.com/user/repo.git \
  --git-ref main \
  --job-command "make test" \
  --runner-image alpine:latest
```
This spawns a container from within a container.

### Target Usage Pattern
```bash
# Worker (Go) spawns job container directly:
docker run --rm \
  -v /tmp/job-workspace:/job \
  -e REACTORCIDE_CODE_DIR=/job/src \
  -e REACTORCIDE_JOB_COMMAND="make test" \
  reactorcide/runner:latest \
  python -m runnerlib.cli run
```
Runnerlib runs **inside** the job container as a library/utility.

## Integration Points

### With Coordinator API (Go)
- **Job Submission**: Coordinator receives jobs via REST API
- **Job Metadata**: Stored in PostgreSQL with git_url, git_ref, job_command
- **Queue Management**: Jobs queued to Corndogs for distribution
- **Status Updates**: Worker updates job status throughout lifecycle

### With Worker (Go)
- **Job Pickup**: Worker polls Corndogs for available jobs
- **Workspace Creation**: Worker creates workspace directory
- **Container Spawn**: Worker runs job container with runnerlib installed
- **Log Shipping**: Worker captures logs from job container stdout/stderr
- **Workflow Triggers**: Worker watches for follow-up job trigger messages from runnerlib
- **Cleanup**: Worker removes workspace after job completes

### With Job Code (Python/Other)
- **Library Import**: Job scripts import runnerlib for utilities
- **CLI Invocation**: Simple jobs use CLI without custom code
- **Plugin Registration**: Jobs register custom lifecycle plugins
- **Secret Registration**: Jobs dynamically register new secrets via socket

## Workflow Orchestration Utilities

Runnerlib provides utilities for triggering follow-up jobs (workflows):

### Triggering Jobs

```python
import runnerlib

# Trigger a single job
runnerlib.trigger_job(
    job_name="deploy-staging",
    env={"DEPLOY_TARGET": "staging", "ARTIFACT_URL": "s3://..."}
)

# Trigger multiple jobs
runnerlib.trigger_jobs([
    {"job_name": "build-linux", "env": {"PLATFORM": "linux"}},
    {"job_name": "build-macos", "env": {"PLATFORM": "macos"}},
])
```

### State Checking

```python
# Check if a job is already running
if runnerlib.is_job_running("deploy-production"):
    print("Deploy already in progress, skipping")
    exit(0)

# Get results from previous job in workflow
test_results = runnerlib.get_job_result("test")
if test_results["exit_code"] == 0:
    trigger_deploy()
```

### Local Execution Mode

When running locally without a worker, runnerlib outputs what should run next:

```python
# This logs to stdout in a format the user can see
runnerlib.log_next_job(
    "deploy-staging",
    reason="tests passed",
    depends_on=["test", "build"]
)
```

Output:
```
📋 Next jobs to run:
  → deploy-staging (waiting on: test, build - all complete)

Run the next job:
  $ python -m runnerlib.cli run --job deploy-staging --workflow-file pipeline.yaml
```

### Communication Protocol

Runnerlib communicates workflow triggers to the worker via:
1. **Stdout Protocol**: JSON messages on stdout that worker watches for
2. **File-based**: Write to `/job/triggers.json` that worker reads
3. **API Call**: Direct HTTP call to Coordinator API to submit jobs

Worker detects these messages and submits follow-up jobs to the queue.

## Future Enhancements

### Approved CI Code Repository ✅ IMPLEMENTED
Support for **separate CI code repository** to enable secure execution of untrusted PR code:
- ✅ **Dual source configuration**: `source_*` fields for untrusted code, `ci_source_*` fields for trusted CI code
- ✅ **Separate checkout**: CI code checked out to `/job/ci/`, source code to `/job/src/`
- ✅ **Multiple VCS support**: git (implemented), copy (implemented), tarball/hg/svn (stubs)
- ✅ **Optional preparation**: Jobs can skip source preparation entirely with `source_type=none`
- ✅ **Security model**: PR code in `/job/src/` cannot modify CI code in `/job/ci/`

**Status**: Fully implemented in Step 0.6 (deployment-plan.md)

See `../DESIGN.md` Security Model section and `test_source_preparation.py` for usage examples.

### Container Registry Integration
- Pre-built `reactorcide/runner` images with runnerlib
- Version-specific tags (`reactorcide/runner:v1.2.3`)
- Language-specific variants (`reactorcide/runner:python3.11`, `reactorcide/runner:node20`)

### Observability
- Structured logging with trace IDs
- Metrics export (Prometheus format)
- Job performance profiling
- Plugin execution timing

### Multi-Language Support
While runnerlib is Python, the **job code can be any language**:
- Job container can have multiple runtimes
- Job command can invoke any executable
- Plugins can be written in Python but orchestrate any language

## References

- **Plugin System**: See `src/plugins.py` for plugin implementation
- **Configuration**: See `src/config.py` for configuration hierarchy
- **Source Prep**: See `src/source_prep.py` for git and directory operations
- **Container Execution**: See `src/container.py` for container orchestration
- **CLI Interface**: See `src/cli.py` for command-line usage

---

**Note**: This design document reflects the **target architecture**. The current implementation is in a transitional state and will evolve toward this vision incrementally. See `deployment-plan.md` for the migration roadmap.
