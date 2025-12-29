# Reactorcide System Design

## Philosophy & Motivation

### The Core Principle

**The goal of any CI/CD system is to run a job.** Some systems have workflow definitions, events, or other capabilities, but at the core, a CI/CD job is a script you can run against a repo at a given point and have things happen. The rest is gluing up those extra bits.

Reactorcide starts from this fundamental truth: if you have a repo at a given point, you should be able to run the job on your laptop with the repo you have currently. This is the core concept everything else builds upon.

### Why Reactorcide Exists

**Security for outside contributions.** This is one of the core motivations for this project.

Most CI/CD systems don't allow third parties to submit a PR and have your approved job code run on their PR with a limited subset of secrets or capabilities - unless you bake those protections into the job itself. This is especially true for self-hosted solutions.

We want Reactorcide to work equally well for an Enterprise and an Open Source company with a wide array of adversarial actors. Exfiltrating secrets from CI is easy. The solution: **separate the job code (trusted) from the source code (untrusted)**. An attacker can run their PR 1000 different ways, but they cannot change the job code that has access to secrets.

Obviously no security is 100% infallible, so the basic separation allows a variety of security postures to be enabled including limiting who can run jobs in a PR in the first place, like Github has done.

### Design Evolution

The original vision was simple: "There is no workflow orchestration. Reactorcide just runs jobs."

The current design honors this philosophy while adding practical workflow capabilities. The key insight: **workflow orchestration exists in the job code** (via runnerlib utilities), **not prescribed by the system**.

Reactorcide provides building blocks. You can:
- Trigger follow-up jobs from within a job
- Make jobs depend on other jobs finishing
- Parse YAML configurations in your job code if you want
- Send emails, press manual buttons, whatever you need

We provide utilities for convenience, but we don't hold your hand or give you a box to play in. Flexibility and simplicity over prescriptive workflows.

---

## Overview

Reactorcide is a minimalist CI/CD system built on the principle of **separation of concerns**. The system is divided into three distinct layers:

1. **Runnerlib** (Python) - Job execution runtime and utilities
2. **Coordinator API** (Go) - Event processing, authentication, and job orchestration
3. **Worker** (Go) - Container lifecycle management and job execution

This separation enables flexibility in deployment models: from running jobs locally on a laptop for emergency debugging, to single-VM deployments, to fully distributed Kubernetes clusters.

## Core Design Principles

### 1. Decoupled Job Definition and Execution
**Critical Distinction**: The code that **defines the CI/CD job** is separate from the code being **tested/built**.

- **Job Code**: Scripts, configuration, pipeline definitions (can be in separate repo)
- **Source Code**: The application code being tested (PR code, branch code, etc.)
- **Default**: By default, job and source are the same repo (simple path)
- **Security Model**: For untrusted contributions, job code comes from a trusted repo while source code comes from the PR

This enables secure execution of CI/CD jobs against untrusted code contributions.

### 2. Environment Isolation First
Every job execution starts from a known, isolated state:
- Jobs run in containers, never on the host
- Workspace is prepared fresh for each job
- No state carries over between job runs
- Container images define the execution environment

### 3. Configuration Flexibility
System configuration and job configuration are distinct:
- **System Config**: How runnerlib itself operates (paths, runtime options)
- **Job Config**: What the job needs (environment variables, secrets, context)
- **Hierarchical Overrides**: Defaults → Environment Variables → CLI Arguments

### 4. VCS Agnostic
No hard dependencies on specific version control systems:
- Git is supported by default but not required
- Support for other VCS (mercurial, svn) via plugins
- Support for source code from tarballs, S3, or other sources
- System cares about "code in a directory", not how it got there

### 5. Local-First Development
You can run CI/CD jobs from your laptop without external dependencies:
- `runnerlib` CLI works standalone with configuration
- No requirement for API or worker infrastructure
- Same execution path locally and in production
- Emergency debugging doesn't require infrastructure

### 6. Bootstrap via Self
The entire system deploys itself using runnerlib:
- Deployment is a CI/CD job
- Self-hosting is a first-class deployment path
- Dogfooding ensures the system works for real use cases

## Jobs vs Workflows

Understanding the distinction between **jobs** and **workflows** is critical to understanding Reactorcide's architecture.

### Job: Single Unit of Execution

A **job** is a single container execution with one or more **steps** that run sequentially or in parallel.

**Key Properties of a Job**:
- Runs in a single container instance
- Steps share the same environment and filesystem
- Steps can run sequentially or in parallel
- Steps can have conditionals (if test passes, then deploy)
- Logs are scoped per step
- Exit code determines job success/failure

**Job Structure**:
```
Job: "test-and-build"
├── Step 1: "checkout" (clone source code)
├── Step 2: "test" (run tests)
├── Step 3: "build" (if step 2 succeeded)
└── Step 4: "upload-artifacts" (if step 3 succeeded)

Shared Environment:
- /job/src/ (source code)
- /job/artifacts/ (build outputs)
- Environment variables
- Secrets
```

