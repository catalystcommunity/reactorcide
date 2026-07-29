# Runnerlib

Runnerlib is the Python job-side library for Reactorcide. It prepares source,
runs job commands, evaluates repository workflow files, masks registered
secret values, and creates follow-up job triggers.

Runnerlib requires Python 3.13 or later.

## Set Up

```bash
uv sync
```

## Run Tests

```bash
uv run pytest
```

## Use the CLI

```bash
uv run runnerlib --help
uv run runnerlib run --help
uv run runnerlib eval --help
```

## Documentation

- [Design](./DESIGN.md)
- [Usage](./docs/usage.md)
- [Plugin Development](./docs/plugin_development.md)
- [Job Definition Reference](../docs/job-definitions.md)
- [Writing Pipelines](../docs/writing-pipelines.md)
