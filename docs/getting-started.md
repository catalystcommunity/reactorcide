# Installation and Deployment

This guide helps you select and install a Reactorcide deployment.

## Select a Deployment

| Use case | Deployment | Result |
|---|---|---|
| Test one job on a workstation | `run-local` | One local job container |
| Develop Reactorcide | Development Compose stack | Coordinator, worker, web application, PostgreSQL, and Corndogs |
| Operate one small installation | VM deployment job | Compose control plane and one host-runtime worker |
| Operate in a cluster | Helm chart | Kubernetes control plane and Kubernetes Job worker |

Read [Runtime Behavior](./runtime-behavior.md) before you depend on a specific
container user, path, or runtime capability.

## Build the CLI

The coordinator module requires the Go version in
`coordinator_api/go.mod`. Build the CLI from the repository root:

```bash
cd coordinator_api
go build -o reactorcide .
cd ..
```

Use `./coordinator_api/reactorcide` in the commands below. You can copy it to a
directory on `PATH` if required.

## Run One Local Job

You need Docker, or containerd with nerdctl. `run-local` uses Docker by
default. Add `--backend containerd` when you use nerdctl.

Run an example:

```bash
./coordinator_api/reactorcide run-local \
  --job-dir ./ \
  ./examples/jobs/hello-world.yaml
```

The command bind-mounts the repository and uses the host user by default. Use
`--as-runner` for deployed-worker user parity.

Run against a different Git source:

```bash
./coordinator_api/reactorcide run-local \
  --code-url https://github.com/example/project.git \
  --code-ref main \
  .reactorcide/jobs/test.yaml
```

## Start the Development Stack

This path is for repository development. Do not use the development
credentials in production.

Prerequisites:

- Go version from the Go modules
- Python 3.13 or later
- `uv`
- Docker with Compose v2

Run:

```bash
./tools setup
./tools dev
```

The default endpoints are:

- Web application: `http://localhost:4080`
- Coordinator API: `http://localhost:8080`

Run tests with:

```bash
./tools test
```

## Install on a VM

The supported VM path is `jobs/deploy-to-vm.yaml`. It installs these services:

- PostgreSQL
- Corndogs
- Coordinator
- One worker

The current VM deployment does not install the web application. It exposes
the coordinator API on port `6080`. Put a TLS reverse proxy in front of this
port before you expose it to a VCS provider or the public internet.

### VM prerequisites

The local deployment machine needs:

- The Reactorcide CLI
- SSH access to the VM
- A local container runtime for `run-local`
- An initialized Reactorcide local secret store

The target VM needs:

- Linux
- Key-based SSH access
- `sudo` access for runtime configuration
- Docker with Compose v2, or containerd with nerdctl
- `curl`, `tar`, and OpenSSL
- Network access to the configured image registry

The deploy job currently uses `~/reactorcide` on the VM. Do not set
`REACTORCIDE_REMOTE_DIR` to a different path.

### Store deployment inputs

Initialize the local secret store. Use a password file. Do not put the
password in the command history.

```bash
./coordinator_api/reactorcide secrets init
chmod 600 ~/.reactorcide-pass
```

Store the SSH private key:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide secrets set --stdin \
  reactorcide/deploy ssh_private_key < ~/.ssh/id_ed25519
```

Create random database and JWT values without terminal output:

```bash
openssl rand -hex 24 | \
  REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide secrets set --stdin \
  reactorcide/deploy db_password

openssl rand -hex 32 | \
  REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide secrets set --stdin \
  reactorcide/deploy jwt_secret
```

The job definition requires these two secret references during input
resolution. The current VM script generates and preserves its own database and
JWT values in the VM `.env` file. It does not apply the two resolved input
values.

The job file has a GitHub webhook secret reference. Create a value even if you
do not enable VCS integration yet:

```bash
openssl rand -hex 32 | \
  REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide secrets set --stdin \
  reactorcide/deployment github_webhook_secret
```

Set the non-secret inputs:

```bash
export REACTORCIDE_DEPLOY_HOST="ci-vm.example.com"
export REACTORCIDE_DEPLOY_USER="reactorcide"
export REACTORCIDE_DEPLOY_DOMAINS="ci.example.com"
```

`REACTORCIDE_DEPLOY_DOMAINS` is currently informational. The deployment job
does not create a TLS route or reverse-proxy service.

Optional image overrides are:

- `REACTORCIDE_COORDINATOR_IMAGE`
- `REACTORCIDE_WORKER_IMAGE`
- `REACTORCIDE_RUNNER_IMAGE`

### Run the VM deployment

Run from the repository root:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide run-local \
  --job-dir ./ \
  ./jobs/deploy-to-vm.yaml
```

Add `--backend containerd` before `--job-dir` when the local deployment
machine uses nerdctl.

The job keeps existing database, JWT, and worker enrollment values on later
runs. It updates images and restarts the services.

### Verify the VM

```bash
curl --fail http://ci-vm.example.com:6080/api/v1/health
ssh reactorcide@ci-vm.example.com \
  'cd ~/reactorcide && source ./.docker-cmd && $DOCKER_CMD ps'
```

Use your TLS endpoint instead of direct port `6080` after you configure the
reverse proxy.

### Create an API token on the VM

The token command prints a new token one time. Redirect the token line to a
file on your workstation:

```bash
mkdir -p ~/.config/reactorcide
chmod 700 ~/.config/reactorcide
ssh reactorcide@ci-vm.example.com \
  'cd ~/reactorcide && source ./.docker-cmd && $DOCKER_CMD exec reactorcide-coordinator /reactorcide token create --name workstation' \
  | sed -n 's/^Token: //p' > ~/.config/reactorcide/api-token
chmod 600 ~/.config/reactorcide/api-token
```

Test the token:

```bash
REACTORCIDE_API_TOKEN="$(cat ~/.config/reactorcide/api-token)" \
  ./coordinator_api/reactorcide logs \
  --api-url https://ci.example.com \
  JOB_ID
```

Use [Connect a VCS Repository](./vcs-setup.md) after the API and TLS endpoint
are ready.

For a shared coordinator, configure organizations, limited tokens, worker
classes, and trusted CI policy after installation. See [Organizations and
Trusted CI Policy](./organizations-and-ci-policy.md). Review its upgrade checks
before you apply migrations 000025 through 000033 to an existing database.

## Install on Kubernetes

Use the [Kubernetes Deployment Guide](../helm_chart/DEPLOYMENT.md). The chart
defaults are a configuration reference. A default install is not a complete
production installation. You must select:

- PostgreSQL
- Corndogs
- A runner image
- Durable object storage for production
- An external route and TLS when you use webhooks

## Next Steps

1. Read [Security Model](./security-model.md).
2. Connect a repository with [VCS Setup](./vcs-setup.md).
3. Add `.reactorcide/workflows` and `.reactorcide/jobs` to the repository.
4. Configure only the secret grants that each job needs.
