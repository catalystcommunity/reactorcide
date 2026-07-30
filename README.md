# Reactorcide

Reactorcide is a small CI/CD system for ephemeral container or vm jobs. You can run
the same job on a workstation, a VM worker, or a Kubernetes worker.

Reactorcide supports trusted CI definitions for source from outside
contributors. It also supports named workflows, job dependencies, secret
grants, authenticated workers, and GitHub webhooks. It is not attached to git, but
git is the current implemented VCS, others may follow if needed.

## Choose a Starting Point

- Run one local job: [Installation and Deployment](./docs/getting-started.md)
- Install on a VM: [VM Installation](./docs/getting-started.md#install-on-a-vm)
- Install on Kubernetes: [Kubernetes Deployment](./helm_chart/DEPLOYMENT.md)
- Connect GitHub: [VCS Setup](./docs/vcs-setup.md)
- Write repository jobs: [Job Definition Reference](./docs/job-definitions.md)
- Understand the security boundary: [Security Model](./docs/security-model.md)

## Build the CLI

Use the Go version in `coordinator_api/go.mod`:

```bash
cd coordinator_api
go build -o reactorcide .
cd ..
```

## Run a Job Locally

You need Docker, or containerd with nerdctl. The default backend is Docker.
Add `--backend containerd` when you use nerdctl.

```bash
./coordinator_api/reactorcide run-local \
  --job-dir ./ \
  ./examples/jobs/hello-world.yaml
```

`run-local` bind-mounts `--job-dir` and uses the host user by default. Use
`--as-runner` to use the deployed runner user.

## Develop Reactorcide

You need Python 3.13 or later, `uv`, Go, and a container runtime.

```bash
./tools setup
./tools dev
./tools test
```

The development stack uses local development credentials. Do not use it for
production.

## Main Components

- `coordinator_api/`: CLI, REST API, workflow state, VCS integration, secrets,
  and worker protocol
- `runnerlib/`: Job-side Python library
- `webapp/`: Optional management web application
- `helm_chart/`: Kubernetes deployment
- `deployment/`: VM Compose deployment assets
- `jobs/`: Build, test, and deployment jobs for Reactorcide
- `examples/`: Job, pipeline, plugin, and API examples

## Implemented Deployment Model

The coordinator owns control-plane state. It connects to PostgreSQL,
Corndogs, and object storage. Authenticated workers request work from the
coordinator. Workers can use Docker, containerd, Kubernetes Jobs, or supported
VM backends.

See [System Design](./DESIGN.md).

## VCS Support

GitHub webhook events are operational. A GitLab client exists, but its webhook
event normalization is incomplete. GitLab events do not start jobs. Complete
the common event mapping before you use GitLab for CI triggers.

See [VCS Provider Integration](./coordinator_api/docs/vcs_integration.md).

## Documentation

Use the [Documentation Index](./docs/README.md) for all operator, job author,
and contributor guides.

## Project Status

Reactorcide is in active development. Review image tags, deployment defaults,
and security settings before production use.

Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) for
project discussion.
