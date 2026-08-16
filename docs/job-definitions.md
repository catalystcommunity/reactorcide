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

## Workflow Definitions

A workflow groups the jobs that run together for one event. A workflow has a name. The name shows as a check on the pull request. Put workflow files in `.reactorcide/workflows/`.

```
your-repo/
  .reactorcide/
    workflows/
      pr.yaml
      release.yaml
    jobs/
      test-go.yaml
      test-web.yaml
```

One event can match more than one workflow. Each matched workflow runs as a separate named check. This lets a large repository split its pipelines per team, product, or org.

### Workflow schema

```yaml
# Required for policy-controlled head CI. It is the stable security identity.
id: reactorcide-pr

# Required: the workflow name. It shows as the check name.
name: "Reactorcide PR"

# Optional: a short description.
description: "Validate commits and run the tests for pull requests."

# Required: when the workflow runs.
on:
  # Required: the event types that start the workflow.
  events:
    - pull_request_opened
    - pull_request_updated
  # Optional: branch patterns. An empty list matches all branches.
  branches:
    - main

# Optional: run the workflow only when matching files change.
paths:
  include:
    - "coordinator_api/**"

# Optional: values that are available to all workflow jobs.
vars:
  release_channel: stable

# Required: the jobs in the workflow. The map key is the job name.
jobs:
  # A job can reference a reusable job file and add the run order.
  conventional-commits:
    job_file: conventional-commits.yaml

  test-go:
    job_file: test-go.yaml
    depends_on:
      - conventional-commits

  # A job can also be written inline.
  test-web:
    image: golang:1.26
    command: "go test ./..."
    depends_on:
      - conventional-commits
```

### How workflow jobs work

- The map key is the job name. Use the key name in `depends_on` to set the run order.
- A job entry uses one of two forms. Use `job_file` to reference a file in `.reactorcide/jobs/`. Or write the job fields inline.
- With `job_file`, the referenced file is the base. The inline fields overlay the base. The inline fields win.
- Write the container fields (`image`, `command`, `capabilities`, and so on) flat in the job entry, or nested under a `job:` key.
- `depends_on` sets the jobs that must finish first. `needs` is an alias for `depends_on`.
- `condition` controls when the job runs. The values are `all_success` (default), `any_failed`, and `always`.
- The `on:` and `paths:` of a referenced job file are ignored. The workflow controls when the jobs run.
- The `vars` map sets the first workflow variables. A job reads the current
  values from `RC_WF_VARS_FILE` or through runnerlib.

runnerlib resolves each `job_file` reference before it sends the triggers. It reads the referenced file, applies the inline fields, and sends a complete job. The coordinator does not need the job files.

The event and branch fields use the same rules as the bare job files. See [Event Types](#event-types) and [Branch Matching](#branch-matching).

### Bare jobs (compatibility)

Bare `.reactorcide/jobs/*.yaml` files still work. Reactorcide uses workflow files first. If there are no workflow files, Reactorcide loads the bare job files instead. It groups them under one workflow. The default workflow name is `Reactorcide Jobs, repo: <name>`, where `<name>` is the repository basename.

The rest of this page describes the bare job file schema. A workflow job entry uses the same job fields.

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

### Checkout Mode

Runnerlib has two checkout modes for pull-request evaluation:

- `isolated` uses separate source, base CI, and head CI clones.
- `shared` uses one non-shallow, blob-filtered Git object store. It stages only the
  `.reactorcide` directory for the base CI and head CI views.

Use `shared` for a large Git repository. The evaluator does not create a full
source working tree in this mode. A child job still gets the source revision
and the approved CI revision that it needs.

Set the mode in the evaluator job specification that you pass to `run-local`
or submit directly:

```yaml
checkout:
  mode: shared
```

You can also set this field in an overlay file:

```bash
reactorcide run-local -i large-repo.yaml ./jobs/evaluate.yaml
```

The overlay uses the normal precedence rules. A repository workflow or job
definition cannot select the evaluator checkout mode. Evaluation has already
started before runnerlib reads those repository files. The mode does not
select a Git URL or a Git revision. The coordinator supplies the trusted base
and head revisions.

The current precedence is job or local overlay, project, then coordinator.
Set the coordinator default with `REACTORCIDE_CHECKOUT_MODE`. An empty project
setting inherits that default. Organization-level checkout defaults are not
implemented.

Shared mode applies only to pull-request evaluation. The evaluator has a Git
object store, but it has no application source worktree. Use isolated mode for
a custom evaluator that reads application files directly.

## Environment Variables

The `environment` section defines additional environment variables injected into the triggered job. These are merged with the standard Reactorcide CI variables.

Standard variables available in every webhook-triggered job:

| Variable | Description |
|---|---|
| `REACTORCIDE_CI` | Always `"true"` |
| `REACTORCIDE_PROVIDER` | VCS provider name, such as `github` |
| `REACTORCIDE_EVENT_TYPE` | Generic event type |
| `REACTORCIDE_REPO` | Repository full name (`org/repo`) |
| `REACTORCIDE_SOURCE_URL` | Source repository clone URL |
| `REACTORCIDE_CI_SOURCE_DIR` | Trusted CI checkout directory (`/job/ci`) |
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

## Policy-Controlled Execution

Do not put an organization in job YAML. The coordinator assigns the
organization from the project, request principal, or parent job.

A pull-request admission rule selects the execution profile and worker class.
Repository workflow and job YAML cannot replace the selected profile, worker
class, CI origin, policy revision, rule, or approval. Child work cannot add a
runtime capability or change its inherited worker class.

A job specification that you submit directly to the coordinator can use the
top-level `worker_class` field. This field is not part of the repository job
schema on this page.

See [Organizations and Trusted CI Policy](./organizations-and-ci-policy.md)
for the policy schema, profile fields, approval rules, and GitHub reporting.