**Example Scenarios**:
- Run tests, and if they pass, build the application
- Download dependencies, compile, and package
- Lint code, run security scan, generate report

### Workflow: Multiple Jobs with Dependencies

A **workflow** is a collection of multiple jobs that may have dependencies and run in sequence or parallel.

**Key Properties of a Workflow**:
- Each job runs in its own container (separate execution)
- Jobs can depend on other jobs completing
- Jobs can run in parallel if no dependencies
- Workflow orchestration is handled by **runnerlib** (not worker)
- Jobs can trigger other jobs conditionally

**Workflow Structure**:
```
Workflow: "deploy-pipeline"
├── Job 1: "test" (parallel)
│   └── Steps: checkout, run-tests
├── Job 2: "lint" (parallel with Job 1)
│   └── Steps: checkout, run-linter
├── Job 3: "build" (waits for Job 1, 2)
│   └── Steps: checkout, build-binary, upload-artifact
└── Job 4: "deploy" (waits for Job 3, conditional on branch)
    └── Steps: download-artifact, deploy-to-prod
```

**Example Scenarios**:
- Run tests and linter in parallel, then build if both pass
- Build for multiple platforms in parallel, then deploy
- Deploy to staging, run smoke tests, conditionally deploy to production

### Orchestration Model

**Runnerlib orchestrates workflows**, not the Coordinator or Worker.

**How it works**:
1. Worker receives "Job 1" from queue
2. Worker spawns container with runnerlib
3. Job 1 executes (runnerlib runs the job steps)
4. **Job 1 determines what comes next** based on:
   - Job result (success/failure)
   - Configuration (workflow YAML or Python code)
   - State checks (is another deploy running? skip this one)
5. Job 1 tells the worker: "trigger Job 2 and Job 3"
6. Worker submits Job 2 and Job 3 to the queue
7. Repeat

**Communication Mechanism**:

Runnerlib communicates workflow triggers back to the worker via:
- **Stdout Protocol**: Special formatted output the worker watches for
- **File-based**: Write trigger requests to `/job/triggers.json`
- **API Call**: Call Coordinator API directly to submit next jobs

**Example Trigger Output**:
```json
{
  "type": "trigger_job",
  "jobs": [
    {
      "job_name": "deploy-staging",
      "depends_on": ["test", "build"],
      "condition": "all_success",
      "env": {
        "DEPLOY_TARGET": "staging",
        "BUILD_ARTIFACT_URL": "s3://artifacts/build-123.tar.gz"
      }
    }
  ]
}
```

**Local Execution Mode**:

When running locally (no worker), runnerlib outputs what should run next:
```
$ python -m runnerlib.cli run --workflow-file pipeline.yaml

✓ Job: test (success)
✓ Job: lint (success)
✓ Job: build (success)

📋 Next jobs to run:
  → deploy-staging (waiting on: test, lint, build - all complete)

Run the next job:
  $ python -m runnerlib.cli run --job deploy-staging --workflow-file pipeline.yaml
```

### Configuration: YAML vs Code

Workflows can be defined in **YAML** (declarative) or **Python** (imperative).

**YAML Workflow** (Declarative):
```yaml
workflow:
  name: ci-pipeline

  jobs:
    - name: test
      image: reactorcide/runner:python3.11
      steps:
        - name: checkout
          run: git clone $GIT_URL /job/src
        - name: test
          run: pytest /job/src/tests

    - name: build
      depends_on: [test]
      image: reactorcide/runner:python3.11
      steps:
        - name: checkout
          run: git clone $GIT_URL /job/src
        - name: build
          run: python /job/src/setup.py bdist_wheel

    - name: deploy
      depends_on: [build]
      condition: branch == "main"
      image: reactorcide/runner:latest
      steps:
        - name: deploy
          run: python deploy.py
```

**Python Workflow** (Imperative):
```python
#!/usr/bin/env python3
import runnerlib
from runnerlib.workflow import Job, Step, trigger_jobs

# Define the job's steps
test_step = Step("test", command="pytest tests/")
build_step = Step("build", command="python setup.py bdist_wheel")

# Execute steps
runnerlib.run_step(test_step)

if test_step.success:
    runnerlib.run_step(build_step)

# Conditionally trigger next job
if build_step.success and runnerlib.env.branch == "main":
    # Check if there's already a deploy running
    if not runnerlib.check_deploy_in_progress():
        trigger_jobs([
            Job("deploy", env={"ARTIFACT": build_step.artifact_url})
        ])
    else:
        print("Deploy already in progress, skipping")
```

**Flexibility**:
- YAML is simple and declarative (most common use case)
- Python allows complex logic and state checks
- Mix both: YAML for structure, Python hooks for custom logic

