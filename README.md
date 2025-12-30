# Reactorcide

A minimalist CI/CD system for serious engineering teams. Works with open source or business needs, with particular focus on security for outside contributions.

Run jobs from your laptop just as easily as from the full system. If your VCS provider is down, fine - it's just building blocks.

## Quick Start

### 1. Build the CLI

```bash
cd coordinator_api && go build -o reactorcide . && cd ..

# Optionally add to PATH
sudo cp coordinator_api/reactorcide /usr/local/bin/
```

### 2. Initialize Secrets Storage

```bash
# Initialize encrypted secrets vault
reactorcide secrets init

# Add secrets (e.g., for VM deployment)
reactorcide secrets set reactorcide/deploy ssh_private_key "$(cat ~/.ssh/id_rsa)"
```

### 3. Deploy to a VM

Create an overlay file at `~/.config/reactorcide/my-vm.yaml`:

```yaml
environment:
  REACTORCIDE_DEPLOY_HOST: "your-vm-hostname"
  REACTORCIDE_DEPLOY_USER: "your-username"
  REACTORCIDE_DEPLOY_DOMAINS: "reactorcide.example.com"
```

Run the deployment:

```bash
REACTORCIDE_SECRETS_PASSWORD="<your-secrets-password>" \
  reactorcide run-local \
  --job-dir ./ \
  -i ~/.config/reactorcide/my-vm.yaml \
  ./jobs/deploy-to-vm.yaml
```

### 4. Create an API Token

After deployment, create a token to authenticate with the API:

```bash
# For Docker Compose deployment
ssh your-vm-hostname "docker exec reactorcide-coordinator /reactorcide token create --name my-token"

# For Kubernetes deployment
kubectl exec -it deploy/reactorcide-coordinator -- /reactorcide token create --name my-token
```

Save the returned token - it cannot be retrieved again.

### 5. Use the API

```bash
# Check API health
curl http://your-vm-hostname:6080/api/v1/health

# List jobs (authenticated)
curl -H "Authorization: Bearer <your-token>" \
  http://your-vm-hostname:6080/api/v1/jobs
```

### Local Development

```bash
# Start local dev stack
docker compose up -d

# Run tests
./tools test

# Build Docker images locally (for development)
./tools docker-build
```

### Running Jobs Locally

```bash
REACTORCIDE_SECRETS_PASSWORD="<your-secrets-password>" \
  reactorcide run-local --job-dir ./ ./jobs/build-all.yaml
```

## Documentation

- **[DESIGN.md](./DESIGN.md)** - Complete system architecture and design principles
- **[AGENTS.md](./AGENTS.md)** - Implementation guidance for AI assistants and contributors
- **[runnerlib/DESIGN.md](./runnerlib/DESIGN.md)** - Detailed runnerlib architecture and API
- **[docs/](./docs/)** - Additional documentation

## Philosophy

- **Isolation First**: Run jobs from a known state in isolated containers
- **Configuration Flexibility**: System config and job config are separate
- **VCS Agnostic**: No hard ties to specific VCS providers
- **Local Development**: Run jobs from your laptop as easily as from the full system
- **Building Blocks**: Modular components that can be combined as needed
- **Security by Design**: Built with outside contributions and security in mind

## Components

- **reactorcide CLI** - Main binary for running jobs, managing secrets, serving API
- **runnerlib** - Python library for job execution inside containers
- **Coordinator API** - REST API for job management and orchestration
- **Worker** - Distributed job processing with Corndogs task queue

## Project Status

Active development. Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) to discuss and contribute.
