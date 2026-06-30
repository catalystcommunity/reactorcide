# Job Definition Reference

Job definitions are YAML files that tell Reactorcide what to run in response to VCS events. They live in `.reactorcide/jobs/` in your repository (or a separate CI source repository).

## File Location

```
your-repo/
  .reactorcide/
    jobs/
      test.yaml
      build.yaml
      deploy.yaml
```

All `.yaml` and `.yml` files in `.reactorcide/jobs/` are loaded. Subdirectories are not scanned.

## YAML Schema

```yaml
# Required: unique name for this job
name: "test"

# Optional: human-readable description
description: "Run tests on pull requests"

# Required: when this job should run
triggers:
  # Required: list of event types that trigger this job
  events:
    - push
    - pull_request_opened

  # Optional: branch patterns to match (empty or omitted = all branches)
  branches:
    - main
    - "feature/*"

# Optional: only trigger if matching files changed
paths:
  include:
    - "src/**"
    - "tests/**"
  exclude:
    - "docs/**"
    - "*.md"

# Required: container execution configuration
job:
  image: "alpine:latest"       # Container image to run
  command: "make test"          # Command to execute
  code_dir: "/job/src"          # Optional: source checkout/mount path
  job_dir: "/job/src"           # Optional: runnerlib/job working directory
  working_dir: "/job/src"       # Optional: raw process working directory
  capabilities: []              # Optional: runtime services such as docker or builder
  run_as:
    user: runner                # Optional: runner, root, or numeric uid[:gid]
  timeout: 1800                # Optional: timeout in seconds
  priority: 10                 # Optional: scheduling priority (higher = more urgent)

# Optional: environment variables injected into the job
environment:
  BUILD_TYPE: "test"
  CI: "true"
```

## Event Types

Reactorcide normalizes VCS-specific events into generic event types. Use these in `triggers.events`:

| Event Type | Description | GitHub Source |
|---|---|---|
| `push` | Commits pushed to a branch | `push` event with `refs/heads/` ref |
| `pull_request_opened` | PR created or reopened | `pull_request` with action `opened` or `reopened` |
| `pull_request_updated` | New commits pushed to a PR | `pull_request` with action `synchronize` |
| `pull_request_merged` | PR merged into target branch | `pull_request` with action `closed` and `merged=true` |
| `pull_request_closed` | PR closed without merging | `pull_request` with action `closed` and `merged=false` |
| `tag_created` | Tag pushed to the repository | `push` event with `refs/tags/` ref |

Events not matching any of these are ignored.

## Branch Matching

The `triggers.branches` field accepts glob patterns. If omitted or empty, all branches match.

| Pattern | Matches | Does Not Match |
|---|---|---|
| `main` | `main` | `main-v2`, `not-main` |
| `feature/*` | `feature/foo`, `feature/bar` | `feature/foo/bar` |
| `release/**` | `release/1.0`, `release/1.0/hotfix` | `releases/1.0` |
| `*` | Any single-segment branch | Multi-segment branches like `feature/foo` |
| `**` | Any branch | (matches everything) |

The matching uses `fnmatch` semantics:
- `*` matches any characters within a single path segment (no `/`)
- `**` matches any number of path segments (including zero)

For push events, the branch is extracted from the git ref. For PR events, the branch is the PR's target (base) branch.

## Path Matching

The optional `paths` section lets you skip jobs when irrelevant files changed. This is useful for monorepos or repositories where not every change needs every job.

**Rules:**
- If `paths` is omitted: the job triggers regardless of which files changed
- If `paths.include` is specified: at least one changed file must match an include pattern
- If `paths.exclude` is specified: files matching an exclude pattern are skipped
- A job triggers if **any** changed file passes both the include and exclude filters
- Patterns use glob syntax (`*` for single segment, `**` for recursive)

**Examples:**

Only run when source code changes:
```yaml
paths:
  include: ["src/**", "lib/**"]
```

Run for everything except documentation:
```yaml
paths:
  exclude: ["docs/**", "*.md", "LICENSE"]
```

Combined include and exclude:
```yaml
paths:
  include: ["src/**", "tests/**"]
  exclude: ["src/generated/**"]
```

