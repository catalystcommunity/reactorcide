# Reactorcide

A minimalist CI/CD system for serious engineering teams. Works with open source or business needs, with particular focus on security for outside contributions.

Run jobs from your laptop just as easily as from the full system. If your VCS provider is down, fine - it's just building blocks.

## Quick Start

### Running Jobs with reactorcide CLI

```bash
# Build and push all images to registry
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  reactorcide run-local --job-dir ./ ./jobs/build-all.yaml

# Deploy to VM (requires env overlay with target config)
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  reactorcide run-local \
  -i ~/.config/reactorcide/reactorcide-vm-deploy-local.yaml \
  --job-dir ./ \
  ./jobs/deploy-to-vm.yaml
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
