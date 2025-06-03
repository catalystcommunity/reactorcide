# Runnerlib Usage Guide

## Quick Start

### Prerequisites

- **nerdctl** - Container runtime ([installation guide](https://github.com/containerd/nerdctl))
- **Python 3.13+** - For running runnerlib

### Basic Usage

1. **Set up your job**:
```bash
# Set required environment variable
export REACTORCIDE_JOB_COMMAND="npm test"

# Or use CLI flag
runnerlib run --job-command "npm test"
```

2. **Prepare your code**:
```bash
# Clone from git
runnerlib checkout https://github.com/user/repo.git

# Or copy local directory
runnerlib copy ./my-project
```

3. **Run your job**:
```bash
runnerlib run
```

## Configuration

### Environment Variables

Set these in your shell or CI environment:

```bash
export REACTORCIDE_CODE_DIR="/job/src"          # Code directory (default)
export REACTORCIDE_JOB_DIR="/job/src"           # Job working directory
export REACTORCIDE_JOB_COMMAND="npm test"       # Command to run (required)
export REACTORCIDE_RUNNER_IMAGE="node:18"       # Container image
export REACTORCIDE_JOB_ENV="NODE_ENV=test"      # Job environment variables
```

### CLI Overrides

Override any environment variable via CLI:

```bash
runnerlib run \
  --job-command "pytest tests/" \
  --runner-image "python:3.11" \
  --job-env "DJANGO_SETTINGS_MODULE=test_settings"
```

### Configuration Hierarchy

Configuration is resolved in this order (later overrides earlier):

1. **Defaults** → 2. **Environment Variables** → 3. **CLI Arguments**

## Commands

### `run` - Execute Job

Execute a job in a container:

```bash
# Basic execution
runnerlib run

# With additional arguments passed to job command
runnerlib run --verbose --coverage

# With custom configuration
runnerlib run \
  --job-command "make test" \
  --runner-image "gcc:latest" \
  --job-dir "/job/build"

# Dry-run to preview execution
runnerlib run --dry-run
```

### `checkout` - Clone Repository

Clone a git repository to the code directory:

```bash
# Clone default branch
runnerlib checkout https://github.com/user/repo.git

# Clone specific branch/tag/commit
runnerlib checkout https://github.com/user/repo.git --ref main
runnerlib checkout https://github.com/user/repo.git --ref v1.2.3
runnerlib checkout https://github.com/user/repo.git --ref abc123

# With custom directory configuration
runnerlib checkout https://github.com/user/repo.git --code-dir "/job/source"
```

### `copy` - Copy Local Directory

Copy a local directory to the code directory:

```bash
# Copy current directory
runnerlib copy .

# Copy specific directory
runnerlib copy ./my-project

# With custom configuration
runnerlib copy ./src --code-dir "/job/app"
```

### `cleanup` - Remove Job Directory

Clean up the job directory:

```bash
# Basic cleanup
runnerlib cleanup

# Verbose cleanup (shows what's being removed)
runnerlib cleanup --verbose
```

### `config` - View Configuration

Display the resolved configuration:

```bash
# Show current configuration
runnerlib config

# Show configuration with overrides
runnerlib config --job-command "test" --runner-image "alpine:latest"
```

### `validate` - Validate Configuration

Validate configuration without execution:

```bash
# Validate current configuration
runnerlib validate

# Validate with custom settings
runnerlib validate --job-command "build" --runner-image "node:16"

# Skip file system checks
runnerlib validate --no-check-files
```

### `git` - Git Operations

#### `git files-changed` - Show Changed Files

```bash
# Show files changed from HEAD~1
runnerlib git files-changed HEAD~1

# Show files changed from main branch
runnerlib git files-changed main

# Show files changed from specific commit
runnerlib git files-changed abc123def

# Pipe to other commands
runnerlib git files-changed HEAD~1 | xargs grep "TODO"
```

#### `git info` - Repository Information

```bash
# Show repository information
runnerlib git info
```

## Environment Variables

### Job Environment Configuration

You can set job-specific environment variables in two ways:

#### 1. Inline Format

```bash
export REACTORCIDE_JOB_ENV="NODE_ENV=test
DEBUG=true
API_URL=https://api.test.com"
```

#### 2. File Format

Create an environment file in the job directory:

```bash
# Create environment file
mkdir -p ./job/config
cat > ./job/config/test.env << EOF
# Test environment configuration
NODE_ENV=test
DEBUG=true
API_URL=https://api.test.com
DATABASE_URL=postgresql://localhost/test_db
EOF

# Reference the file
export REACTORCIDE_JOB_ENV="./job/config/test.env"
```

**Security Note**: Environment files must be within the `./job/` directory for security.

### Environment File Format

Environment files support:
- Key=value pairs
- Comments (lines starting with `#`)
- Empty lines
- Values with spaces

```env
# Application configuration
APP_NAME=MyApp
APP_VERSION=1.0.0

# Database settings
DATABASE_URL=postgresql://localhost/myapp
DATABASE_POOL_SIZE=10

# Feature flags
ENABLE_FEATURE_X=true
DEBUG_MODE=false

# Secrets (will be masked in logs)
API_SECRET=super-secret-key
AUTH_TOKEN=jwt-token-here
```

## Examples

### Node.js Project

```bash
# Set up Node.js testing environment
export REACTORCIDE_JOB_COMMAND="npm test"
export REACTORCIDE_RUNNER_IMAGE="node:18-alpine"
export REACTORCIDE_JOB_ENV="NODE_ENV=test
CI=true"

# Clone and test
runnerlib checkout https://github.com/user/node-app.git
runnerlib run
```

### Python Project

```bash
# Python project with pytest
runnerlib run \
  --job-command "python -m pytest tests/ -v" \
  --runner-image "python:3.11" \
  --job-env "PYTHONPATH=/job/src
DJANGO_SETTINGS_MODULE=settings.test"
```

### Go Project

```bash
# Go project with custom build
runnerlib copy ./my-go-app
runnerlib run \
  --job-command "go test ./..." \
  --runner-image "golang:1.21" \
  --job-dir "/job/src"
```

### Multi-step Workflow

```bash
# 1. Prepare environment
mkdir -p ./job/config
echo "ENVIRONMENT=staging" > ./job/config/app.env

# 2. Get code
runnerlib checkout https://github.com/user/app.git --ref develop

# 3. Validate configuration
runnerlib validate \
  --job-command "make test" \
  --runner-image "ubuntu:22.04" \
  --job-env "./job/config/app.env"

# 4. Run with dry-run first
runnerlib run --dry-run \
  --job-command "make test" \
  --runner-image "ubuntu:22.04" \
  --job-env "./job/config/app.env"

# 5. Execute if dry-run looks good
runnerlib run \
  --job-command "make test" \
  --runner-image "ubuntu:22.04" \
  --job-env "./job/config/app.env"

# 6. Clean up
runnerlib cleanup --verbose
```

## Dry-Run Mode

The dry-run mode is perfect for debugging and validation:

```bash
# Preview what would be executed
runnerlib run --dry-run

# Check if container image is available
runnerlib run --dry-run --runner-image "custom:latest"

# Validate environment file parsing
runnerlib run --dry-run --job-env "./job/test.env"
```

### Dry-Run Use Cases

1. **CI/CD Pipeline Development** - Validate job configuration
2. **Environment Debugging** - Check variable resolution
3. **Container Image Testing** - Verify image availability
4. **Directory Structure Validation** - Ensure files are in place

## Troubleshooting

### Common Issues

#### "nerdctl is not available in PATH"

**Solution**: Install nerdctl container runtime:
```bash
# On Linux with curl
curl -fsSL https://github.com/containerd/nerdctl/releases/download/v1.7.0/nerdctl-1.7.0-linux-amd64.tar.gz | sudo tar -xz -C /usr/local/bin

# Or follow official installation guide
```

#### "Missing required configuration: job_command"

**Solution**: Set the job command:
```bash
export REACTORCIDE_JOB_COMMAND="your-command-here"
# OR
runnerlib run --job-command "your-command-here"
```

#### "Path traversal not allowed in job_env path"

**Solution**: Use paths within the job directory:
```bash
# ❌ Invalid
export REACTORCIDE_JOB_ENV="../../../etc/passwd"

# ✅ Valid
export REACTORCIDE_JOB_ENV="./job/config/app.env"
```

#### "Repository directory does not exist"

**Solution**: Set up your code first:
```bash
runnerlib checkout https://github.com/user/repo.git
# OR
runnerlib copy ./your-project
```

#### "Container image is NOT available"

**Solution**: Check image name and registry access:
```bash
# Test image availability
nerdctl pull your-image:tag

# Use a known image
runnerlib run --runner-image "alpine:latest"
```

### Debug Information

For troubleshooting, use these commands:

```bash
# Check configuration resolution
runnerlib config

# Validate setup
runnerlib validate --verbose

# Preview execution
runnerlib run --dry-run

# Check git repository
runnerlib git info

# Verify directory structure
runnerlib cleanup --verbose  # Shows what would be cleaned
```

### Getting Help

```bash
# Command help
runnerlib --help
runnerlib run --help
runnerlib git --help

# Validate your configuration
runnerlib validate

# See what would be executed
runnerlib run --dry-run
```

## Best Practices

### Security

1. **Use relative paths** for job environment files
2. **Store secrets securely** - they'll be masked in logs
3. **Validate configurations** before production use
4. **Use specific image tags** instead of `latest`

### Performance

1. **Use local container images** when possible
2. **Reuse job directories** for iterative development
3. **Use `.gitignore`** to avoid copying unnecessary files
4. **Clean up regularly** to free disk space

### Development Workflow

1. **Start with dry-run** to validate configuration
2. **Use validate command** during development
3. **Test with simple commands** first
4. **Use verbose flags** for debugging

### CI/CD Integration

```bash
#!/bin/bash
# Example CI script

set -e

# Validate environment
runnerlib validate --job-command "$JOB_COMMAND" --runner-image "$RUNNER_IMAGE"

# Get code
runnerlib checkout "$REPO_URL" --ref "$GIT_REF"

# Run job
runnerlib run --job-command "$JOB_COMMAND" --runner-image "$RUNNER_IMAGE"

# Cleanup
runnerlib cleanup
```