# Reactorcide System Design

This document describes the implemented Reactorcide architecture. Use the
linked operator guides for installation procedures.

## Purpose

Reactorcide runs CI/CD jobs in isolated containers. It supports these use
cases:

- Run a job from a developer workstation.
- Run jobs on a single VM.
- Run jobs as Kubernetes Jobs.
- Start jobs from VCS webhooks.
- Run trusted CI definitions against source from an outside contributor.

The local and remote paths use the same job format. This makes a job easier to
test before you submit it to a coordinator.

## Components

### CLI

The `reactorcide` binary provides these functions:

- Run a job locally with `run-local`.
- Submit a job to a coordinator.
- read job logs.
- Manage API tokens, secrets, and secret grants.
- Start the coordinator.
- Start a worker.

### Coordinator

The coordinator is the control plane. It:

- Provides the REST API and the management API.
- Stores users, projects, jobs, workflows, workers, and access rules in
  PostgreSQL.
- Submits ready work to Corndogs.
- Enrolls workers and gives work to authenticated workers.
- Receives job logs and results from workers.
- Resolves approved secrets and VCS credentials.
- Processes supported VCS webhooks.
- Updates VCS statuses and pull-request comments.
- Evaluates workflow dependencies after each job result.

The coordinator is the only component that must connect to PostgreSQL,
Corndogs, and the object store.

### Worker

A worker is an execution agent. It:

- Enrolls with a coordinator.
- Advertises its operating system, architecture, and custom
  characteristics.
- Requests work from the coordinator.
- Starts and stops job containers.
- Sends logs, status, and results to the coordinator.

A worker does not connect directly to PostgreSQL, Corndogs, or the object
store.

The worker supports these execution backends:

- Docker
- containerd through nerdctl
- Kubernetes Jobs
- A VM backend on supported macOS and Windows builds

See [Worker Operation](./docs/workers.md) for enrollment, queue matching, and
worker administration.

### Runnerlib

Runnerlib is a Python library and command-line program in a job image. It:

- Prepares source and trusted CI checkouts.
- Runs a job command.
- Resolves local job configuration.
- Masks registered secret values in its output.
- Reads VCS event context.
- Evaluates repository job and workflow files.
- Writes or submits follow-up job triggers.

The worker owns the container lifecycle. Runnerlib operates in the job
container.

See [Runnerlib Design](./runnerlib/DESIGN.md).

### Web application

The web application provides the browser interface. It uses the coordinator
management API. The web application is optional.

See [UI Authentication and Authorization](./docs/ui-auth.md).

### Corndogs

Corndogs stores ready job leases. The coordinator uses its CSIL-RPC TCP
interface. Workers do not connect to Corndogs.

### PostgreSQL

PostgreSQL stores control-plane records. Database migrations run when the
coordinator starts. You can also run migrations with `reactorcide migrate`.

### Object store

The coordinator stores job logs in the configured object store.

Implemented backends are:

- `filesystem`
- `memory` for tests
- `s3`, including compatible S3 services

The Kubernetes filesystem backend uses ephemeral pod storage. Do not use it
for durable production logs. The GCS configuration keys are reserved, but a
GCS backend is not implemented.

## Control Flow

### Local job

1. The user runs `reactorcide run-local JOB_FILE`.
2. The CLI loads the job file and overlays.
3. The CLI creates a workspace.
4. The selected backend starts the job container.
5. The job runs against a bind mount or a requested Git checkout.
6. The CLI returns the job exit status.

`run-local` uses the host user for a direct bind mount by default. Use
`--as-runner`, `--user`, `run_as`, or `run_local` when the job needs a
different user.

### Remote job

1. A user, VCS event, or parent job creates a job in the coordinator.
2. The coordinator records the job and sends ready work to Corndogs.
3. An authenticated worker requests a matching lease.
4. The worker creates a clean workspace.
5. The worker gets approved secrets and VCS checkout credentials from the
   coordinator.
6. The worker starts a container or a Kubernetes Job.
7. The worker sends logs and state changes to the coordinator.
8. The coordinator stores the result and evaluates dependent workflow nodes.
9. The coordinator submits newly ready nodes.
10. The coordinator updates the VCS workflow status when applicable.

## Source and CI Separation

A job can have two Git sources:

- `SourceURL` and `SourceRef` identify the code that the job tests.
- `CISourceURL` and `CISourceRef` identify trusted CI content.

The normal paths are:

```text
/job/src   application source
/job/ci    trusted CI source
```

For a pull request from a fork, the application source URL points to the fork.
For an open or updated pull request, the CI source URL points to the upstream
repository at a trusted base commit. For a merged pull request, it points to the
trusted revision on the target branch.
This prevents the pull request from replacing its own CI definition.

