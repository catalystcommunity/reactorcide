# Writing Pipelines in Reactorcide

This guide explains how to write multi-step CI/CD pipelines in Reactorcide using the workflow orchestration utilities.

## Table of Contents

- [Overview](#overview)
- [Core Concepts](#core-concepts)
- [Quick Start](#quick-start)
- [Workflow Functions](#workflow-functions)
- [Common Patterns](#common-patterns)
- [Advanced Usage](#advanced-usage)
- [Best Practices](#best-practices)
- [Examples](#examples)

## Overview

Reactorcide uses an **ingest pipeline pattern** where:

1. An initial "ingest" job analyzes the trigger (PR, push, tag, etc.)
2. The ingest job determines what work needs to be done
3. The ingest job triggers follow-up jobs with proper dependencies
4. Follow-up jobs execute in parallel or sequence based on dependencies
5. Any job can trigger additional jobs, creating dynamic workflows

This approach gives you **maximum flexibility** - workflows are just Python code, so you can use any logic, external APIs, or state checks to determine what runs next.

The workflow model is described in [Workflow Design](./workflow-design.md). It keeps `triggers.json`, but moves dependency waiting, state merging, skip reasons, and VCS reporting into coordinator-owned workflow records.

## Core Concepts

### Jobs vs Workflows

- **Job**: A single container execution with steps that share environment/filesystem
- **Workflow**: Multiple jobs with dependencies, evaluated by the coordinator from `triggers.json`
- **Ingest Job**: The initial job that analyzes triggers and schedules follow-up jobs

### Job Triggering

Jobs trigger follow-up jobs through runnerlib. Runnerlib writes
`/job/triggers.json` first. In a coordinator job, runnerlib then sends the
trigger data to the authenticated coordinator API. The coordinator records
workflow nodes, submits ready jobs, and leaves blocked jobs waiting without
occupying worker slots. If the authenticated API request fails, runnerlib
fails the job. A remote worker does not use the local file as a fallback.

The coordinator accepts trigger data only from the job token of the running
parent job. A user, a service token, or the admin token cannot submit
triggers for a job, and a finished job cannot submit triggers.

`run-local` can use the trigger file without API credentials.

Source code is available at `REACTORCIDE_CODE_DIR` (default `/job/src`). The job working directory is `REACTORCIDE_JOB_DIR` (defaulting to the code directory). Prefer these environment variables in reusable pipeline scripts so jobs continue to work when a definition customizes `code_dir` or `job_dir`.

**Trigger File Format**:
```json
{
  "type": "trigger_job",
  "operation_id": "<event id or random UUID>",
  "trigger_type": "runnerlib",
  "jobs": [
    {
      "job_name": "deploy",
      "depends_on": ["test", "build"],
      "condition": "all_success",
      "env": {
        "TARGET": "production"
      },
      "source_type": "git",
      "source_url": "https://github.com/user/repo.git",
      "source_ref": "main",
      "container_image": "reactorcide/runner:latest",
      "job_command": "make deploy",
      "priority": 10,
      "timeout": 1800,
      "for_each": ["us-east", "us-west"],
      "item_var": "REGION"
    }
  ]
}
```

### Dependencies and Conditions

- **depends_on**: List of job names that must complete first
- **condition**: When to run the job
  - `all_success` - Run only if all dependencies succeeded (default)
  - `any_failed` - Run if at least one dependency failed, timed out, or was cancelled
  - `always` - Run regardless of dependency results
- **for_each**: Literal list of values that expands one trigger into multiple workflow nodes
- **item_var**: Environment variable name for the current `for_each` value

## Quick Start

### Import the Workflow Module

The runner image installs runnerlib as the Python package `src`. In a job
that uses the runner image, import the module as `src.workflow`:

```python
from src.workflow import trigger_job, flush_triggers, WorkflowContext
```

The scripts in [`examples/pipelines/`](../examples/pipelines/) add
`runnerlib/src` to `sys.path` and use `from workflow import ...`. The
examples on this page use the short form too.

### Basic Pipeline Script

```python
#!/usr/bin/env python3
"""
Minimal pipeline: run tests, then deploy on main branch.
"""

import os
import sys
from workflow import trigger_job, flush_triggers, WorkflowContext

def main():
    ctx = WorkflowContext()

    # Run some work...
    print("Running tests...")
    # ... test execution logic ...

    # Trigger follow-up job conditionally
    if os.getenv("REACTORCIDE_BRANCH") == "main":
        trigger_job("deploy", env={"TARGET": "production"})

    # Write triggers to file
    flush_triggers()

    return 0

if __name__ == "__main__":
    sys.exit(main())
```

### Using Context Manager (Recommended)

```python
#!/usr/bin/env python3
"""
Same pipeline using context manager (cleaner).
"""

import sys
from workflow import workflow_context

def main():
    with workflow_context() as ctx:
        print("Running tests...")
        # ... test execution logic ...

        if ctx.branch == "main":
            ctx.trigger_job("deploy", env={"TARGET": "production"})

        # Triggers automatically flushed on exit

    return 0

if __name__ == "__main__":
    sys.exit(main())
```

## Workflow Functions

### Job Triggering

#### `trigger_job(job_name, env={}, depends_on=[], condition="all_success", **kwargs)`

Trigger a follow-up job.

**Parameters**:
- `job_name` (str): Name of the job to trigger
- `env` (dict): Environment variables for the job
- `depends_on` (list): Job names this job depends on
- `condition` (str): Triggering condition (`all_success`, `any_failed`, or `always`)
- `**kwargs`: Additional job configuration (source_url, container_image, etc.)

**Example**:
```python
trigger_job(
    "deploy-staging",
    env={"TARGET": "staging", "VERSION": "1.0.0"},
    depends_on=["test", "build"],
    condition="all_success",
    container_image="reactorcide/runner:latest",
    job_command="python deploy.py",
    priority=5,
    timeout=1800,
    for_each=["us-east", "us-west"],
    item_var="REGION",
)
```

#### `submit_job(job_name, env={}, **kwargs)`

Alias for `trigger_job()` for backward compatibility.

#### `flush_triggers()`

Write accumulated triggers to `/job/triggers.json`.

**Must be called** at the end of your script unless using context manager.

**Example**:
```python
trigger_job("test")
trigger_job("lint")
flush_triggers()  # Writes both triggers to file
```

### Workflow State

#### `set_workflow_var(key, value)`

Publish a workflow variable for later jobs. The coordinator merges the value after the current job completes.

```python
from workflow import set_workflow_var

set_workflow_var("image_tag", "abc123")
```

If two jobs write the same key with different JSON values, the workflow fails instead of choosing a winner implicitly.

#### `set_workflow_output(key, value)`

Publish a node-scoped output. The coordinator stores it as `<job_name>.<key>`.

```python
from workflow import set_workflow_output

set_workflow_output("image_digest", "sha256:...")
```

#### `workflow_vars()`

Load the current workflow variables. The function reads `RC_WF_VARS_JSON`
first. If that variable is not set, it reads the file at `RC_WF_VARS_FILE`
(default `/job/workflow-vars.json`). It returns an empty dictionary when
neither has a JSON object.

```python
from workflow import workflow_vars

vars = workflow_vars()
image_tag = vars.get("image_tag")
```

### Git Utilities

#### `changed_files(from_ref="HEAD^", to_ref="HEAD", base_dir="/job/src")`

Get list of files changed between two git refs.

**Parameters**:
- `from_ref` (str): Starting git ref (default: HEAD^)
- `to_ref` (str): Ending git ref (default: HEAD)
- `base_dir` (str): Git repository directory (default: /job/src)

**Returns**: List of file paths

**Example**:
```python
# Files changed in last commit
files = changed_files()

# Files changed in PR (from main to current branch)
files = changed_files("origin/main", "HEAD")

# Check if Python files changed
python_changed = any(f.endswith(".py") for f in files)
if python_changed:
    trigger_job("python-tests")
```

#### `git_info(base_dir="/job/src")`

Get git repository information.

**Parameters**:
- `base_dir` (str): Git repository directory (default: /job/src)

**Returns**: Dictionary with:
- `branch`: Current branch name
- `commit`: Full commit SHA
- `short_commit`: Short commit SHA (7 chars)
- `tag`: Current tag (if on a tag)
- `remote_url`: Remote origin URL

**Example**:
```python
info = git_info()

print(f"Building {info['commit']} on branch {info['branch']}")

if info['tag']:
    trigger_job("release", env={"TAG": info['tag']})
```

### Next Job Logging

#### `log_next_job(job_name, reason="")`

Log what job should run next (for local execution).

**Example**:
```python
log_next_job("deploy", reason="tests passed")
# Output: 📋 Next job to run: deploy (reason: tests passed)
```

### Context Manager

#### `workflow_context(triggers_file="/job/triggers.json")`

Context manager that automatically flushes triggers on successful exit.

**Benefits**:
- Automatic trigger flushing
- No flushing if exception occurs
- Cleaner code

**Example**:
```python
with workflow_context() as ctx:
    # Access context properties
    print(f"Job: {ctx.job_id}")

    # Trigger jobs
    ctx.trigger_job("test")
    ctx.trigger_job("lint")

    # Automatically flushed on exit
```

### Context Properties

When using `WorkflowContext` or `workflow_context`:

- `ctx.job_id` - Current job ID (`REACTORCIDE_JOB_ID`)
- `ctx.workflow_id` - Current workflow ID (`RC_WF_ID`)
- `ctx.branch` - The branch of the code under test
- `ctx.commit` - The commit SHA of the code under test
- `ctx.ref` - The full git ref of the code under test

These properties come from the job environment that the coordinator sets:

| Property | Push | Pull request | Tag |
| --- | --- | --- | --- |
| `ctx.branch` | `REACTORCIDE_BRANCH` | the head branch (`REACTORCIDE_HEAD_REF`, then `REACTORCIDE_PR_REF`) | `None` |
| `ctx.commit` | `REACTORCIDE_SHA` | `REACTORCIDE_SHA` | `REACTORCIDE_SHA` |
| `ctx.ref` | `refs/heads/<branch>` | `refs/heads/<head branch>` | `refs/tags/<tag>` |

For a pull request, `ctx.branch` is the head branch, not the target branch.
`REACTORCIDE_BRANCH` of a pull request job is the target branch. Thus a pull
request into `main` does not make `ctx.branch == "main"` true. The head
branch can be the branch of a fork: do not use it to give trust.

`REACTORCIDE_GIT_BRANCH`, `REACTORCIDE_GIT_COMMIT`, and `REACTORCIDE_GIT_REF`
replace these values when they are set (for example, in a local run). A
property is `None` when the job environment does not have the value.

**Example**:
```python
with workflow_context() as ctx:
    if ctx.branch and ctx.branch.startswith("release/"):
        ctx.trigger_job("release-validation")
```

## Common Patterns

### Pattern 1: Simple Test-Then-Deploy

Run tests on every commit, deploy only on main branch.

```python
with workflow_context() as ctx:
    # This is the "test" job
    run_tests()

    # Trigger deploy if on main
    if ctx.branch == "main":
        ctx.trigger_job("deploy-production", env={"COMMIT": ctx.commit})
```

### Pattern 2: Parallel Jobs with Dependencies

Run test and lint in parallel, then build if both pass.

```python
with workflow_context() as ctx:
    # Trigger parallel jobs (no dependencies)
    ctx.trigger_job("test", env={"SUITE": "full"})
    ctx.trigger_job("lint", env={"TOOL": "ruff"})

    # Build waits for both
    ctx.trigger_job(
        "build",
        depends_on=["test", "lint"],
        condition="all_success"
    )
```

### Pattern 3: Conditional Workflow Based on Changes

Only run relevant jobs based on what files changed.

```python
with workflow_context() as ctx:
    files = changed_files("origin/main", "HEAD")

    python_changed = any(f.endswith(".py") for f in files)
    js_changed = any(f.endswith((".js", ".ts")) for f in files)
    docs_changed = any("docs/" in f for f in files)

    if python_changed:
        ctx.trigger_job("python-tests")

    if js_changed:
        ctx.trigger_job("js-tests")

    if docs_changed:
        ctx.trigger_job("build-docs")
```

### Pattern 4: Ordered Deploy

A job cannot examine the state of other jobs. Use `depends_on` and
`condition` to set the order of jobs in one workflow:

```python
with workflow_context() as ctx:
    ctx.trigger_job("build")
    ctx.trigger_job("deploy", depends_on=["build"], condition="all_success")
```

### Pattern 5: Multi-Stage Pipeline

Build artifacts, then deploy to staging, then production.

```python
with workflow_context() as ctx:
    # Stage 1: Validation (parallel)
    ctx.trigger_job("test")
    ctx.trigger_job("lint")
    ctx.trigger_job("security-scan")

    # Stage 2: Build (waits for validation)
    ctx.trigger_job(
        "build",
        depends_on=["test", "lint", "security-scan"],
        condition="all_success"
    )

    # Stage 3: Deploy to staging (waits for build)
    ctx.trigger_job(
        "deploy-staging",
        depends_on=["build"],
        env={"TARGET": "staging"}
    )

    # Stage 4: Deploy to production (waits for staging)
    if ctx.branch == "main":
        ctx.trigger_job(
            "deploy-production",
            depends_on=["deploy-staging"],
            env={"TARGET": "production"},
            timeout=1800
        )
```

### Pattern 6: Tag-Based Release

Trigger special jobs when a tag is pushed.

```python
with workflow_context() as ctx:
    info = git_info()

    if info["tag"]:
        print(f"Release tag detected: {info['tag']}")

        # Build release artifacts
        ctx.trigger_job(
            "build-release",
            env={"TAG": info["tag"], "RELEASE": "true"}
        )

        # Sign artifacts
        ctx.trigger_job(
            "sign-artifacts",
            depends_on=["build-release"],
            env={"TAG": info["tag"]}
        )

        # Publish to registry
        ctx.trigger_job(
            "publish-release",
            depends_on=["sign-artifacts"],
            env={"TAG": info["tag"]}
        )
```

### Pattern 7: Dynamic Job Generation

Create jobs dynamically based on configuration or analysis.

```python
with workflow_context() as ctx:
    # Read test matrix from config
    test_matrix = [
        {"python": "3.9", "os": "ubuntu"},
        {"python": "3.10", "os": "ubuntu"},
        {"python": "3.11", "os": "ubuntu"},
        {"python": "3.11", "os": "macos"},
    ]

    # Create a test job for each matrix entry
    for config in test_matrix:
        job_name = f"test-py{config['python']}-{config['os']}"
        ctx.trigger_job(
            job_name,
            env={
                "PYTHON_VERSION": config["python"],
                "OS": config["os"],
            },
            container_image=f"python:{config['python']}-slim"
        )

    # Build depends on all test jobs
    test_job_names = [
        f"test-py{c['python']}-{c['os']}" for c in test_matrix
    ]
    ctx.trigger_job("build", depends_on=test_job_names)
```

## Advanced Usage

### Custom Trigger Files

You can specify a custom triggers file location:

```python
ctx = WorkflowContext(triggers_file="/tmp/my-triggers.json")
ctx.trigger_job("test")
ctx.flush_triggers()
```

### Appending to Existing Triggers

If the triggers file already exists, new triggers are appended:

```python
# Job 1 writes triggers
ctx1 = WorkflowContext()
ctx1.trigger_job("test")
ctx1.flush_triggers()

# Job 2 appends more triggers
ctx2 = WorkflowContext()
ctx2.trigger_job("deploy")
ctx2.flush_triggers()

# Result: Both triggers are in the file
```

### Full Job Configuration

You can specify all job parameters when triggering:

```python
ctx.trigger_job(
    "deploy",
    env={"TARGET": "production", "VERSION": "1.0.0"},
    depends_on=["test", "build"],
    condition="all_success",
    source_type="git",
    source_url="https://github.com/user/repo.git",
    source_ref="main",
    ci_source_type="git",
    ci_source_url="https://github.com/user/ci-code.git",
    ci_source_ref="main",
    container_image="reactorcide/runner:latest",
    job_command="python /job/ci/scripts/deploy.py",
    priority=10,
    timeout=1800
)
```

### Using External APIs

You can query external systems to make workflow decisions:

```python
import requests

with workflow_context() as ctx:
    # Check if staging environment is healthy
    response = requests.get("https://staging.example.com/health")

    if response.status_code == 200:
        ctx.trigger_job("integration-tests", env={"TARGET": "staging"})
    else:
        print("Staging unhealthy, skipping integration tests")
```

### Complex Conditions

Use Python logic for complex conditional workflows:

```python
with workflow_context() as ctx:
    info = git_info()
    files = changed_files()

    # Determine if this is a hotfix
    is_hotfix = (
        ctx.branch and ctx.branch.startswith("hotfix/")
    )

    # Determine criticality based on changes
    critical_files = ["core/security.py", "auth/login.py"]
    is_critical = any(f in files for f in critical_files)

    # Adjust workflow based on analysis
    if is_hotfix and is_critical:
        # Fast-track: Skip some checks, high priority
        ctx.trigger_job("critical-tests", priority=10)
        ctx.trigger_job(
            "deploy-production",
            depends_on=["critical-tests"],
            priority=10,
            timeout=600
        )
    elif is_critical:
        # Full validation for critical changes
        ctx.trigger_job("full-test-suite", timeout=3600)
        ctx.trigger_job("security-scan")
        ctx.trigger_job(
            "deploy-production",
            depends_on=["full-test-suite", "security-scan"]
        )
    else:
        # Normal workflow
        ctx.trigger_job("test")
        if ctx.branch == "main":
            ctx.trigger_job("deploy-staging", depends_on=["test"])
```

## Best Practices

### 1. Use Context Manager

Prefer `workflow_context()` over manual `flush_triggers()`:

```python
# ✅ Good - automatic flushing
with workflow_context() as ctx:
    ctx.trigger_job("test")

# ❌ Avoid - manual flushing required
ctx = WorkflowContext()
ctx.trigger_job("test")
ctx.flush_triggers()  # Easy to forget!
```

### 2. Keep Ingest Jobs Fast

Ingest jobs should analyze and schedule, not do heavy work:

```python
# ✅ Good - analysis only
with workflow_context() as ctx:
    files = changed_files()
    if any(f.endswith(".py") for f in files):
        ctx.trigger_job("python-tests")  # Test work happens in separate job

# ❌ Avoid - heavy work in ingest job
with workflow_context() as ctx:
    run_full_test_suite()  # This should be in a separate job
    ctx.trigger_job("deploy")
```

### 3. Use Meaningful Job Names

Job names should be descriptive and unique:

```python
# ✅ Good
ctx.trigger_job("test-python-3.11-ubuntu")
ctx.trigger_job("deploy-production-us-east")

# ❌ Avoid
ctx.trigger_job("job1")
ctx.trigger_job("job2")
```

### 4. Set Appropriate Timeouts

Long-running jobs should have explicit timeouts:

```python
# ✅ Good
ctx.trigger_job("integration-tests", timeout=3600)  # 1 hour
ctx.trigger_job("deploy", timeout=1800)  # 30 minutes

# ❌ Avoid - relies on default timeout
ctx.trigger_job("very-long-job")
```

### 5. Use Priorities Wisely

High-priority jobs should be genuinely urgent:

```python
# ✅ Good
if ctx.branch == "main":
    ctx.trigger_job("deploy-production", priority=10)  # Production deploys are urgent
else:
    ctx.trigger_job("deploy-staging", priority=5)  # Staging is normal priority

# ❌ Avoid - everything high priority
ctx.trigger_job("test", priority=10)  # Tests shouldn't all be high priority
```

### 6. Handle Errors Gracefully

Don't let ingest jobs fail unnecessarily:

```python
# ✅ Good - graceful degradation
try:
    files = changed_files()
    needs_test = any(f.endswith(".py") for f in files)
except Exception as e:
    print(f"Could not analyze changes: {e}")
    needs_test = True  # Default to running tests

if needs_test:
    ctx.trigger_job("test")

# ❌ Avoid - unhandled exceptions
files = changed_files()  # Might fail and crash ingest job
```

### 7. Document Complex Logic

Add comments explaining workflow decisions:

```python
with workflow_context() as ctx:
    # Only deploy from main or release branches
    # Release branches follow pattern: release/vX.Y
    is_release_branch = (
        ctx.branch == "main" or
        (ctx.branch and ctx.branch.startswith("release/"))
    )

    if is_release_branch:
        ctx.trigger_job("deploy-production")
```

## Examples

See the [`examples/pipelines/`](../examples/pipelines/) directory for complete working examples:

- [`simple_test_and_deploy.py`](../examples/pipelines/simple_test_and_deploy.py) - Basic sequential pipeline
- [`complex_parallel_pipeline.py`](../examples/pipelines/complex_parallel_pipeline.py) - Parallel jobs with dependencies
- [`context_manager_example.py`](../examples/pipelines/context_manager_example.py) - Using context manager

Each example includes:
- Complete working code
- Usage instructions
- Comments explaining the pattern

## Troubleshooting

### Triggers Not Being Processed

**Problem**: Jobs trigger but follow-up jobs don't run.

**Solutions**:
1. Make sure that the script calls `flush_triggers()`, or use the context manager.
2. In a coordinator job, make sure that `REACTORCIDE_COORDINATOR_URL`,
   `REACTORCIDE_API_TOKEN`, and `REACTORCIDE_JOB_ID` are set. Runnerlib sends
   the triggers to `POST /api/v1/jobs/{job_id}/triggers`. If the request
   fails, the job fails. After a successful request, runnerlib deletes
   `/job/triggers.json`.
3. In `run-local`, make sure that the script writes `/job/triggers.json`.

### Jobs Running When They Shouldn't

**Problem**: Jobs run even when dependencies fail.

**Solution**: Check the `condition` parameter:

```python
# Only run if all dependencies succeed
ctx.trigger_job("deploy", depends_on=["test", "build"], condition="all_success")
```

### Git Functions Not Working

**Problem**: `changed_files()` or `git_info()` return empty/None.

**Solutions**:
1. Ensure git is installed in the container
2. Verify `REACTORCIDE_CODE_DIR` points at a git repository (default `/job/src`)
3. Check that the refs exist: `git rev-parse origin/main`

### Context Properties Are None

**Problem**: `ctx.branch` or `ctx.commit` are None.

**Solution**: These properties read the job environment (see
[Context Properties](#context-properties)). A job from the coordinator has
`REACTORCIDE_BRANCH` and `REACTORCIDE_SHA`. For a local run, set
`REACTORCIDE_GIT_BRANCH`, `REACTORCIDE_GIT_COMMIT`, and `REACTORCIDE_GIT_REF`.
`ctx.branch` is `None` for a tag event: use `ctx.ref`.

## Reference

### Environment Variables

The workflow module uses these environment variables:

- `REACTORCIDE_JOB_ID` - Current job ID
- `REACTORCIDE_COORDINATOR_URL` - Coordinator API URL (for trigger submission)
- `REACTORCIDE_API_TOKEN` - Job token (for trigger submission)
- `REACTORCIDE_EVENT_ID` - Operation ID of the trigger submission. If it is
  not set, runnerlib uses a random UUID.
- `RC_WF_ID` - Current workflow ID
- `RC_WF_VARS_JSON` - Workflow variables as JSON (read first)
- `RC_WF_VARS_FILE` - Workflow variables file (default `/job/workflow-vars.json`)
- `RC_WF_OUTPUT_FILE` - File for `set_workflow_var()` and
  `set_workflow_output()` (default `/job/workflow-output.json`)
- `REACTORCIDE_BRANCH`, `REACTORCIDE_SHA`, `REACTORCIDE_EVENT_TYPE`,
  `REACTORCIDE_PR_NUMBER`, `REACTORCIDE_HEAD_REF`, `REACTORCIDE_PR_REF` -
  Read by `ctx.branch`, `ctx.commit`, and `ctx.ref`. The coordinator sets
  them.
- `REACTORCIDE_GIT_BRANCH`, `REACTORCIDE_GIT_COMMIT`, `REACTORCIDE_GIT_REF` -
  Replace the values of `ctx.branch`, `ctx.commit`, and `ctx.ref` when set.
  Reactorcide does not set them.

### JobTrigger Fields

When triggering jobs, these fields are available:

- `job_name` (required) - Name of the job
- `depends_on` - List of job names this depends on
- `condition` - Triggering condition (`all_success`, `any_failed`, or `always`)
- `env` - Environment variables dict
- `source_type` - Source type ("git", "copy", "none")
- `source_url` - Source URL
- `source_ref` - Source ref (branch, tag, commit)
- `ci_source_type` - CI source type
- `ci_source_url` - CI source URL
- `ci_source_ref` - CI source ref
- `container_image` - Container image to use
- `job_command` - Command to run
- `priority` - Requested priority (integer, higher = more urgent). See
  [Priority and Lanes](./priority.md).
- `timeout` - Job timeout in seconds
- `capabilities` - Runtime services, such as `docker` or `builder`
- `image_pull_secrets` - Kubernetes Secret names for the image pull
- `code_dir`, `job_dir`, `working_dir` - Container paths
- `run_as_user`, `run_as_administrator` - Job identity
- `for_each`, `item_var` - Expand one trigger into many nodes
- `worker_class` - Worker class. A trigger cannot raise or change the
  inherited worker class.
- `characteristics` - Worker routing characteristics
- `resources` - CPU and memory requests and limits
- `disable_run_local`, `run_local` - Local execution settings
- `quarantine_policy` - `report` or `ignore_quarantined`

## Additional Resources

- [DESIGN.md](../DESIGN.md) - System architecture and design principles
- [Runnerlib DESIGN.md](../runnerlib/DESIGN.md) - Detailed runnerlib design
- [Installation and Deployment](./getting-started.md) - Deployment guide

## Support

Questions? Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) or open an issue on GitHub.
