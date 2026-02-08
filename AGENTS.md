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

## Project Status

The system is actively being developed with the core runnerlib functionality complete and the coordinator API providing job management capabilities. Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) to discuss and contribute.