### Step Execution Within a Job

Steps within a job can be:
- **Sequential**: One after another (default)
- **Parallel**: Multiple steps at once
- **Conditional**: Only run if condition is true

**Example with Parallel Steps**:
```yaml
job:
  name: multi-platform-test
  steps:
    - name: test-linux
      parallel: true
      run: pytest tests/ --platform=linux

    - name: test-macos
      parallel: true
      run: pytest tests/ --platform=macos

    - name: test-windows
      parallel: true
      run: pytest tests/ --platform=windows

    - name: aggregate-results
      depends_on: [test-linux, test-macos, test-windows]
      run: python aggregate-test-results.py
```

**Implementation**: Runnerlib manages step parallelization within the container using Python's async/await or threading.

### Convenience Functions

Runnerlib provides utilities to make workflow orchestration easy:

```python
# Trigger follow-up jobs
runnerlib.trigger_job("deploy", env={"TARGET": "staging"})

# Check state
if runnerlib.is_job_running("deploy"):
    print("Deploy already in progress")

# Get previous job results
test_results = runnerlib.get_job_result("test")
if test_results["exit_code"] == 0:
    trigger_deploy()

# For local execution, log what should run next
runnerlib.log_next_job("deploy-staging", reason="tests passed")
```

## System Components

### 1. Runnerlib (Python)

**Role**: Job execution runtime, utilities library, and CLI

**Responsibilities**:
- Prepare job workspace and source code
- Provide lifecycle hooks for job phases
- Execute job commands with proper isolation
- Manage secrets and mask sensitive data in logs
- Provide utilities for common CI/CD tasks (git operations, file change detection)
- Stream logs in real-time

**Interfaces**:
- **Python Library**: Import into job scripts for advanced control
- **CLI**: Standalone tool for simple job execution
- **Plugin System**: Extend behavior via lifecycle hooks

**Does NOT**:
- ❌ Manage job queues
- ❌ Handle authentication/authorization
- ❌ Process webhooks or events
- ❌ Spawn job containers (in target architecture - worker does this)

**Lifecycle Phases**:
1. `PRE_VALIDATION` - Before configuration validation
2. `POST_VALIDATION` - After configuration validation
3. `PRE_SOURCE_PREP` - Before source checkout/preparation
4. `POST_SOURCE_PREP` - After source is ready
5. `PRE_CONTAINER` - Before container execution
6. `POST_CONTAINER` - After container completes
7. `ON_ERROR` - When errors occur
8. `CLEANUP` - Final cleanup

**Key Features**:
- **Secret Masking**: Value-based secret masking with runtime registration via Unix socket
- **Git Operations**: File change detection, repository info, commit/tag queries
- **Hierarchical Configuration**: Defaults → Environment Variables → CLI Arguments
- **Dry-Run Mode**: Preview execution without running

See `runnerlib/DESIGN.md` for detailed runnerlib architecture.

### 2. Coordinator API (Go)

**Role**: Central orchestration hub for job management

**Responsibilities**:
- Process incoming events from VCS providers (webhooks)
- Authenticate users and manage access control
- Determine which jobs to run based on project configuration
- Submit jobs to the task queue (Corndogs)
- Manage job state and metadata in PostgreSQL
- Update VCS with job status (pending, success, failure)
- Provide REST API for job submission and monitoring
- Store and serve job logs
- Manage object storage for artifacts

**Key Capabilities**:

#### Event Processing
- **Webhook Ingestion**: Receive events from GitHub, GitLab, etc.
- **Polling Support**: Fallback for systems without webhooks
- **Event Filtering**: Determine relevance (branch, PR, tag, push)
- **Configuration Resolution**: Match events to project job definitions

#### Authentication & Authorization
- **Token-based Auth**: API token management for users
- **Project Access Control**: Users have permissions for specific projects
- **Branch Protection**: Rules for what jobs can run on what branches
- **Event Authorization**: Verify webhook signatures from VCS providers

#### Job Configuration Management
- **Project Registry**: Store project configurations (repos, branches, job definitions)
- **Job Templates**: Reusable job definitions with parameter substitution
- **Secret Management**: Secure storage and injection of secrets into jobs
- **Environment Presets**: Common environment variable sets

#### Job Orchestration
- **Queue Submission**: Submit jobs to Corndogs task queue
- **Priority Management**: High-priority jobs (main branch) vs low-priority (PRs)
- **Job Dependencies**: Multi-step workflows with dependencies
- **Retry Logic**: Automatic retry with configurable policies
- **Timeout Handling**: Job timeout enforcement

#### VCS Integration
- **Status Updates**: Post job status back to GitHub/GitLab
- **Commit Status**: Mark commits as pending/success/failure
- **PR Comments**: Post job results as PR comments
- **Branch Protection**: Prevent merges until jobs pass

