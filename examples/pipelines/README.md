# Reactorcide Pipeline Examples

This directory contains example pipeline scripts demonstrating various workflow patterns in Reactorcide.

## Overview

These examples show how to use the runnerlib workflow module (`src/workflow.py`) to create multi-step CI/CD pipelines where jobs can trigger follow-up jobs based on conditions, results, and state.

## Examples

### 1. Simple Test and Deploy (`simple_test_and_deploy.py`)

**Pattern**: Sequential pipeline with conditional logic

**What it does**:
- Runs tests
- If tests pass and on `main` branch, triggers deploy
- Otherwise, just reports success

**Key concepts**:
- Basic job triggering
- Branch-based conditions
- Using `git_info()` to get repository information

**Use case**: Simple CI/CD where you test every PR but only deploy from main

---

### 2. Complex Parallel Pipeline (`complex_parallel_pipeline.py`)

**Pattern**: Parallel jobs with dependencies and conditional deploy

**What it does**:
- Analyzes changed files to determine what needs to run
- Triggers test and lint jobs in parallel
- After both complete, triggers build job
- If on main branch, triggers deploy (it waits for build)

**Key concepts**:
- Parallel job execution
- Job dependencies (`depends_on`)
- Change detection with `changed_files()`
- Conditional job triggering

**Use case**: Production pipeline with parallel validation, artifact building, and a deploy from main

---

### 3. Context Manager Example (`context_manager_example.py`)

**Pattern**: Using context manager for cleaner code

**What it does**:
- Runs security scans
- Triggers different jobs based on branch type and tags
- Uses context manager for automatic trigger flushing

**Key concepts**:
- `workflow_context` context manager
- Automatic trigger flushing
- Tag-based triggering
- Multiple conditional paths

**Use case**: Security-first pipeline with different actions for releases, tags, and PRs

---

## Running Examples

### Locally (for testing)

You can run these scripts locally to see what jobs would be triggered:

```bash
cd /path/to/reactorcide
export REACTORCIDE_GIT_BRANCH=main
export REACTORCIDE_GIT_COMMIT=$(git rev-parse HEAD)
export REACTORCIDE_GIT_REF=refs/heads/main

python3 examples/pipelines/simple_test_and_deploy.py
```

The script shows the jobs that it triggers and writes `/job/triggers.json`.
Create the `/job` directory first, or change the script.

`ctx.branch`, `ctx.commit`, and `ctx.ref` come from the job environment:

| Property | Push | Pull request | Tag |
| --- | --- | --- | --- |
| `ctx.branch` | `REACTORCIDE_BRANCH` | the head branch (`REACTORCIDE_HEAD_REF`, then `REACTORCIDE_PR_REF`) | `None` |
| `ctx.commit` | `REACTORCIDE_SHA` | `REACTORCIDE_SHA` | `REACTORCIDE_SHA` |
| `ctx.ref` | `refs/heads/<branch>` | `refs/heads/<head branch>` | `refs/tags/<tag>` |

For a pull request, `ctx.branch` is the head branch, not the target branch.
A pull request into `main` does not give `main`. The head branch can be the
branch of a fork.

`REACTORCIDE_GIT_BRANCH`, `REACTORCIDE_GIT_COMMIT`, and `REACTORCIDE_GIT_REF`
replace these values when they are set. Use them for a local run, as in the
example above.

### Via Runnerlib

Run as a complete job with source checkout:

```bash
runnerlib run \
    --source-type git \
    --source-url https://github.com/user/repo.git \
    --source-ref main \
    --ci-source-type git \
    --ci-source-url https://github.com/user/repo.git \
    --ci-source-ref main \
    --job-command "python /job/ci/examples/pipelines/simple_test_and_deploy.py"
```

### Via Coordinator API

Submit as a job through the API (requires running Reactorcide system):

```bash
curl -X POST http://coordinator:6080/api/v1/jobs \
  -H "Authorization: Bearer $(cat ~/.config/reactorcide/api-token)" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "ingest-pipeline",
    "source_type": "git",
    "source_url": "https://github.com/user/repo.git",
    "source_ref": "main",
    "ci_source_type": "git",
    "ci_source_url": "https://github.com/user/repo.git",
    "ci_source_ref": "main",
    "runner_image": "containers.catalystsquad.com/public/reactorcide/runnerbase:dev",
    "job_command": "python /job/ci/examples/pipelines/simple_test_and_deploy.py"
  }'
```

## Creating Your Own Pipelines

### Basic Structure

In the runner image, the module is `src.workflow`. Use
`from src.workflow import ...` there. The examples below use the short form
`from workflow import ...`, which needs `runnerlib/src` on `sys.path`.

```python
#!/usr/bin/env python3
import sys
from workflow import trigger_job, flush_triggers, git_info

def main():
    # Get context
    info = git_info()

    # Do some work
    # ... run tests, build, etc ...

    # Trigger follow-up jobs
    trigger_job("next-job", env={"KEY": "value"})

    # Flush triggers to file
    flush_triggers()

    return 0

if __name__ == "__main__":
    sys.exit(main())
```

### Using Context Manager (Recommended)

```python
#!/usr/bin/env python3
import sys
from workflow import workflow_context

def main():
    with workflow_context() as ctx:
        # Get context information
        print(f"Branch: {ctx.branch}")
        print(f"Commit: {ctx.commit}")

        # Do some work
        # ... run tests, build, etc ...

        # Trigger follow-up jobs
        ctx.trigger_job("next-job", env={"KEY": "value"})

        # Triggers automatically flushed on exit

    return 0

if __name__ == "__main__":
    sys.exit(main())
```

## Key Functions

### Job Triggering

- `trigger_job(name, env={}, depends_on=[], condition="all_success", **kwargs)` - Trigger a follow-up job
- `flush_triggers()` - Write triggers to `/job/triggers.json`

### Git Utilities

- `git_info()` - Get branch, commit, tag, remote URL
- `changed_files(from_ref, to_ref)` - Get list of changed files

### Context Manager

- `workflow_context()` - Context manager that auto-flushes triggers

## Learn More

See the full documentation in
[`docs/writing-pipelines.md`](../../docs/writing-pipelines.md).
