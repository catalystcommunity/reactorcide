# Events, Checks, and Schedules

This document tells how Reactorcide changes an incoming event into work, how
it reports the result to the VCS provider, and how scheduled builds run.

## Events

An event is a request to answer the question "what must run for this?". The
coordinator records each event in the `ci_events` table before it starts any
work.

| Source | Example |
|---|---|
| `webhook` | GitHub or GitLab push, pull request, tag |
| `schedule` | A schedule fires (see [Schedules](#schedules)) |
| `manual` | `POST /api/v1/jobs` with `REACTORCIDE_JOB_KIND=eval` |

The event row holds the repository, branch or ref, commit, pull request,
actor, delivery ID, and a status: `received`, `evaluating`, `evaluated`,
`failed`, or `duplicate`. The row holds display-safe facts only. It does not
hold the raw webhook payload or secrets.

### Eval Job

Each event starts one eval job. The eval job is not a member of a workflow. Its
name starts with `Eval event`, for example:

- `Eval event push main (abc1234) on reactorcide`
- `Eval event pull_request #12 updated on reactorcide`
- `Eval event schedule nightly on main (abc1234) on reactorcide`

The eval job reads `.reactorcide/workflows/*.yaml`. It reports one decision for
each workflow file. The coordinator creates the workflows that must run.

When an event creates exactly one workflow, the UI shows the eval job as the
first step of that workflow. When an event creates zero or more than one
workflow, the event has its own page at `/app/events/<event_id>`.

### Duplicate Events

- A redelivery with the same delivery ID (`X-GitHub-Delivery`,
  `X-Gitlab-Event-UUID`) creates nothing. The coordinator returns the existing
  event.
- A new delivery for the same repository, event type, ref, commit, and pull
  request is a semantic duplicate. The `duplicate_events` setting controls it:

| Level | Values | Default |
|---|---|---|
| Organization | `run`, `skip` | `run` |
| Project | `inherit`, `run`, `skip` | `inherit` |

With `run`, the duplicate starts a new eval. With `skip`, the coordinator
records a `duplicate` event that points to the first event, and starts
nothing. To run an event again on purpose, use Retry on the eval job.

A retried eval job uses the same event. The coordinator uses the event ID as
the trigger operation ID, so a retried eval cannot create a second copy of a
workflow. The event links to the newest eval job.

A manual submit can send an `Idempotency-Key` header. A repeat with the same
key returns the first job.

### Decisions

Each workflow file gets one decision:

| Decision | Meaning |
|---|---|
| `run` | The workflow matched and the coordinator created it |
| `skipped` | The workflow did not match the event |
| `invalid` | The workflow file has an error |
| `not_admitted` | The execution profile refused the workflow |

Each decision has a list of structured reasons. A reason has a `kind` and
fields for that kind:

| Kind | Tells |
|---|---|
| `event` | The event type and whether the workflow accepts it |
| `branch` | The branch patterns and the branch |
| `paths` | The include and exclude patterns, the matched files (at most 50), and the exact count |
| `schedule` | The schedule, its cron expression, and the fire time |
| `policy` | The CI policy rule, the CI origin, the profile, and the policy reasons |
| `dependency` | The condition and the status of each dependency (job level) |
| `admission` | Why the execution profile refused a job (job level) |

The full list of changed files is in object storage at
`reactorcide/events/v1/<event_id>/changed-files.json`. The event row holds the
first 50 paths and the exact count.

The workflow page has a closed section, "Why is this workflow running?". It
shows the decision reasons of that workflow.

## CI Policy

CI policy applies only when the project stores a policy.

When the project has no stored policy, nothing is gated. Anybody can develop
CI changes:

- When the project sets a CI source (`default_ci_source_url` and
  `default_ci_source_ref`), the eval uses it.
- Else the eval uses the CI code of the event's own commit: the pushed commit
  for a push, and the head commit for a pull request from the same repository.
- A pull request from a fork always uses the CI code of the base commit. Fork
  content is never the CI source.
- No policy decision and no policy refusal is recorded.
  `Reactorcide Checks` does not fail for a CI change.

When the project stores a policy, pushes and pull requests go through it.
When a push or a pull request changes `.reactorcide/`, its own CI code runs
only when a policy rule permits it. Otherwise the CI code of the base runs:
the push's `before` commit (the default branch head for a new branch or a
tag), or the pull request's base commit. A refused CI change makes
`Reactorcide Checks` fail. When the stored policy cannot be loaded, the eval
uses the base CI code.

The policy stays in the coordinator API. You can change it at any time. See
[Organizations and CI Policy](./organizations-and-ci-policy.md).

## Conditional Jobs

A job in a workflow file can have an `if` block. All the predicates in the
block must be true for the job to run.

```yaml
jobs:
  build:
    image: golang:1.26
    command: make build
  docs:
    image: alpine
    command: make docs
    if:
      paths: {include: ["docs/**"], exclude: ["docs/*.tmp"]}
      branches: [main, "release/*"]
      events: [push, pull_request_updated]
      schedule: false
  publish-docs:
    needs: [docs]
    image: alpine
    command: make publish
```

| Predicate | True when |
|---|---|
| `paths` | One changed file matches `include` and does not match `exclude`. A plain list means `include`. |
| `branches` | The branch matches one pattern |
| `events` | The event type is in the list |
| `schedule` | `true`: the event is a schedule. `false`: the event is not a schedule. |

When the changed files or the branch are not known, that predicate is true,
and its reason tells this.

A job whose `if` is false becomes a node with status `skipped`. It gets
reasons, for example `skipped by if: no changed file matches paths include
[docs/**]`. Jobs that depend on it follow the normal condition rules. An
unknown key in `if` makes the workflow file `invalid`.

The older per-job `triggers` and `paths` keys in a workflow file now map to
`if` predicates. Before this change, the eval ignored them. Examine your
workflow files for these keys before you deploy.

Every node has a reason: waiting, submitted, skipped by `if`, skipped by
condition, and not admitted. The UI shows the reasons in the job table and the
graph.

## Reactorcide Checks Status

Each event posts one commit status with the context `Reactorcide Checks`. It
includes the eval job, every workflow of the event, and policy refusals.

| State | When |
|---|---|
| pending | The eval runs, or a workflow runs and no failure is known |
| success | All workflows passed or were skipped, or no workflow matched |
| failure | The eval failed, timed out, or was cancelled; a workflow failed, was cancelled, or was not admitted; or a stored policy refused a CI change |

A known failure is posted at once, also while other workflows run. The
description then tells how many workflows still run.

The status links to the workflow page when the event has one workflow. Else it
links to the event page.

A workflow can post one more status of its own:

```yaml
status:
  context: lint
```

The context must not be empty, must have 100 characters or fewer, and must not
be `Reactorcide Checks`. The workflow also counts toward `Reactorcide Checks`.

Events do not post `reactorcide/eval`, workflow-name contexts, or
`Reactorcide CI Policy`. Jobs and workflows from before this change keep the
old statuses.

**Before you deploy:** change branch protection in each repository. Require
`Reactorcide Checks`. Remove the old required contexts: `reactorcide/eval`,
the workflow names (for example `Reactorcide Jobs, repo: ...`), and
`Reactorcide CI Policy`. If you do not, branch protection waits for statuses
that are not posted.

## Dynamic Jobs

A running job can add jobs at runtime with `runnerlib trigger` or
`trigger_job()`. The `dynamic_jobs` setting controls this:

| Level | Values | Default |
|---|---|---|
| Organization | `allow`, `deny` | `allow` |
| Project | `inherit`, `allow`, `deny` | `inherit` |

When the setting is `deny`, the trigger API returns HTTP 403 with
`dynamic_jobs_denied`, and the coordinator writes an audit record. The eval
result is not a dynamic job. The setting does not apply to it.

## Schedules

A schedule runs one workflow on one branch at the times of a cron expression.
The schedule is in the coordinator API. The workflow definition is in the
repository on that branch.

A schedule has a name, a branch, a workflow reference (the explicit workflow
`id`, or the file path under `.reactorcide/workflows/`), a cron expression, a
timezone, and an enabled flag. Manage schedules in the project page of the UI,
or with the CLI:

```bash
reactorcide schedules list --project <project-id>
reactorcide schedules create --project <project-id> --name nightly \
  --branch main --workflow nightly --cron "0 3 * * *" --timezone UTC
reactorcide schedules disable <schedule-id>
reactorcide schedules fires <schedule-id>
```

At each fire, the coordinator:

1. Gets the branch head commit from the VCS provider.
2. Records a `schedule` event.
3. Starts an eval job that evaluates only the referenced workflow, on that
   commit. The `on` and `paths` filters of the workflow do not apply.

If the referenced workflow is not on the branch, the fire records the decision
`invalid` with the reason `workflow not found`. The schedule shows the error.
The coordinator does not delete the schedule. To delete a schedule, use the API
or the CLI.

If the eval job fails, the schedule shows the error. A later successful fire
clears it.

### One Scheduler

Only one coordinator replica runs the scheduler at a time. The replica holds
the lease `scheduler` in the `coordinator_leases` table. It renews the lease
every 10 seconds. The lease expires after 30 seconds. When the holder stops,
another replica takes the lease.

Each fire has one row in `ci_schedule_fires`, unique by schedule and fire time.
Thus a fire occurs one time, also when two replicas run the scheduler at the
same time for a short period.

When the scheduler did not run for some time, it fires one time for the newest
missed time. It does not fire for each missed time. A fire that stays in the
`firing` state for more than 10 minutes becomes `failed`.