This separation is not a complete sandbox. Source code and CI code can still
run in the same job container. Do not give a secret to a job that executes
untrusted source unless the job design makes that access acceptable. Use
separate jobs and secret grants to reduce the scope.

See [Security Model](./docs/security-model.md).

## Job Model

A job is one container execution. The canonical worker job fields include:

- Image and command
- Environment
- Source type, URL, and ref
- Code, job, and working directories
- Timeout
- Container user
- CPU and memory limits
- Runtime capabilities
- Worker characteristics

Job YAML can use a flat format for direct execution. Repository event files
can use a nested `job` block. See [Job Definition
Reference](./docs/job-definitions.md).

### Runtime capabilities

Capabilities request runtime features. They do not change the container user.

- `docker` gives a supported job access to a Docker-compatible runtime.
- `builder` starts a BuildKit sidecar or equivalent builder service.
- `gpu` requests GPU access where the backend supports it.

Use `run_as.user` when a job must use `root` or a numeric user.

## Workflow Model

A workflow contains jobs and dependency edges. The coordinator owns workflow
state and scheduling.

Runnerlib can create triggers with:

- Job name
- Dependencies
- A condition
- Environment
- Job configuration
- `for_each` values

The coordinator records all nodes. It submits only nodes whose dependencies
are ready. A blocked node does not occupy a worker.

Supported dependency conditions are:

- `all_success`
- `any_failed`
- `always`

See [Workflow Design](./docs/workflow-design.md).

## VCS Model

GitHub is the operational webhook provider. The GitLab client can parse
payloads and call provider APIs, but it does not set generic Reactorcide events.
GitLab webhook events do not start jobs in the current implementation.

A complete provider adapter:

- Validates its webhook.
- Converts provider events to Reactorcide event types.
- Supplies repository, push, and pull-request data.
- Updates commit status.
- Creates or updates pull-request comments.

Supported generic events are:

- `push`
- `pull_request_opened`
- `pull_request_updated`
- `pull_request_merged`
- `pull_request_closed`
- `tag_created`

Projects select accepted events and target branches. A project can use
provider-specific secret references for webhook validation and API access.

See [Connect a VCS Repository](./docs/vcs-setup.md).

## Secret Model

The local CLI can store encrypted secrets for local jobs. A deployed
coordinator stores encrypted secret values in PostgreSQL.

Job files contain references such as:

```text
${secret:path:key}
```

They do not contain secret values.

The coordinator authorizes a remote job with:

- Its built-in job or project scope.
- An explicit secret grant.

VCS credentials are system credentials. The coordinator and worker use them
for webhook validation, checkout, status, and comments. They are not normal
job environment variables.

See [Secrets](./docs/secrets.md) and [VCS Credentials and Secret
Grants](./docs/vcs-credentials-and-secret-grants.md).

## Worker Selection and Resources

A queue can require worker characteristics. A worker is eligible when all
required values match. Standard values include `os` and `arch`. Operators can
also define typed custom values.

Job resources and worker characteristics are separate:

- Characteristics select an eligible worker.
- Resources set CPU and memory requests or limits for the job.

## Deployment Shapes

### Developer workstation

Use the development compose stack for the full service set. Use `run-local`
when you only need local job execution.

### Single VM

The VM deployment uses Compose. It starts PostgreSQL, Corndogs, the
coordinator, and one worker. The worker uses the host Docker or containerd
runtime.

### Kubernetes

The Helm chart starts the coordinator and worker deployments. The worker uses
the Kubernetes backend and creates one Kubernetes Job for each Reactorcide
job. The chart can install PostgreSQL and Corndogs for evaluation. Use managed
PostgreSQL and durable S3 storage for production.

See [Installation and Deployment](./docs/getting-started.md).

## Trust Boundaries

Operators must preserve these boundaries:

- Only the coordinator can access database master keys.
- Only the coordinator connects to control-plane storage services.
- Workers use authenticated coordinator sessions.
- A webhook must have a project or deployment signing secret.
- A VCS API token must have only the permissions that Reactorcide needs.
- Fork source remains separate from trusted CI definitions.
- Secret grants must be narrow.
- A job with a host runtime socket has strong control of that runtime.
- A `root` job has more risk than a non-root job.

## Source of Truth

Use these documents with this design:

- [Documentation Index](./docs/README.md)
- [Runtime Behavior](./docs/runtime-behavior.md)
- [Worker Operation](./docs/workers.md)
- [Workflow Design](./docs/workflow-design.md)
- [Job Definition Reference](./docs/job-definitions.md)
- [VCS Credentials and Secret Grants](./docs/vcs-credentials-and-secret-grants.md)
