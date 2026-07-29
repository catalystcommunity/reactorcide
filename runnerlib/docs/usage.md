# Runnerlib Usage

Runnerlib operates in a Reactorcide job container. The worker normally creates
that container. Developers can also run runnerlib directly for tests.

Runnerlib requires Python 3.13 or later.

## Install for Development

From `runnerlib/`:

```bash
uv sync
uv run runnerlib --help
```

## Commands

The CLI provides:

```text
run
checkout
run-job
copy
cleanup
config
validate
eval
trigger
git
```

Use command help for the current options:

```bash
uv run runnerlib run --help
uv run runnerlib checkout --help
uv run runnerlib eval --help
```

## Run a Command

In a prepared job container:

```bash
runnerlib run --job-command "pytest"
```

Runnerlib runs the command in the current container by default. The
`--container` option is for runnerlib integration tests that need an inner
container. Deployed workers do not use that option.

Pass the command after `--` when required:

```bash
runnerlib run -- python -m pytest
```

## Prepare Source

Run with Git source:

```bash
runnerlib run \
  --source-type git \
  --source-url https://github.com/example/project.git \
  --source-ref main \
  --job-command "make test"
```

Run with separate CI source:

```bash
runnerlib run \
  --source-type git \
  --source-url https://github.com/example/project.git \
  --source-ref feature \
  --ci-source-type git \
  --ci-source-url https://github.com/example/ci.git \
  --ci-source-ref main \
  --job-command "sh /job/ci/test.sh"
```

Implemented source types are `none`, `copy`, and `git`. Other names can appear
in compatibility options, but their preparation backends are not implemented.

## Checkout

Checkout one Git repository:

```bash
runnerlib checkout \
  https://github.com/example/project.git \
  --ref main \
  --code-dir /job/src
```

Normal remote jobs can receive temporary VCS checkout configuration from the
worker. Runnerlib removes it after source preparation.

## Paths

The default paths are:

```text
/job/src          application source
/job/ci           trusted CI source
/job/triggers.json
```

The worker sets:

```text
REACTORCIDE_CODE_DIR
REACTORCIDE_JOB_DIR
REACTORCIDE_IN_CONTAINER=true
```

Use the environment variables in reusable scripts. Do not assume that every
job uses the default path.

## Evaluate Repository Jobs

`eval` reads workflow and job files from the CI source directory. It matches
them against a VCS event and writes triggers.

```bash
runnerlib eval \
  --ci-source-dir /job/ci \
  --source-dir /job/src \
  --event-type pull_request_updated \
  --branch main \
  --source-url https://github.com/example/project.git \
  --source-ref COMMIT_SHA
```

The coordinator supplies these values in a normal VCS eval job.

See [Job Definition Reference](../../docs/job-definitions.md).

## Trigger Jobs

A job can use the Python workflow helpers:

```python
from src.workflow import flush_triggers, trigger_job

trigger_job(
    "test",
    container_image="python:3.13",
    job_command="pytest",
)
flush_triggers()
```

The coordinator API is the primary remote trigger path. The local file path is
`/job/triggers.json`.

See [Writing Pipelines](../../docs/writing-pipelines.md).

## Configuration

Configuration precedence is:

1. CLI argument
2. `REACTORCIDE_*` environment variable
3. Default

Inspect resolved configuration:

```bash
uv run runnerlib config
```

Validate without execution:

```bash
uv run runnerlib validate
```

## Secrets

Runnerlib can mask configured secret values and values registered through its
secret socket. Masking does not prevent job code from reading a value that is
in its environment.

For local job execution, use Reactorcide secret references in the job YAML:

```yaml
environment:
  API_TOKEN: "${secret:example/service:api_token}"
```

Run the job through `reactorcide run-local` so the local secret provider
resolves the reference. Do not put a value in the YAML.

For remote jobs, the coordinator authorizes the reference before it sends the
value to the worker. See [VCS Credentials and Secret
Grants](../../docs/vcs-credentials-and-secret-grants.md).

## Plugins

Put repository lifecycle plugins in `.reactorcide/plugins`. Runnerlib finds
this directory after it prepares the source. In a fork pull request,
runnerlib uses plugins from the trusted CI source and does not load plugins
from the fork.

Set `--plugin-dir` to load an additional trusted plugin directory:

```bash
runnerlib run \
  --plugin-dir /job/ci/plugins \
  --job-command "make test"
```

See [Plugin Development](./plugin_development.md).

## Tests

Run all runnerlib tests:

```bash
uv run pytest
```

Run one test file:

```bash
uv run pytest tests/test_eval.py
```
