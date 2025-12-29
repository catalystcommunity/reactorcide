# Getting Started: VM Deployment

This guide covers deploying Reactorcide to a VM with a single command.

## Prerequisites

### Local Machine (where you run the deploy script)
- Go 1.21+ (to build the coordinator binary)
- SSH client with key-based authentication to the target VM
- rsync

### Target VM
- Docker and Docker Compose v2
- SSH access (key-based authentication)
- User with docker group membership (to run containers without sudo)

## Deployment

### 1. Set Environment Variables

Required:
```bash
export REACTORCIDE_DEPLOY_HOST="your-vm-hostname-or-ip"
export REACTORCIDE_DEPLOY_USER="your-ssh-user"
export REACTORCIDE_DEPLOY_DOMAINS="your-domain.com"
```

Optional (auto-generated if not provided):
```bash
export REACTORCIDE_DB_PASSWORD="your-db-password"
export REACTORCIDE_JWT_SECRET="your-jwt-secret"
```

Optional configuration:
```bash
export REACTORCIDE_REMOTE_DIR="~/reactorcide"        # Default: ~/reactorcide
export REACTORCIDE_WORKER_CONCURRENCY="2"            # Default: 2
export REACTORCIDE_WORKER_POLL_INTERVAL="5"          # Default: 5 seconds
export REACTORCIDE_LOG_LEVEL="info"                  # Default: info
```

### 2. Run Deployment

From the repository root:
```bash
bash deployment/deploy-vm.sh
```

The script will:
1. Build the coordinator binary locally
2. Copy all deployment files to the VM
3. Build Docker images on the VM
4. Start all services
5. Run database migrations
6. Verify the deployment

### 3. Create an API Token

SSH to your VM and create a token:
```bash
ssh your-user@your-vm
cd ~/reactorcide
docker compose -f docker-compose.prod.yml exec coordinator-api /reactorcide token create --name "my-token"
```

Save the generated token - it's only shown once.

### 4. Verify Installation

Check the API health:
```bash
curl http://your-vm:6080/api/v1/health
```

## Services

After deployment, these services will be running:

| Service | Port | Description |
|---------|------|-------------|
| coordinator-api | 6080 | REST API for job management |
| corndogs | 5080 | gRPC task queue |
| postgres | 5432 | Main database |
| postgres-corndogs | 5433 | Task queue database |
| worker | - | Job processor |

## Submitting a Job

With your API token, submit a job:

```bash
curl -X POST http://your-vm:6080/api/v1/jobs \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-job",
    "source_url": "https://github.com/your-org/your-repo.git",
    "source_ref": "main",
    "job_command": "echo Hello from Reactorcide"
  }'
```

## Updating

To update an existing deployment, simply run the deploy script again:
```bash
bash deployment/deploy-vm.sh
```

The script will rebuild images and restart services with the new code.

## Troubleshooting

### Check service logs
```bash
ssh your-user@your-vm
cd ~/reactorcide
docker compose -f docker-compose.prod.yml logs -f coordinator-api
docker compose -f docker-compose.prod.yml logs -f worker
```

### Check service status
```bash
docker compose -f docker-compose.prod.yml ps
```

### Restart services
```bash
docker compose -f docker-compose.prod.yml restart
```

### View all running containers
```bash
docker ps
```