**Interfaces**:
- **REST API**: Submit jobs, query status, retrieve logs
- **Webhook Endpoints**: Receive events from VCS providers
- **gRPC (Corndogs)**: Submit/monitor tasks in the queue
- **PostgreSQL**: Store job metadata, state, and configuration
- **Object Storage**: Store logs and artifacts (S3, MinIO, GCS, filesystem)

**Does NOT**:
- ❌ Execute job code directly
- ❌ Manage container lifecycle
- ❌ Perform git operations or source checkout (job containers do this)

**Data Model**:

```
Project:
  - id, name, vcs_url, default_branch
  - configuration (what jobs to run, when)

Job:
  - id, project_id, status, created_at, updated_at
  - git_url, git_ref, git_commit
  - job_command, job_image, job_env
  - priority, timeout, retry_count
  - logs_url, artifacts_url

User:
  - id, username, email
  - api_tokens

ProjectPermission:
  - user_id, project_id, role (read, write, admin)
```

### 3. Worker (Go)

**Role**: Minimal, non-prescriptive container lifecycle manager

**Philosophy**: The worker is **intentionally minimal** and **not prescriptive** about how jobs run. It simply:
1. Gets a job definition from the queue
2. Creates a workspace
3. Injects environment variables
4. Spawns the container with the specified image and command
5. Monitors and logs output
6. Cleans up

The worker does **NOT** dictate how source code is prepared, what tools are used, or how the job executes. This flexibility enables:
- Using runnerlib (default/recommended path)
- Bringing your own CI/CD tooling in a custom docker image
- Running truly weird/custom scenarios (lab equipment control, specialized binaries, etc.)

**Responsibilities**:
- Poll Corndogs task queue for available jobs
- Prepare workspace directory for job execution
- Spawn job containers with proper mounts and environment
- Monitor container execution and enforce resource limits
- Stream logs to Coordinator API
- Handle job completion and cleanup
- Report job status updates throughout lifecycle
- **Listen for workflow triggers** (next jobs to run)

**Execution Flow**:

```
1. Poll Corndogs → Get job task
2. Create workspace: /tmp/jobs/<job-id>/
3. Prepare environment:
   - Write job.env file (environment variables, secrets)
   - Create expected directory structure
4. Spawn job container:
   - Use specified image (from job definition)
   - Mount workspace → /job (in container)
   - Inject environment variables via --env-file
   - Run specified command (from job definition)
5. Monitor execution:
   - Stream stdout/stderr
   - Track exit code
   - Enforce timeout
   - Watch for workflow triggers
6. Handle completion:
   - Process any triggered follow-up jobs
   - Update job status
7. Cleanup:
   - Stop container
   - Remove workspace (or preserve for debugging)
```

**Container Execution Examples**:

**Docker/Nerdctl (VM/Laptop Deployment)**:

Using Runnerlib:
```bash
docker run --rm \
  -v /tmp/jobs/123/:/job \
  -e JOB_ID=123 \
  -e GIT_URL=https://github.com/user/repo.git \
  -e GIT_REF=main \
  --env-file /tmp/jobs/123/job.env \
  reactorcide/runner:latest \
  python -m runnerlib.cli run --git-url $GIT_URL --git-ref $GIT_REF --job-command "make test"
```

Custom CI/CD Tool:
```bash
docker run --rm \
  -v /tmp/jobs/456/:/workspace \
  --env-file /tmp/jobs/456/job.env \
  mycompany/custom-ci:v2 \
  /usr/local/bin/my-ci-tool --config /workspace/ci-config.yaml
```

Specialized Use Case:
```bash
docker run --rm \
  -v /tmp/jobs/789/:/data \
  --device=/dev/usb0 \
  --env-file /tmp/jobs/789/job.env \
  lab-equipment/controller:latest \
  /bin/control-device --secret $LAB_AUTH_TOKEN --upload-results
```

**Kubernetes (K8s Deployment)**:

Using Runnerlib:
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: reactorcide-job-123
  namespace: reactorcide-jobs
spec:
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: job
        image: reactorcide/runner:latest
        command:
          - python
          - -m
          - runnerlib.cli
          - run
          - --git-url
          - https://github.com/user/repo.git
          - --git-ref
          - main
          - --job-command
          - make test
        env:
        - name: JOB_ID
          value: "123"
        envFrom:
        - secretRef:
            name: job-123-secrets
        volumeMounts:
        - name: workspace
          mountPath: /job
      volumes:
      - name: workspace
        emptyDir: {}
