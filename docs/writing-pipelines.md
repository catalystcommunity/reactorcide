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

## Core Concepts

### Jobs vs Workflows

- **Job**: A single container execution with steps that share environment/filesystem
- **Workflow**: Multiple jobs with dependencies, orchestrated by runnerlib
- **Ingest Job**: The initial job that analyzes triggers and schedules follow-up jobs

### Job Triggering

Jobs trigger follow-up jobs by writing to `/job/triggers.json`. The worker reads this file after job completion and submits the triggered jobs to the queue.

**Trigger File Format**:
```json
{
  "type": "trigger_job",
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
      "timeout": 1800
    }
  ]
}
```

### Dependencies and Conditions

- **depends_on**: List of job names that must complete first
- **condition**: When to run the job
  - `all_success` - Run only if all dependencies succeeded (default)
  - `any_success` - Run if at least one dependency succeeded
  - `always` - Run regardless of dependency results

## Quick Start

### Basic Pipeline Script

```python
#!/usr/bin/env python3
"""
Minimal pipeline: run tests, then deploy on main branch.
"""

import sys
from workflow import trigger_job, flush_triggers, WorkflowContext

def main():
    ctx = WorkflowContext()

    # Run some work...
    print("Running tests...")
    # ... test execution logic ...

    # Trigger follow-up job conditionally
    if ctx.branch == "main":
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
- `condition` (str): Triggering condition ("all_success", "any_success", "always")
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
    timeout=1800
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

### State Checking (Requires API Access)

#### `is_job_running(job_name)`

Check if a job is currently running.

**Note**: Requires `REACTORCIDE_COORDINATOR_URL` and `REACTORCIDE_API_TOKEN` environment variables.

**Example**:
```python
if not is_job_running("deploy-production"):
    trigger_job("deploy-production")
else:
    print("Deploy already in progress, skipping")
```

#### `get_job_result(job_name)`

Get result of a previous job.

**Returns**: Dictionary with job information or None

**Example**:
```python
test_result = get_job_result("test")
if test_result and test_result["exit_code"] == 0:
    trigger_job("deploy")
```

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
    print(f"Branch: {ctx.branch}")
    print(f"Commit: {ctx.commit}")

    # Trigger jobs
    ctx.trigger_job("test")
    ctx.trigger_job("lint")

    # Automatically flushed on exit
```

### Context Properties

When using `WorkflowContext` or `workflow_context`:

- `ctx.job_id` - Current job ID
- `ctx.branch` - Current git branch
- `ctx.commit` - Current git commit SHA
- `ctx.ref` - Current git ref

**Example**:
```python
with workflow_context() as ctx:
    if ctx.branch == "main":
        print("Running on main branch")

    if ctx.branch and ctx.branch.startswith("release/"):
        trigger_job("release-validation")
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

### Pattern 4: Guarded Deploy (Check State)

Prevent duplicate deploys by checking if one is already running.

```python
with workflow_context() as ctx:
    if ctx.branch == "main":
        # Only trigger deploy if none running
        if not ctx.is_job_running("deploy-production"):
            ctx.trigger_job("deploy-production")
        else:
            print("Deploy already in progress, skipping")
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
1. Ensure `flush_triggers()` is called (or use context manager)
2. Check that `/job/triggers.json` is being written
3. Verify worker has permission to read the triggers file

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
2. Verify `/job/src` contains a git repository
3. Check that the refs exist: `git rev-parse origin/main`

### Context Properties Are None

**Problem**: `ctx.branch` or `ctx.commit` are None.

**Solution**: Ensure environment variables are set:
- `REACTORCIDE_GIT_BRANCH`
- `REACTORCIDE_GIT_COMMIT`
- `REACTORCIDE_GIT_REF`

These are typically set by runnerlib during source preparation.

## Reference

### Environment Variables

The workflow module uses these environment variables:

- `REACTORCIDE_JOB_ID` - Current job ID
- `REACTORCIDE_GIT_BRANCH` - Current git branch
- `REACTORCIDE_GIT_COMMIT` - Current git commit SHA
- `REACTORCIDE_GIT_REF` - Current git ref
- `REACTORCIDE_COORDINATOR_URL` - Coordinator API URL (for state checking)
- `REACTORCIDE_API_TOKEN` - API token (for state checking)

### JobTrigger Fields

When triggering jobs, these fields are available:

- `job_name` (required) - Name of the job
- `depends_on` - List of job names this depends on
- `condition` - Triggering condition ("all_success", "any_success", "always")
- `env` - Environment variables dict
- `source_type` - Source type ("git", "copy", "none")
- `source_url` - Source URL
- `source_ref` - Source ref (branch, tag, commit)
- `ci_source_type` - CI source type
- `ci_source_url` - CI source URL
- `ci_source_ref` - CI source ref
- `container_image` - Container image to use
- `job_command` - Command to run
- `priority` - Job priority (integer, higher = more urgent)
- `timeout` - Job timeout in seconds

## Additional Resources

- [DESIGN.md](../DESIGN.md) - System architecture and design principles
- [Runnerlib DESIGN.md](../runnerlib/DESIGN.md) - Detailed runnerlib design
- [deployment-plan.md](../deployment-plan.md) - Deployment guide

## Support

Questions? Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) or open an issue on GitHub.
