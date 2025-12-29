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
2. **Job Execution**: Use `python -m src.cli run` or submit jobs via the API
3. **Kubernetes Deployment**: Deploy with Helm chart for production use
4. **Integration**: Connect to existing Corndogs deployment or deploy alongside

## Project Status

The system is actively being developed with the core runnerlib functionality complete and the coordinator API providing job management capabilities. Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) to discuss and contribute.