```

The worker interacts with the Kubernetes API to create these Job resources instead of calling docker directly. The conceptual model remains the same: spawn container, monitor execution, capture logs.

**Key Features**:
- **Simple Binary**: No Python dependency, minimal resource footprint
- **Concurrent Jobs**: Handle multiple jobs in parallel
- **Runtime Agnostic**: Supports Docker, nerdctl, or Kubernetes Jobs
- **Resource Limits**: Enforce CPU/memory limits on containers
- **Error Handling**: Robust error handling with detailed logging
- **Health Monitoring**: Report worker health to Coordinator
- **Workflow Support**: Handle chained job execution

**Container Runtime Modes**:
1. **Docker**: Direct docker socket access (VM/laptop deployments)
2. **Nerdctl**: Containerd-based execution (rootless, more secure)
3. **Kubernetes**: Spawn Kubernetes Job resources via K8s API (K8s deployments)

The worker abstracts these differences - the same Go binary works in all three modes based on configuration.

**Configuration**:
- **Corndogs Connection**: gRPC endpoint for task queue
- **Coordinator API**: REST endpoint for status updates
- **Worker Pool**: Number of concurrent job slots
- **Workspace Root**: Where to create job directories (for Docker/nerdctl)
- **Container Runtime**: Execution mode - docker, nerdctl, or kubernetes
- **Kubernetes Config** (when runtime=kubernetes):
  - Kubeconfig path or in-cluster authentication
  - Job namespace
  - Service account for spawned jobs
  - Resource limits/requests defaults
  - TTL for completed jobs
  - Volume configurations

**Does NOT**:
- ❌ Make decisions about which jobs to run (Coordinator does)
- ❌ Authenticate users or manage access control
- ❌ Checkout source code (job container does this, if needed)
- ❌ Provide job utilities or lifecycle hooks (runnerlib does, if used)
- ❌ Dictate how jobs execute (fully flexible)

## Component Integration

### End-to-End Flow: GitHub PR Event

```
1. Developer opens PR on GitHub
   │
   ▼
2. GitHub sends webhook to Coordinator API
   │
   ▼
3. Coordinator API:
   - Verifies webhook signature
   - Looks up project configuration
   - Determines jobs to run: ["test", "lint"]
   - Creates job records in PostgreSQL
   - Submits tasks to Corndogs queue
   - Updates GitHub with "pending" status
   │
   ▼
4. Worker polls Corndogs, gets "test" job
   │
   ▼
5. Worker:
   - Creates /tmp/jobs/456/
   - Writes job.env with environment variables and secrets
   - Spawns container: reactorcide/runner:latest
     - Mounts /tmp/jobs/456/ → /job
     - Passes command: python -m runnerlib.cli run --git-url <url> --git-ref <ref> --job-command "npm test"
   │
   ▼
6. Job Container (runnerlib inside container):
   - Runs: python -m runnerlib.cli run
   - Checks out source code: git clone to /job/src/
   - Executes job steps: npm test
   - Streams logs to stdout (worker captures)
   - Masks secrets in output
   - Determines if follow-up jobs should trigger
   │
   ▼
7. Worker:
   - Captures logs, ships to Coordinator API
   - Watches for workflow trigger messages
   - Gets exit code from container
   - Submits any triggered follow-up jobs to queue
   - Updates job status: "success" or "failure"
   - Cleans up workspace
   │
   ▼
8. Coordinator API:
   - Stores final job status
   - Updates GitHub PR with status
   - Posts job log URL as PR comment
```

### Integration Points

#### Runnerlib ↔ Worker
- **Invocation**: Worker spawns container with runnerlib installed (or any custom image)
- **Configuration**: Worker passes job config via environment variables and command-line args
- **Logs**: Worker captures stdout/stderr from container
- **Exit Code**: Worker gets success/failure from container exit code
- **Workflow Triggers**: Worker watches for workflow trigger messages from runnerlib

#### Coordinator ↔ Worker
- **Job Assignment**: Worker polls Corndogs for jobs submitted by Coordinator
- **Status Updates**: Worker reports status changes to Coordinator REST API
- **Log Shipping**: Worker streams logs to Coordinator object storage
- **Health**: Worker registers health status with Coordinator
- **Job Submission**: Worker can submit follow-up jobs triggered by workflows

#### Coordinator ↔ VCS Providers
- **Webhooks**: Coordinator receives events from GitHub/GitLab
- **Status Updates**: Coordinator posts job status to commits/PRs
- **Authentication**: Coordinator uses OAuth/tokens to access VCS APIs

#### Runnerlib ↔ Job Code
- **Library Import**: Job scripts import runnerlib for utilities
- **Lifecycle Hooks**: Job code registers custom plugins
- **Secret Registration**: Job code registers secrets via Unix socket
- **Git Utilities**: Job code queries git info, changed files, etc.

## Deployment Models

### 1. Local Laptop (Development/Emergency)

**Use Case**: Developer testing job locally or emergency debugging when infrastructure is down

```
Developer Machine
├── runnerlib (pip install)
├── job workspace (./job/)
└── docker daemon

