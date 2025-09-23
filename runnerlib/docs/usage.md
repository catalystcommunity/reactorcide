# Runnerlib Usage Guide

Runnerlib is a containerized job runner - it executes commands in isolated containers with proper secret handling and source preparation.

## Quick Start

### Prerequisites

- **Container Runtime** - Docker, containerd, nerdctl, or podman
- **Python 3.8+** - For running runnerlib
- **Git** - For repository operations

### Installation

```bash
# Clone the repository
git clone https://github.com/catalystcommunity/reactorcide.git
cd reactorcide/runnerlib

# Install dependencies
pip install -e .
# Or with uv:
uv pip install -e .

# Test the installation
python -m src.cli run --runner-image alpine:latest --job-command "echo 'Hello World'"
```

## Core Commands

### `run` - Execute Commands Directly

Execute a job in a container:

```bash
# Basic execution
python -m src.cli run \
  --runner-image alpine:latest \
  --job-command "echo 'Hello World'"

# With secrets file (secure environment variables)
python -m src.cli run \
  --runner-image node:18 \
  --job-command "npm test" \
  --secrets-file secrets.env

# Dry-run to preview execution
python -m src.cli run \
  --runner-image alpine:latest \
  --job-command "ls -la" \
  --dry-run
```

### `run-job` - Execute from Job Files

Run jobs defined in JSON or YAML files:

```bash
# Run a job file
python -m src.cli run-job my-job.yaml

# With secrets
python -m src.cli run-job my-job.yaml --secrets-file secrets.env

# Dry-run mode
python -m src.cli run-job my-job.yaml --dry-run
```

#### Job File Format

**JSON Example (`job.json`):**
```json
{
  "name": "test-job",
  "image": "node:18-alpine",
  "command": "npm test",
  "environment": {
    "NODE_ENV": "production",
    "LOG_LEVEL": "info"
  },
  "source": {
    "type": "git",
    "url": "https://github.com/user/repo.git",
    "ref": "main"
  }
}
```

**YAML Example (`job.yaml`):**
```yaml
name: build-job
image: golang:1.21
command: go test ./...
environment:
  GOOS: linux
  GOARCH: amd64
source:
  type: local
  path: .
```

### `checkout` - Clone Repository

Clone a git repository to the job workspace:

```bash
# Clone default branch
python -m src.cli checkout https://github.com/user/repo.git

# Clone specific branch/tag/commit
python -m src.cli checkout https://github.com/user/repo.git --ref main
python -m src.cli checkout https://github.com/user/repo.git --ref v1.2.3
```

### `copy` - Copy Local Directory

Copy a local directory to the job workspace:

```bash
python -m src.cli copy ./my-project
```

### Other Commands

```bash
# Show configuration
python -m src.cli config

# Validate setup
python -m src.cli validate

# Clean up job directories
python -m src.cli cleanup

# Git operations
python -m src.cli git files-changed --from-ref main
python -m src.cli git info
```

## Secure Secrets Management

### Using `--secrets-file`

The `--secrets-file` option provides secure secret injection without exposing them in process lists:

```bash
# Create a secrets file
cat > secrets.env << EOF
API_KEY=secret123
DATABASE_URL=postgresql://user:pass@localhost/db
NPM_TOKEN=npm_auth_token
EOF

# Use with run command
python -m src.cli run \
  --runner-image node:18 \
  --job-command "npm publish" \
  --secrets-file secrets.env

# Use with job file
python -m src.cli run-job build.yaml --secrets-file secrets.env
```

**Security Features:**
- Secrets loaded as environment variables inside container
- Secrets file also mounted read-only at `/run/secrets/env`
- **Never visible in process lists** (ps aux)
- Automatic masking in logs

## Configuration

### Environment Variables

```bash
export REACTORCIDE_CODE_DIR="/job/src"          # Code directory in container
export REACTORCIDE_JOB_DIR="/job/src"           # Working directory in container
export REACTORCIDE_JOB_COMMAND="npm test"       # Command to run
export REACTORCIDE_RUNNER_IMAGE="node:18"       # Container image
export REACTORCIDE_JOB_ENV="NODE_ENV=test"      # Environment variables
export REACTORCIDE_SECRETS_FILE="secrets.env"   # Secrets file path
```

### Configuration Hierarchy

Configuration is resolved in order (later overrides earlier):

1. **Defaults** → 2. **Environment Variables** → 3. **CLI Arguments**

## Examples

### Node.js Project

```bash
# Direct execution
python -m src.cli run \
  --runner-image node:18 \
  --job-command "npm test" \
  --secrets-file npm-secrets.env

# Using job file
cat > node-job.yaml << EOF
name: node-tests
image: node:18-alpine
command: npm ci && npm test
environment:
  NODE_ENV: test
  CI: true
EOF

python -m src.cli run-job node-job.yaml
```

### Python Project

```bash
# Run pytest
python -m src.cli run \
  --runner-image python:3.11 \
  --job-command "pytest tests/ -v" \
  --job-env "PYTHONPATH=/job/src"
```

### Go Project

```bash
# Build and test
python -m src.cli run \
  --runner-image golang:1.21 \
  --job-command "go test ./..." \
  --job-dir "/job/src"
```

### Shell Script in Container

```bash
# Run deployment script on remote host via SSH
cat > deploy.yaml << EOF
name: deploy-to-vm
image: ubuntu:22.04
command: |
  sh -c '
    apt-get update && apt-get install -y openssh-client &&
    echo "\${SSH_PRIVATE_KEY}" > /tmp/key &&
    chmod 600 /tmp/key &&
    ssh -i /tmp/key \${VM_HOST} "echo deployed"
  '
EOF

python -m src.cli run-job deploy.yaml --secrets-file vm-secrets.env
```

## Directory Structure

Runnerlib manages a workspace structure:

```
./job/                    # Job workspace (mounted as /job in container)
├── src/                  # Source code (from git or local copy)
├── work/                 # Working directory
└── config/               # Configuration files (if using job_env with files)
```

## Troubleshooting

### Common Issues

#### "Container runtime not found"
```bash
# Check available runtimes
which docker || which nerdctl || which podman

# Install Docker if needed
curl -fsSL https://get.docker.com | sh
```

#### "Missing required configuration: job_command"
```bash
# Provide via CLI
python -m src.cli run --job-command "echo test"

# Or via environment
export REACTORCIDE_JOB_COMMAND="echo test"
```

#### "Secrets file not found"
```bash
# Check file exists and has correct permissions
ls -la secrets.env
chmod 600 secrets.env
```

### Debug Commands

```bash
# Check configuration
python -m src.cli config

# Validate setup
python -m src.cli validate

# Dry-run to preview
python -m src.cli run --dry-run \
  --runner-image alpine:latest \
  --job-command "ls -la"

# Check git status
python -m src.cli git info
```

## Security Best Practices

1. **Never pass secrets as CLI arguments** - Use `--secrets-file` instead
2. **Set proper file permissions** - `chmod 600` on secret files
3. **Use `.gitignore`** - Add `*.secrets` and `*-secrets.env`
4. **Use specific image tags** - Avoid `:latest` in production
5. **Validate job sources** - Be careful with untrusted repositories

## Plugin System

Runnerlib supports plugins for extending functionality. See [plugin_development.md](plugin_development.md) for details on creating custom plugins.

## Getting Help

```bash
# Command help
python -m src.cli --help
python -m src.cli run --help
python -m src.cli run-job --help

# Validate configuration
python -m src.cli validate

# Preview execution
python -m src.cli run --dry-run
```