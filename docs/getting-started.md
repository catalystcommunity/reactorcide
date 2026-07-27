# Getting Started: VM Deployment

This guide covers deploying Reactorcide to a VM with a single command.

The management web UI is public-view by default (`REACTORCIDE_UI_AUTH_MODE=none`): no
login, anyone reaching it can browse public projects/jobs and gracefully cancel a runaway
job, but nothing else. Turn on LinkKeys login and role-based access if you need per-user
accounts, private projects, or self-service secret/webhook management from the browser —
see **[docs/ui-auth.md](./ui-auth.md)** for auth modes, environment variables, first-admin/
bootstrap setup, and the full permission matrix.

## Prerequisites

### Local Machine (where you run the deploy script)
- Go 1.21+ (to build the coordinator binary)
- SSH client with key-based authentication to the target VM
- rsync

### Target VM
- containerd with nerdctl, or Docker with Docker Compose v2
- SSH access (key-based authentication)
- Runtime access for the deployment user. Prefer nerdctl/containerd where available; Docker remains supported.

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
export REACTORCIDE_LOG_LEVEL="info"                  # Default: info
```

### 2. Run Deployment

The supported VM deploy is the `jobs/deploy-to-vm.yaml` reactorcide job, run
locally with the `reactorcide` CLI. Build the CLI, then run the job from the
repository root:

```bash
cd coordinator_api && go build -o reactorcide . && cd ..
./coordinator_api/reactorcide run-local --job-dir ./ ./jobs/deploy-to-vm.yaml
```

The job reads the `REACTORCIDE_DEPLOY_*` variables set above (plus an
`SSH_PRIVATE_KEY` for the target host) and will:
1. Copy the deployment files to the VM
2. Detect the container runtime and generate the compose override
3. Pull the Reactorcide images
4. Start all services (postgres, corndogs, coordinator, worker)
5. Run database migrations on startup and verify health

See `jobs/deploy-to-vm.yaml` for the full list of inputs. (For Kubernetes, see
`helm_chart/DEPLOYMENT.md` and `jobs/deploy-to-k8s.yaml`.)

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
| corndogs | 5080 | CSIL-RPC task queue |
| postgres | 5432 | Main database |
| worker | - | Job processor |

## Submitting a Job

With your API token, submit a job. `code_dir` defaults to `/job/src`, `job_dir` defaults to `code_dir`, and deployed workers run as the image runner uid unless `run_as_user` is set.

```bash
curl -X POST http://your-vm:6080/api/v1/jobs \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-job",
    "source_url": "https://github.com/your-org/your-repo.git",
    "source_ref": "main",
    "source_type": "git",
    "job_command": "echo Hello from Reactorcide",
    "run_as_user": "runner"
  }'
```

## Updating

To update an existing deployment, run the deploy job again:
```bash
./coordinator_api/reactorcide run-local --job-dir ./ ./jobs/deploy-to-vm.yaml
```

It pulls the latest images and restarts services with the new configuration.

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
nerdctl ps || docker ps
```