$ python -m runnerlib.cli run \
    --git-url https://github.com/user/repo \
    --git-ref feature-branch \
    --job-command "make test"
```

**What Runs**: Only runnerlib
**Dependencies**: Python 3.11+, Docker, Git

### 2. Single VM (Small Teams)

**Use Case**: Small team or startup wanting simple deployment

**Via SSH Deployment** (`deploy-vm.sh`):

```
VM (Ubuntu/Debian)
├── PostgreSQL (docker container)
├── MinIO (docker container)
├── Coordinator API (docker container)
├── Worker (docker container)
└── Docker daemon

Prerequisites:
- SSH access
- Docker installed
- Ports 80/443 open

Deployment:
$ ./deploy-vm.sh --host vm.example.com --ssh-key ~/.ssh/id_rsa
```

Runnerlib deploys the system to the VM as a CI/CD job.

**What Runs**: Full stack (Coordinator + Worker + DB + Storage)
**Scale**: 1-50 jobs/hour

### 3. Kubernetes (Large Teams/Production)

**Use Case**: Large teams, high job volume, multi-tenant environments

**Via Helm Chart**:

```
Kubernetes Cluster
├── coordinator-api (Deployment)
│   ├── Replicas: 3
│   ├── HPA: 2-10 replicas
│   └── Service: LoadBalancer
├── workers (Deployment)
│   ├── Replicas: 5
│   ├── HPA: 3-20 replicas
│   └── Spawns Kubernetes Jobs for CI/CD execution
├── postgresql (StatefulSet)
│   └── PVC: 100GB
├── minio (StatefulSet)
│   └── PVC: 500GB
└── corndogs (optional, can be external)

Job Execution (dynamic):
├── reactorcide-job-123 (Kubernetes Job)
├── reactorcide-job-124 (Kubernetes Job)
└── reactorcide-job-125 (Kubernetes Job)
  (Created/deleted by workers as CI/CD jobs run)

Prerequisites:
- Kubernetes 1.25+
- kubectl access
- Helm 3+
- RBAC: Worker service account needs Job create/delete/watch permissions

Deployment:
$ helm install reactorcide ./helm/reactorcide \
    --set coordinator.replicas=3 \
    --set worker.replicas=5
```

**Architecture Note**: Worker pods are long-running (Deployment) but spawn short-lived Kubernetes Job resources for each CI/CD job execution. This provides clean isolation and leverages Kubernetes native scheduling.

**What Runs**: Distributed, auto-scaling architecture
**Scale**: 1000+ jobs/hour

### 4. Hybrid (Kubernetes + External Workers)

**Use Case**: Specialized build agents (macOS, GPU, ARM)

```
Kubernetes Cluster
├── Coordinator API (Deployment)
└── PostgreSQL, MinIO

External Workers (VMs, bare metal)
├── Worker binary (Go)
├── Connects to Coordinator API
└── Specialized hardware/OS
```

Workers can run anywhere with network access to Coordinator.

## Current State vs Target State

### Current Architecture (Transitional)

**Issues**:
- ❌ Double container nesting (worker container → spawns job container)
- ❌ Runnerlib spawns containers (should be worker's job)
- ❌ Worker depends on Python runtime
- ❌ Unclear separation between worker and runnerlib

**Current Flow**:
```
Worker (Go in container)
  → Calls: python -m runnerlib.cli run --git-url ... --runner-image ...
    → Runnerlib spawns docker container
      → Job container runs job code
```

### Target Architecture (Goal)

**Benefits**:
- ✅ Single container nesting (worker spawns job container only)
- ✅ Worker is pure Go binary (no Python, no git operations)
- ✅ Runnerlib is library inside job container
- ✅ Clear separation of concerns
- ✅ Kubernetes-native (worker spawns Kubernetes Jobs)
- ✅ Fully flexible (can run any docker image with any command)

**Target Flow**:
```
Worker (Go binary)
  → Creates workspace
  → Spawns docker container (runnerlib or custom image)
    → Container handles source preparation (if using runnerlib)
    → Job code executes with utilities available
    → Determines and triggers follow-up jobs (workflows)
```

## Security Model

### Untrusted Code Execution

**Problem**: How to run CI/CD jobs on untrusted PR code without exposing secrets?

**Solution**: Separate CI code from source code

```
Job Definition:
  ci_code:
    git_url: https://github.com/company/ci-definitions.git
    git_ref: main
    path: pipelines/test-pr.py

  source_code:
    git_url: https://github.com/attacker/fork.git
    git_ref: malicious-pr-branch

Execution Flow:
  1. Worker spawns container with runnerlib
  2. Runnerlib checks out CI code (trusted) → /job/ci/
  3. Runnerlib checks out source code (untrusted) → /job/src/
  4. Runnerlib executes: python /job/ci/pipelines/test-pr.py
  5. CI code has access to secrets
  6. Source code cannot modify CI code (separate directories)
  7. CI code controls what gets executed
