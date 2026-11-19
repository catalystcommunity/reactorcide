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
- Request an OIDC token for the job (`runnerlib oidc-token`).

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

The CLI help also names `tarball`, `hg`, and `svn`. These types are stubs.
They are not implemented.

Runnerlib can prepare application source and CI source separately. Use this
for a trusted CI checkout and an untrusted pull-request checkout.

Runnerlib supports `isolated` and `shared` checkout modes for pull-request
evaluation. Isolated mode uses separate clones. Shared mode fetches the exact
base and head revisions into one non-shallow, blob-filtered object store. It
stages only `.reactorcide` files for the base and head CI views. Set the mode with
`REACTORCIDE_CHECKOUT_MODE`. The default is `isolated`.

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

`runnerlib run` gives the job command to the shell (`subprocess` with
`shell=True`). Thus shell syntax works in the command.

Eval wraps the `command` of each matched job as
`runnerlib run --job-command '<command>'`. Thus each triggered job prepares
its source through runnerlib before the command runs. Eval does not wrap a
command that starts with `runnerlib `. Set `raw_command: true` in the job to
send the command without the wrap. The wrap quotes the command as one
argument for the shell of the job: POSIX single quotes (each `'` becomes
`'"'"'`), or PowerShell single quotes (each `'` becomes `''`) for a job
with `os: windows` or a PowerShell `REACTORCIDE_JOB_SHELLCMD`. Thus a
command can contain single quotes.

## Lifecycle Hooks

Plugins can run at these phases:

1. `PRE_VALIDATION`
2. `POST_VALIDATION`
3. `PRE_SOURCE_PREP`
4. `POST_SOURCE_PREP`
5. `PRE_CONTAINER`
6. `POST_CONTAINER`
7. `ON_ERROR`
8. `CLEANUP`

`PRE_CONTAINER` and `POST_CONTAINER` are the phases before and after the job
command. The names are historical. Runnerlib does not start the outer job
container.

Plugins of a run with untrusted application source come only from the
trusted CI tree. If that tree has no plugins, no repository plugins load.
See [Plugin Development](./docs/plugin_development.md#plugin-loading).

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

Then eval applies the `if` block of each job (`paths`, `branches`, `events`,
`schedule`). A job whose `if` is false becomes a skipped node with reasons.
For the `schedule` event, eval evaluates only the workflow that
`REACTORCIDE_WORKFLOW_REF` names. The `on` and `paths` filters of that
workflow do not apply. See [Events, Checks, and
Schedules](../docs/events-and-schedules.md).

It resolves reusable `job_file` entries and creates complete job triggers.
For a pull request that changes CI files, eval returns inactive base and head
workflow candidates. The evaluation job receives a coordinator policy snapshot,
revision, and active approval snapshot. Triggered jobs inherit these values.
Runnerlib treats them as opaque values and does not evaluate them. The
coordinator loads current approvals, selects the allowed candidate, and applies
the execution authority.
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
- Capabilities, characteristics, resources, and `run_as`

Runnerlib creates one operation ID for each trigger payload. It uses the same
ID for the API request and the local fallback file. The coordinator can then
ignore a repeated submission of the same payload.

When a workflow job submits the payload, the coordinator attaches each new
workflow to the current workflow graph. Runnerlib does not calculate the graph
or its aggregate status.

The coordinator owns dependency evaluation and workflow state. Runnerlib only
creates requests and reads the context that the coordinator provides.

See [Writing Pipelines](../docs/writing-pipelines.md) and [Workflow
Design](../docs/workflow-design.md).

## OIDC Tokens

`runnerlib oidc-token --audience AUD` sends the job token to
`POST /api/v1/oidc/token` and prints only the signed token to stdout. With
`--output FILE`, it writes the token to a file with mode 0600. It needs
`REACTORCIDE_COORDINATOR_URL` and `REACTORCIDE_API_TOKEN`, and HTTPS unless
`--allow-insecure-transport` is set. It registers the token with the masker
when the masker runs. The Python API is `src.oidc.request_token`. See
[OIDC Tokens for Jobs](../docs/oidc.md).

## Extension Rules

Keep these rules when you add runnerlib behavior:

- Keep container lifecycle code in the worker.
- Keep coordinator state in the coordinator.
- Do not print secret values.
- Keep application source and CI source as separate inputs.
- Preserve `REACTORCIDE_CODE_DIR` and `REACTORCIDE_JOB_DIR`.
- Add focused tests under `runnerlib/tests/`.
