# Reactorcide Examples

This directory contains examples for the REST API, jobs, pipelines, runnerlib
plugins, and event evaluation.

## Start with a Job

Run the hello-world job:

```bash
../coordinator_api/reactorcide run-local \
  --job-dir ../ \
  ./jobs/hello-world.yaml
```

Run this command from `examples/`.

Job examples are in `jobs/`:

- `hello-world.yaml`
- `docker-test.yaml`
- `pr-from-fork.yaml`
- `with-secrets.yaml`

Use [Job Definition Reference](../docs/job-definitions.md) for the current
schema.

## API Clients

`api_client.go` and `api_client.py` show Bearer authentication, health checks,
job submission, job polling, cancellation, and deletion.

Set:

```bash
export REACTORCIDE_API_URL="https://ci.example.com"
export REACTORCIDE_API_TOKEN="$(cat ~/.config/reactorcide/api-token)"
```

Run:

```bash
go run api_client.go
python api_client.py
```

The current job API uses these source fields:

```json
{
  "source_type": "git",
  "source_url": "https://github.com/example/project.git",
  "source_ref": "main"
}
```

It also accepts trusted CI source fields, paths, timeout, priority, queue,
worker characteristics, and resources. See
`coordinator_api/internal/handlers/job_handler.go` for the request structure.

## Pipeline Examples

Python workflow examples are in `pipelines/`. Read
[Pipeline Examples](./pipelines/README.md) and [Writing
Pipelines](../docs/writing-pipelines.md).

## Plugin Examples

Runnerlib plugin examples are in `plugins/`. Read [Plugin
Development](../runnerlib/docs/plugin_development.md).

Plugins run in the runnerlib process. Load only trusted plugin code.

## Eval Example

`test_e2e.py` and `e2e_test_job.yaml` demonstrate job evaluation and trigger
output. `mock_corndogs.py` is a test helper. It is not a production Corndogs
server.

## Worker Flow

The coordinator submits ready jobs to Corndogs. An authenticated worker asks
the coordinator for a matching job. The worker does not poll Corndogs
directly.
