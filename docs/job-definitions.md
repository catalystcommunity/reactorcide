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

# Optional: the owner of the workflow. The coordinator reads it from the
# trusted CI definition only. See docs/workflow-design.md "Owners and Contact".
owners:
  group: reactorcide-ci          # a Reactorcide group, or vcs_team:ORG/SLUG
  subjects:                      # optional extra owner subjects
    - vcs_user:github/alice

# Optional: how to reach the owner.
contact:
  channel: "#reactorcide-ci"
  help_url: https://wiki.example/reactorcide-ci
  email: reactorcide-ci@example.com

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
- With `job_file`, the referenced file is the base. The inline fields overlay the base. The inline fields win. The overlay merges the `job` map and the `environment` map one key at a time. It also merges the `run_as` block one field at a time: an inline `run_as` block changes only the fields that it sets, and the other fields keep the value of the job file. An empty or null field keeps the value of the job file. An overlay file (`-i`) uses the same rule. Each other value replaces the value of the base.
- Write the container fields (`image`, `command`, `capabilities`, and so on) flat in the job entry, or nested under a `job:` key.
- `depends_on` sets the jobs that must finish first. `needs` is an alias for `depends_on`.
- `condition` controls when the job runs. The values are `all_success` (default), `any_failed`, and `always`.
- `if` holds the predicates `paths`, `branches`, `events`, and `schedule`. All the predicates must be true for the job to run. Otherwise the job becomes a `skipped` node. See [Conditional Jobs](./events-and-schedules.md#conditional-jobs).
- A job file can have an `if` block. A workflow entry with its own `if` replaces it.
- The `on:` and `paths:` of a referenced job file are ignored. The workflow controls when the jobs run.
- The `vars` map sets the first workflow variables. A job reads the current
  values from `RC_WF_VARS_FILE` or through runnerlib.

runnerlib resolves each `job_file` reference before it sends the triggers. It reads the referenced file, applies the inline fields, and sends a complete job. The coordinator does not need the job files. If a referenced file is missing or not valid, eval records an `invalid` decision for the workflow file.

A trigger file from a job script (not from eval) can also name a `job_file`. The coordinator then reads the file from the job workspace. If it cannot read or parse the file, the workflow node fails as `not_admitted`, with the load error as the reason. The coordinator does not drop the node.

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
  image_pull_secrets: []        # Optional: Kubernetes Secret names for the image pull
  run_as:
    user: runner                # Optional: runner, root, or numeric uid[:gid]
    administrator: true         # Optional: Windows VM jobs only (see below)
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

The coordinator also uses the event type `schedule` for a schedule fire. A
schedule names one workflow, and the `on` filter of that workflow does not
apply. Use the job `if` predicate `schedule` to run or skip a job on a
schedule fire. See [Schedules](./events-and-schedules.md#schedules).

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
| `job.image` | string | The image to use: an image alias name or an image reference. Falls back to the project's `default_runner_image` if not set. A macOS or Windows VM job must name an image alias or a pinned reference that an alias pins. See [Image Aliases](#image-aliases). |
| `job.command` | string | Command to execute inside the container. Falls back to the project's `default_job_command` if not set. |
| `job.code_dir` | string | Container path where source code is mounted or checked out. Defaults to `/job/src`. |
| `job.job_dir` | string | Container path runnerlib treats as the job directory. Defaults to `code_dir`. |
| `job.working_dir` | string | Raw process working directory. Defaults to `job_dir`. |
| `job.capabilities` | list | Runtime services the job needs, such as `docker` or `builder`. Capabilities do not imply root. |
| `job.image_pull_secrets` | list | Names of Kubernetes Secrets that permit the image pull. Write names only, never credential values. The worker rejects a name that is not in its operator allowlist. See [Job Image Pull Secrets](#job-image-pull-secrets). |
| `job.run_as.user` | string | Container user for deployed workers: `runner`, `root`, or numeric `uid[:gid]`. Defaults to `runner`. An overlay file (`-i`) and a workflow entry that overlays a `job_file` change only the `run_as` fields that they set (see [How workflow jobs work](#how-workflow-jobs-work)). |
| `job.run_as.administrator` | boolean | Windows VM jobs only. When the guest image has no job account, the guest daemon creates it. This value sets whether the new account is in the Administrators group. Defaults to `true`. The daemon does not change an account that the image has. Container backends ignore this value. The value must be `true` or `false`. |
| `job.timeout` | integer | Timeout in seconds. Falls back to the project's `default_timeout_seconds` if not set. |
| `job.priority` | integer | Requested priority. The coordinator adds it, clamped to -50..50, to the base of the lane of the job. Higher values are scheduled first. See [Priority and Lanes](./priority.md#requested-priority). |
| `job.raw_command` | boolean | Default `false`. Eval wraps the command as `runnerlib run --job-command '<command>'`, so runnerlib prepares the source first. With `true`, eval sends the command as it is. Eval does not wrap a command that starts with `runnerlib `. The wrap quotes the command as one argument for the shell of the job, so a command can contain single quotes and other shell characters. For `sh -c`, each `'` becomes `'"'"'`. For PowerShell (`os: windows`, or a `REACTORCIDE_JOB_SHELLCMD` that starts with `powershell` or `pwsh`), each `'` becomes `''`. |
| `job.depends_on` | list | Workflow jobs that must finish first. `needs` is an alias in a workflow entry. |
| `job.condition` | string | `all_success` (default), `any_failed`, or `always`. |
| `job.for_each` | list | Literal values. Eval expands the job into one node for each value. |
| `job.item_var` | string | The environment variable that gets the `for_each` value of the node. |
| `job.characteristics` | map | Worker routing characteristics, such as `os` and `arch`. The coordinator routes the job to a queue of workers that have these characteristics. The `os` value also selects the default shell prefix. |
| `job.resources` | map | CPU and memory requests and limits (`cpu.request`, `cpu.limit`, `memory.limit`). |
| `job.disable_run_local` | boolean | `run-local` refuses the job. CI ignores this value. |
| `job.quarantine_policy` | string | `report` (default) or `ignore_quarantined`. With `ignore_quarantined`, the workflow node succeeds when the job failed only by quarantined tests of its JUnit reports. The job exit code does not change. See [Artifacts and Test Results](./artifacts-and-tests.md#what-a-quarantine-changes). |

For the path and run identity contract across local, VM, and Kubernetes execution, see [Runtime Behavior](./runtime-behavior.md).

### Command Execution

Each `job.command` goes through a shell. This applies to a single-line
command and to a multi-line command. Thus `;`, `|`, `&&`, `$NAME`, and
`$(...)` are shell syntax in each command. The worker adds the shell
prefix in front of the command, and gives the full command text as the last
argument:

```text
command: echo a; sleep 5   ->   ["sh", "-c", "echo a; sleep 5"]
```

For a repository job without `raw_command: true`, eval first wraps the
command as `runnerlib run --job-command '<command>'` (see `job.raw_command`).
The worker then adds the shell prefix to that text. Runnerlib runs the inner
command through `/bin/sh` again. Thus an image without a shell or without
runnerlib needs `raw_command: true`.

The shell prefix is the first value of this list that is set:

1. `command_prefix` of a job spec file (`run-local` and `submit` only).
2. The job environment variable `REACTORCIDE_JOB_SHELLCMD`. Use this
   variable in workflow job entries.
3. The default for the `os` characteristic of the job:

| `os` characteristic | Default shell prefix |
|---|---|
| `linux` (default), `macos` | `sh -c` |
| `windows` | `powershell.exe -NoProfile -NonInteractive -Command` |

The worker splits the prefix at each space, so each flag is one argument.
Use quotes for a word that has a space (`"C:/Program Files/PowerShell/7/pwsh.exe" -Command`).
A backslash is not an escape character. A prefix with only one word is
replaced by `sh -c`.

The worker does not add the prefix when the command starts with a shell
invocation: `sh -c`, `bash -c`, `zsh -c` (also with `/bin/` or
`/usr/bin/`), `powershell`, `powershell.exe`, `pwsh`, `pwsh.exe`,
`cmd /c`, or `cmd.exe /c`. The worker splits such a command into words
and runs it as it is.

**Images without a shell.** A job image with no `/bin/sh` (for example a
distroless image) cannot run `sh -c`. For such a job, set
`REACTORCIDE_JOB_SHELLCMD: none` (or `command_prefix: none`). With `none`,
the worker splits a single-line command into words at each space (quotes
group words) and runs the first word directly. Shell syntax does not work
with `none`. Reactorcide has no separate argument-list form of `command`.
Alternatively, set a prefix for a shell that the image has, for example
`/busybox/sh -c` for a distroless `debug` image.

A Windows job must stop at the first error. With the PowerShell default,
start the command with `$ErrorActionPreference = 'Stop'`, and examine
`$LASTEXITCODE` after each external program. See
[Windows job commands](./vm-runners-windows.md#windows-job-commands).

### Job Image Pull Secrets

Use `job.image_pull_secrets` when the job image is in a private registry:

```yaml
job:
  image: containers.example.com/private/team/toolchain@sha256:example
  command: make test
  image_pull_secrets:
    - regcred
```

Each list item is the NAME of a Kubernetes Secret. Do not put credentials or
secret values in the list. Each referenced Secret must:

- Exist in the configured Kubernetes job namespace.
- Have type `kubernetes.io/dockerconfigjson`.
- Contain credentials for the registry in the selected image.

The worker validates each name and rejects the job before Kubernetes Job
creation when a name is empty, invalid, duplicated, or not approved by the
operator allowlist (`worker.allowedJobImagePullSecrets`, see
[Workers](./workers.md#job-image-pull-secrets)). An empty allowlist rejects
all job-level requests. Kubernetes reports a missing Secret or an invalid
registry credential; Reactorcide does not read Secret data to improve the
error.

An overlay that omits `image_pull_secrets` keeps the base list. An overlay
with a non-empty list replaces the base list.

Docker, containerd, and local execution accept and keep the field but do not
read Kubernetes Secrets. The local container runtime uses its own configured
credential store to pull the image.

### Image Aliases

An image alias is a name for one pinned image reference. An organization
admin sets organization aliases. A project can set more aliases. A project
alias replaces an organization alias that has the same name, for the jobs
of that project.

An alias name has 1 to 63 characters: `a-z`, `0-9`, `.`, `_`, and `-`. The
first character is a letter or a digit. A name cannot contain `/`, `:`, or
`@`.

```yaml
job:
  image: windows-vs2022
  command: |
    $ErrorActionPreference = 'Stop'
    & .\build.ps1
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  characteristics:
    os: windows
```

A Windows job gets the shell prefix
`powershell.exe -NoProfile -NonInteractive -Command` when it sets no
prefix (see [Command Execution](#command-execution)). A Windows job must stop at the first error. Start the PowerShell text with
`$ErrorActionPreference = 'Stop'`, and examine `$LASTEXITCODE` after each
external program. See
[Windows job commands](./vm-runners-windows.md#windows-job-commands).

The coordinator resolves `job.image` one time, when it creates the job:

1. When the value has the form of an alias name, the coordinator looks for
   a project alias with that name, and then for an organization alias.
2. When it finds an alias, the job gets the pinned reference of the alias.
   The job records the alias name (`image_alias`) and gets the
   characteristic `image_digest: "sha256:<digest>"`. Only a worker that has
   that image can claim the job.
3. When it finds no alias, the value is an image reference. A container job
   with a reference runs as before.

Rules for VM jobs (characteristic `os` is `macos` or `windows`):

- The job image must be an alias name, or a pinned reference
  (`...@sha256:<digest>`) whose digest an alias in the scope of the job
  pins. That alias must have the kind `vm` and the os and arch of the job.
  The pinned reference also gets the `image_digest` characteristic.
- For a pinned reference, the job stores the reference of the alias that
  pins the digest, not the reference that the job gives. The location of
  the pull is always the location of an alias. The job records no alias
  name, so the job record shows the reference alone. The job records the
  name of the alias that pins the digest (`image_pin_alias`) for a retry.
- The coordinator refuses any other VM job when it creates the job. The
  error tells you to add an image alias. No worker can have an image that
  no alias pins, and a worker never pulls an image during a job.

The coordinator also refuses a job (VM or container) that can never run
with the image of its alias:

- The `os` or `arch` characteristic of the job is not the os or arch of
  the alias. A job without `arch` agrees with each arch.
- An alias characteristic has another value than the job characteristic
  with the same key.
- The alias has a `worker_class` characteristic that is not the worker
  class of the job.

The error names the alias, the key, the value of the job, the value of the
alias, and the fix. See
[Image Aliases: Job creation refusals](./image-aliases.md#job-creation-refusals).

More rules:

- The organization image policy checks the resolved reference.
- A job stores an `oci://` reference in the plain form
  (`registry.example.com/team/image:tag@sha256:<digest>`).
- A retry and a re-run resolve the image again through the alias of the
  earlier run and use its current pin. For a direct pinned reference, the
  alias is the alias that pinned the digest at creation. The new job
  records the earlier image in `image_retry_of` when the image changed.
  The coordinator refuses the retry when that alias does not exist any
  more or does not fit the job. See
  [Image Aliases: Retry and rerun](./image-aliases.md#retry-and-rerun).
- A triggered child job that does not set `image` gets the reference, the
  alias name, and the digest of its parent. It does not resolve the alias
  again. The coordinator refuses the child when no worker can get its
  image any more: no alias of the job scope with the same kind and
  platform pins the digest, and the alias name of the job does not resolve
  (project alias, then organization alias) to an alias with the same kind
  and platform. For example, the alias was deleted.
- `image_digest` is a reserved characteristic. The coordinator sets it and
  removes a value that a job definition sets.
- A tag is a free-form string, and a hash is a valid tag. The digest is the
  pin, not the tag.
- The job record shows `alias (reference)` for an alias and the reference
  alone for a direct reference (`image_display`).

To manage aliases, roll out a new pin, and see the image state of workers,
see [Image Aliases and Worker Image Sets](./image-aliases.md).

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

Variables defined in `environment` are added alongside these.

The coordinator owns the event facts. A job definition cannot set them. Eval
refuses a job whose `environment` sets one of these names, and the
coordinator removes them from trigger output:

- `REACTORCIDE_ACTOR_SUBJECTS`, `REACTORCIDE_CI_APPROVALS`,
  `REACTORCIDE_RUN_AUTHORITY`, `REACTORCIDE_CI_POLICY`,
  `REACTORCIDE_CI_POLICY_REVISION`, `REACTORCIDE_DIFF_BASE`,
  `REACTORCIDE_SHA`, `REACTORCIDE_SOURCE_URL`, `REACTORCIDE_EVENT_ID`,
  `REACTORCIDE_EVENT_TYPE`, `REACTORCIDE_BRANCH`, `REACTORCIDE_REPO`,
  `REACTORCIDE_PROVIDER`, `REACTORCIDE_IS_FORK_PR`, `REACTORCIDE_JOB_KIND`,
  `REACTORCIDE_EVAL_DUAL_CI`, `REACTORCIDE_REPORT_GENERATION`,
  `REACTORCIDE_REPORT_TARGET_ID`, `REACTORCIDE_REF_CLASSES`,
  `REACTORCIDE_CACHE_NAMESPACE`, `REACTORCIDE_CACHE_WRITABLE`, and
  `REACTORCIDE_ARTIFACT_NAMESPACE`.
- Each name that starts with `REACTORCIDE_HEAD_`, `REACTORCIDE_BASE_`, or
  `REACTORCIDE_PR_`.

The match ignores case.

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

See [Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md)
for the policy schema, profile fields, approval rules, and GitHub reporting.