## Job Configuration

| Field | Type | Description |
|---|---|---|
| `job.image` | string | Container image to use. Falls back to the project's `default_runner_image` if not set. |
| `job.command` | string | Command to execute inside the container. Falls back to the project's `default_job_command` if not set. |
| `job.code_dir` | string | Container path where source code is mounted or checked out. Defaults to `/job/src`. |
| `job.job_dir` | string | Container path runnerlib treats as the job directory. Defaults to `code_dir`. |
| `job.working_dir` | string | Raw process working directory. Defaults to `job_dir`. |
| `job.capabilities` | list | Runtime services the job needs, such as `docker` or `builder`. Capabilities do not imply root. |
| `job.run_as.user` | string | Container user for deployed workers: `runner`, `root`, or numeric `uid[:gid]`. Defaults to `runner`. |
| `job.timeout` | integer | Timeout in seconds. Falls back to the project's `default_timeout_seconds` if not set. |
| `job.priority` | integer | Scheduling priority. Higher values are scheduled first. |

For the path and run identity contract across local, VM, and Kubernetes execution, see [Runtime Behavior](./runtime-behavior.md).

### Local-only Run Settings

Flat job files used with `reactorcide run-local` can also include a top-level `run_local` block:

```yaml
run_local:
  user: host       # host, runner, root, or numeric uid[:gid]
  as_runner: false
```

`run_local` is ignored by deployed workers. It exists so local execution can bind-mount a working tree and still choose the uid that writes into that tree. `run-local` defaults to the host uid; use `run_as.user: root` or `--user root` when a local job must run as root.

## Environment Variables

The `environment` section defines additional environment variables injected into the triggered job. These are merged with the standard Reactorcide CI variables.

Standard variables available in every webhook-triggered job:

| Variable | Description |
|---|---|
| `REACTORCIDE_CI` | Always `"true"` |
| `REACTORCIDE_PROVIDER` | VCS provider (`github`, `gitlab`) |
| `REACTORCIDE_EVENT_TYPE` | Generic event type |
| `REACTORCIDE_REPO` | Repository full name (`org/repo`) |
| `REACTORCIDE_SOURCE_URL` | Source repository clone URL |
| `REACTORCIDE_SHA` | Commit SHA |
| `REACTORCIDE_BRANCH` | Branch name |
| `REACTORCIDE_PR_NUMBER` | PR number (PR events only) |
| `REACTORCIDE_PR_REF` | PR head branch (PR events only) |
| `REACTORCIDE_PR_BASE_REF` | PR base branch (PR events only) |

Variables defined in `environment` are added alongside these. If a key conflicts, the job definition value takes precedence.

## Complete Examples

### Run tests on pull requests

```yaml
name: test
description: "Run unit tests on PRs targeting main"
triggers:
  events:
    - pull_request_opened
    - pull_request_updated
  branches:
    - main
paths:
  include: ["src/**", "tests/**"]
  exclude: ["src/generated/**"]
job:
  image: python:3.12
  command: "pip install -r requirements.txt && pytest -v"
  timeout: 1800
  priority: 10
environment:
  PYTHONDONTWRITEBYTECODE: "1"
```

### Build on push to main

```yaml
name: build
description: "Build and publish artifacts on push to main"
triggers:
  events:
    - push
  branches:
    - main
job:
  image: golang:1.22
  command: "make build && make publish"
  timeout: 3600
  priority: 5
environment:
  CGO_ENABLED: "0"
  GOOS: linux
```

### Deploy on tag

```yaml
name: deploy
description: "Deploy when a release tag is pushed"
triggers:
  events:
    - tag_created
job:
  image: alpine:latest
  command: "sh /job/ci/scripts/deploy.sh"
  timeout: 1800
  priority: 20
environment:
  DEPLOY_ENV: production
```

### Cleanup on PR close

```yaml
name: cleanup
description: "Clean up preview environments when PRs are closed"
triggers:
  events:
    - pull_request_closed
    - pull_request_merged
job:
  image: alpine:latest
  command: "sh /job/ci/scripts/cleanup-preview.sh"
  timeout: 300
  priority: 1
```
