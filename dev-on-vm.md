# Development on VM - Quick Iteration Loop

## Overview

This document describes the development workflow for making changes to Reactorcide components and deploying them to a VM for testing.

## Architecture

### Repository Structure
- **Source Code**: `runnerlib/job/coordinator_api/` - Go source files
- **Built Binary**: `runnerlib/job/coordinator-api` - Compiled binary (output)
- **Dockerfile**: `runnerlib/job/Dockerfile.coordinator` - Packages binary in Alpine
- **Deployment Script**: `deploy-with-reactorcide.sh` - Orchestrates the deployment

### Build Flow
```
coordinator_api/ (source)
    ↓ go build
coordinator-api (binary)
    ↓ docker build
reactorcide-coordinator:latest (image)
    ↓ deploy-vm.sh
VM deployment
```

## Quick Development Loop

### 1. Setup Environment Variables

Create your deployment configuration:
```bash
# Copy example config
cp deploy.config.example ~/my-reactorcide-vm.config

# Edit with your values
vim ~/my-reactorcide-vm.config

# Source it
source ~/my-reactorcide-vm.config
```

Example config (`~/my-reactorcide-vm.config`):
```bash
export REACTORCIDE_DEPLOY_HOST="user@hostname"
export REACTORCIDE_DEPLOY_USER="user"
export REACTORCIDE_DEPLOY_DOMAINS="reactorcide.example.local"
```

### 2. Make Code Changes

Edit any files in `runnerlib/job/coordinator_api/`:
```bash
vim runnerlib/job/coordinator_api/internal/handlers/job_handler.go
```

### 3. Deploy Changes

The deployment script automatically:
1. Builds the Go binary
2. Packages it in Docker
3. Deploys to VM

```bash
source ~/my-reactorcide-vm.config
./deploy-with-reactorcide.sh
```

### 4. Monitor Deployment

Check service status:
```bash
ssh ${REACTORCIDE_DEPLOY_HOST} 'cd ~/reactorcide && docker compose -f docker-compose.prod.yml ps'
```

View logs:
```bash
ssh ${REACTORCIDE_DEPLOY_HOST} 'cd ~/reactorcide && docker compose -f docker-compose.prod.yml logs -f coordinator-api'
```

## Key Points

### Automatic Binary Build
The deployment script now **always rebuilds** the coordinator binary to ensure latest changes are deployed:
```bash
# From deploy-with-reactorcide.sh
(cd runnerlib/job/coordinator_api && CGO_ENABLED=0 go build -o ../coordinator-api .)
```

### Binary Location
- **Source**: `runnerlib/job/coordinator_api/` (directory with .go files)
- **Output**: `runnerlib/job/coordinator-api` (compiled binary)
- The Dockerfile expects the binary at `runnerlib/job/coordinator-api`

### Environment Variables
All Reactorcide configuration uses `REACTORCIDE_` prefix to avoid conflicts:
- `REACTORCIDE_DEPLOY_HOST` - SSH connection string
- `REACTORCIDE_DEPLOY_USER` - Username on VM
- `REACTORCIDE_DEPLOY_DOMAINS` - Comma-separated domain list
- `REACTORCIDE_DB_PASSWORD` - Optional, generated if not set
- `REACTORCIDE_JWT_SECRET` - Optional, generated if not set

## Troubleshooting

### Build Fails
```bash
# Check Go is installed
go version

# Try building manually
cd runnerlib/job/coordinator_api
CGO_ENABLED=0 go build -o ../coordinator-api .
```

### Deployment Fails
```bash
# Check SSH access
ssh ${REACTORCIDE_DEPLOY_HOST} 'echo "Connection OK"'

# Check environment variables
env | grep REACTORCIDE_

# Run with dry-run
./deploy-with-reactorcide.sh --dry-run
```

### Service Not Starting
```bash
# Check service logs
ssh ${REACTORCIDE_DEPLOY_HOST} 'cd ~/reactorcide && docker compose -f docker-compose.prod.yml logs coordinator-api'

# Check service health
ssh ${REACTORCIDE_DEPLOY_HOST} 'curl -s http://localhost:8080/api/v1/health'
```

## Development Workflow Diagram

```
┌─────────────────┐
│ Edit Source Code│
│ coordinator_api/│
└────────┬────────┘
         │
         ▼
┌─────────────────────────┐
│ ./deploy-with-          │
│ reactorcide.sh          │
│                         │
│ 1. Builds binary        │
│ 2. Creates Docker image │
│ 3. Deploys to VM        │
└────────┬────────────────┘
         │
         ▼
┌─────────────────────────┐
│ VM: docker-compose      │
│ restarts services       │
└────────┬────────────────┘
         │
         ▼
┌─────────────────────────┐
│ Test changes            │
│ ssh + curl/logs         │
└─────────────────────────┘
```

## Future Improvements

- [ ] Add watch mode for automatic rebuild on file changes
- [ ] Local docker-compose for testing before VM deployment
- [ ] Faster incremental builds
- [ ] Multi-stage Dockerfile to build inside Docker (no local Go needed)