```

**Security Properties**:
- ✅ PR code cannot steal secrets (no access to CI code)
- ✅ PR code cannot modify job behavior
- ✅ CI code controls what gets executed
- ✅ Source code is isolated in /job/src/

### Secret Management

**Runnerlib**:
- Automatic masking of secret values in logs
- Runtime secret registration via Unix socket
- Value-based masking (finds secrets anywhere)

**Coordinator**:
- Encrypted secret storage in PostgreSQL
- Secrets injected via environment variables
- Per-project secret namespaces

**Worker**:
- Receives secrets from Coordinator
- Passes to container via `--env-file`
- Secrets never logged

### Container Isolation

- No privileged containers
- No host network access
- Controlled mounts (only job workspace)
- No arbitrary volume mounts
- Resource limits (CPU, memory)
- Readonly root filesystem (where possible)

## Future Roadmap

### Container Registry
**Blocker for Kubernetes deployment**

- Build `reactorcide/runner` base images
- Publish to registry (Docker Hub, GHCR, private)
- Version tagging strategy
- Language variants (python3.11, node20, go1.21)

**Current State**: Images built locally, not pushed anywhere

### Polling Support
For VCS providers without webhooks:
- Coordinator polls repositories for changes
- Configurable poll intervals
- Efficient diff-based change detection

### Multi-Step Workflows
- DAG-based workflow definitions
- Conditional job execution
- Artifact passing between steps
- Parallel job execution

### Observability
- Structured logging with trace IDs
- Prometheus metrics for all components
- Job performance analytics
- Worker capacity monitoring

### Advanced VCS Integration
- Mercurial support
- SVN support
- Perforce support (via plugins)

### Kubernetes-Native Features
When deploying to Kubernetes, additional capabilities become available:
- **Sidecar Support**: Add sidecar containers to jobs (logging agents, proxies, service meshes)
- **Init Containers**: Pre-job setup containers
- **Volume Types**: Support for PVCs, ConfigMaps, Secrets, hostPath, etc.
- **Affinity/Tolerations**: Schedule jobs on specific node pools
- **Pod Security Policies**: Enforce security constraints
- **Network Policies**: Control job network access
- **Resource Quotas**: Enforce resource limits per project
- **Job Templates**: Reusable Kubernetes Job manifests with variable substitution

**Configuration Primitives**:
```yaml
# Example job configuration with Kubernetes-specific features
job:
  name: test-with-cache
  image: reactorcide/runner:latest
  command: "make test"

  kubernetes:
    sidecars:
      - name: cache-proxy
        image: memcached:latest

    init_containers:
      - name: setup-deps
        image: busybox
        command: ["sh", "-c", "echo 'preparing...'"]

    volumes:
      - name: cache
        persistentVolumeClaim:
          claimName: build-cache

    affinity:
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
          - matchExpressions:
            - key: workload-type
              operator: In
              values: ["ci-cd"]
```

## Key Files and Locations

### Runnerlib
- `runnerlib/src/cli.py` - CLI entrypoint
- `runnerlib/src/config.py` - Configuration system
- `runnerlib/src/plugins.py` - Plugin/lifecycle hook system
- `runnerlib/src/source_prep.py` - Git operations
- `runnerlib/src/container.py` - Container execution
- `runnerlib/DESIGN.md` - Detailed runnerlib design

### Coordinator API
- `coordinator_api/cmd/coordinator/main.go` - API server entrypoint
- `coordinator_api/internal/handlers/` - REST API handlers
- `coordinator_api/internal/store/` - Database operations
- `coordinator_api/internal/vcs/` - VCS integration (GitHub, GitLab)
- `coordinator_api/internal/worker/` - Worker coordination

### Worker
- `coordinator_api/internal/worker/` - Worker implementation
- `coordinator_api/internal/worker/job_processor.go` - Job execution

### Deployment
- `helm/reactorcide/` - Kubernetes Helm chart
- `docker-compose.yml` - Local development environment
- `deploy-vm.sh` - Single VM deployment script
- `deployment-plan.md` - Deployment strategy and roadmap

## Migration Strategy

### Phase 1: Current State (Complete)
- ✅ Runnerlib CLI and library functional
- ✅ Coordinator API with VCS webhooks
- ✅ Worker with Corndogs integration
- ✅ Docker Compose for local dev
- ✅ Basic secret masking

### Phase 2: Container Registry (Next)
- ⏳ Build reactorcide/runner base images
- ⏳ Publish to registry
- ⏳ Update worker to pull from registry
- ⏳ Version management

### Phase 3: Target Architecture
- ⏳ Move container spawning from runnerlib to worker
- ⏳ Remove Python dependency from worker container
- ⏳ Runnerlib becomes pure library in job containers
- ⏳ Worker becomes minimal, non-prescriptive (just spawns containers)
- ⏳ All source preparation happens inside job containers

### Phase 4: Advanced Features
- ⏳ Separate CI code repository support
- ⏳ Multi-step workflows
- ⏳ Kubernetes Job spawning (worker uses K8s API instead of docker)
- ⏳ Kubernetes-native features (sidecars, init containers, PVCs)
- ⏳ Advanced observability

## Testing Strategy

### Unit Tests
- Runnerlib: `pytest` with mocks for docker/git
- Coordinator: `go test` with database fixtures
- Worker: `go test` with mock job queue

### Integration Tests
- Full stack with Docker Compose
- Real git operations, real containers
- End-to-end webhook → job execution → status update

### Manual Testing
- Local runnerlib execution
- VM deployment via `deploy-vm.sh`
- GitHub/GitLab webhook integration

## Configuration Examples

### Project Configuration (Coordinator)

```yaml
projects:
  - name: my-app
    vcs_url: https://github.com/user/my-app.git
    default_branch: main

    jobs:
      - name: test
        events: [pull_request, push]
        branches: [main, develop, "feature/*"]
        job_image: reactorcide/runner:python3.11
        job_command: "pytest tests/"
        timeout: 600
        priority: 5

      - name: deploy
        events: [push]
        branches: [main]
        job_image: reactorcide/runner:latest
        job_command: "python deploy.py"
        secrets: [DEPLOY_KEY, AWS_SECRET]
        timeout: 1200
        priority: 10
