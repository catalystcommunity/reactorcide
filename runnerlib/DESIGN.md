# Runnerlib Design

Runnerlib is the job-side Python library for Reactorcide. The worker starts
and stops the job container. Runnerlib operates in that container.

See [the system design](../DESIGN.md) for the complete architecture.

## Responsibilities

Runnerlib provides these functions:

- Load job configuration from defaults, environment variables, and command
  arguments.
- Prepare application and trusted CI source.
- Run a job command.
- Mask registered secret values in output.
- Run lifecycle hooks.
- Evaluate repository job and workflow files.
- Create follow-up job triggers.
- Read and write workflow variables and outputs.

Runnerlib does not:

- Poll Corndogs.
- Enroll workers.
- Start the outer job container.
- Store coordinator records.
- Select a worker.

## Installation

Runnerlib requires Python 3.13 or later. The package metadata is in
`pyproject.toml`.

Install the development environment with:

```bash
uv sync
```

Run the tests with:

```bash
uv run pytest
```

The `runnerlib` command maps to `src.cli:app`.

## Configuration

Configuration precedence is:

1. Command-line arguments
2. `REACTORCIDE_*` environment variables
3. Defaults

Important paths are:

```text
/job              job workspace
/job/src          application source by default
/job/ci           trusted CI source by default
/job/artifacts    job artifacts by convention
/job/triggers.json
```

The worker sets `REACTORCIDE_IN_CONTAINER=true`. It also sets
`REACTORCIDE_CODE_DIR` and `REACTORCIDE_JOB_DIR`.

## Source Preparation

Implemented source types are:

- `none`
- `copy`
- `git`

Runnerlib can prepare application source and CI source separately. Use this
for a trusted CI checkout and an untrusted pull-request checkout.

For Git, runnerlib can configure these remotes from event variables:

- `origin` from `REACTORCIDE_HEAD_*`
- `upstream` from `REACTORCIDE_BASE_*`

Raw commands do not automatically prepare both remotes. A raw command must do
that work itself.

The worker can prepare short-lived Git credential files at:

```text
/job/.reactorcide/vcs-auth
```

Runnerlib removes these files after normal source preparation. See [VCS
Credentials and Secret Grants](../docs/vcs-credentials-and-secret-grants.md).

## Command Execution

`runnerlib run` prepares the configured source and runs one job command.

Examples:

```bash
runnerlib run --job-command "pytest"
runnerlib checkout
runnerlib eval --event-type pull_request_updated --branch main
```

Use `--help` on a command for its current arguments.

## Lifecycle Hooks

Plugins can run at these phases:

1. `PRE_VALIDATION`
2. `POST_VALIDATION`
3. `PRE_SOURCE_PREP`
4. `POST_SOURCE_PREP`
5. `PRE_EXECUTION`
6. `POST_EXECUTION`
7. `ON_ERROR`
8. `CLEANUP`

Some compatibility names use `PRE_CONTAINER` and `POST_CONTAINER`. They refer
to the execution phase. Runnerlib does not start the outer job container.

See [Plugin Development](./docs/plugin_development.md).

## Secret Masking

Runnerlib masks secret values that it knows. A process can register an
additional value through the runnerlib secret socket.

Masking reduces accidental disclosure in logs. It is not an authorization
control. A job process can read any secret that exists in its environment.
The coordinator must authorize each remote secret before it gives the value to
the worker.

## Event Evaluation

`runnerlib eval` reads:

- `.reactorcide/workflows/*.yaml`
- `.reactorcide/jobs/*.yaml`

Workflow files take precedence. If no workflow files exist, eval uses the
standalone job files as one compatibility workflow.

Eval matches:

- Generic event type
- Target branch glob
- Changed path include and exclude globs

It resolves reusable `job_file` entries and creates complete job triggers.
See [Job Definition Reference](../docs/job-definitions.md).

## Workflow Triggers

Job code can submit follow-up work through the coordinator API. The local file
protocol uses `/job/triggers.json` as a fallback.

A trigger can include:

- Job name and command
- Container image
- Source configuration
- Environment
- Dependencies
- Condition
- Priority and timeout
- `for_each` values

The coordinator owns dependency evaluation and workflow state. Runnerlib only
creates requests and reads the context that the coordinator provides.

See [Writing Pipelines](../docs/writing-pipelines.md) and [Workflow
Design](../docs/workflow-design.md).

## Extension Rules

Keep these rules when you add runnerlib behavior:

- Keep container lifecycle code in the worker.
- Keep coordinator state in the coordinator.
- Do not print secret values.
- Keep application source and CI source as separate inputs.
- Preserve `REACTORCIDE_CODE_DIR` and `REACTORCIDE_JOB_DIR`.
- Add focused tests under `runnerlib/tests/`.