```

### Job Definition (runnerlib)

```yaml
# job.yaml - can be passed to: python -m runnerlib.cli run-job job.yaml

job_command: "make test && make build"
code_dir: /job/src
job_env:
  NODE_ENV: test
  CI: "true"

git:
  url: https://github.com/user/repo.git
  ref: main

secrets:
  - name: API_KEY
    value: secret123  # Would normally come from secret manager
```

### Worker Configuration

**Docker/Nerdctl Mode** (VM/Laptop):
```yaml
# worker-config.yaml

corndogs:
  endpoint: corndogs.example.com:50051
  queue: ci-jobs

coordinator:
  api_url: https://coordinator.example.com
  api_token: ${COORDINATOR_TOKEN}

worker:
  pool_size: 5
  workspace_root: /tmp/reactorcide-jobs
  container_runtime: docker  # or nerdctl
  docker_socket: /var/run/docker.sock

logging:
  level: info
  format: json
```

**Kubernetes Mode**:
```yaml
# worker-config.yaml

corndogs:
  endpoint: corndogs.example.com:50051
  queue: ci-jobs

coordinator:
  api_url: https://coordinator.example.com
  api_token: ${COORDINATOR_TOKEN}

worker:
  pool_size: 10
  container_runtime: kubernetes

kubernetes:
  # Use in-cluster config when running as K8s pod
  in_cluster: true
  # Or specify kubeconfig path
  # kubeconfig: /home/user/.kube/config

  job_namespace: reactorcide-jobs
  service_account: reactorcide-job-runner

  # Default resource limits for spawned jobs
  default_resources:
    requests:
      cpu: "500m"
      memory: "512Mi"
    limits:
      cpu: "2000m"
      memory: "2Gi"

  # How long to keep completed jobs
  ttl_seconds_after_finished: 3600

  # Default volume for workspace
  workspace_volume:
    emptyDir: {}

logging:
  level: info
  format: json
```

## Glossary

- **Job**: A single container execution with one or more steps that share environment and filesystem (e.g., "run tests on PR #123")
- **Step**: A single command or operation within a job (e.g., "checkout", "test", "build")
- **Workflow**: A collection of multiple jobs with dependencies that may run in sequence or parallel
- **Job Code**: The scripts/configuration that define what the job does (can be separate from source code)
- **Source Code**: The application code being tested/built
- **Worker**: Minimal Go binary that spawns job containers and monitors execution
- **Runnerlib**: Python library providing job execution utilities, lifecycle hooks, and workflow orchestration
- **Coordinator**: Go API service that processes events, manages jobs, and handles authentication
- **Lifecycle Hook**: Plugin extension point in runnerlib execution phases
- **Task Queue**: Corndogs gRPC service for distributing jobs to workers
- **Workflow Trigger**: Message from runnerlib to worker indicating follow-up jobs to run

## References

- Project CLAUDE.md: `./CLAUDE.md`
- Runnerlib Design: `./runnerlib/DESIGN.md`
- Deployment Plan: `./deployment-plan.md`
- Helm Chart: `./helm/reactorcide/`
- Coordinator API: `./coordinator_api/`
- Runnerlib Source: `./runnerlib/src/`

---

**Document Status**: Living document, updated as architecture evolves
**Last Updated**: 2025-10-16
**Authors**: Catalyst Community
