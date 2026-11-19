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
| `manual` | A manual run on a ref (see [Manual Runs on a Ref](#manual-runs-on-a-ref)), or `POST /api/v1/jobs` with `REACTORCIDE_JOB_KIND=eval` |

The event row holds the repository, branch or ref, commit, pull request,
actor, delivery ID, the [ref classes](./ref-classes.md) of the event, and a
status: `received`, `evaluating`, `evaluated`, `failed`, `duplicate`,
`awaiting_approval`, `rate_limited`, or `skipped`. A `skipped` or
`rate_limited` event has no eval job. Its summary has `skip_reasons`, with
the kind `draft`, `shed`, or `rate_limit` and a text that tells why.
The coordinator computes the ref classes from trusted facts before it stores
the event. The eval job, the workflows, and their jobs get the classes of the
event. The row holds display-safe facts only. It does not
hold the raw webhook payload or secrets.

### Eval Job

Each event starts one eval job. The eval job is not a member of a workflow. Its
name starts with `Eval event`, for example:

- `Eval event push main (abc1234) on acme/app`
- `Eval event pull_request #12 updated on acme/app`
- `Eval event schedule nightly on main (abc1234) on acme/app`

The last part is the full name of the repository.

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

A manual eval submit (`POST /api/v1/jobs` with `REACTORCIDE_JOB_KIND=eval`)
can send an `Idempotency-Key` header. A repeat with the same key returns the
first job. When the caller cannot read the first job, or the first request
still runs, the answer is `409`.

### Decisions

Each workflow file gets one decision:

| Decision | Meaning |
|---|---|
| `run` | The workflow matched and the coordinator created it |
| `skipped` | The workflow did not match the event |
| `invalid` | The workflow file has an error |
| `not_admitted` | The execution profile refused the workflow |
| `carried` | A re-evaluation after an approval did not change the decision. The event keeps the workflow of the earlier event and does not run it again. |

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
| `ref_class` | The ref classes of the event (`classes`) and the operator rules that added classes (`rule_ids`). Each decision of an event has it. |
| `approval` | A `carried` decision: the approval did not change the CI origin or the profile of the workflow |
| `priority` | The lane and the priority of a job (job level, see [Priority and Lanes](./priority.md)) |

The full list of changed files is in object storage at
`reactorcide/events/v1/<event_id>/changed-files.json`. The event row holds the
first 50 paths and the exact count.

The workflow page has a closed section, "Why is this workflow running?". It
shows the decision reasons of that workflow.

## CI Policy

CI policy applies when the project stores a policy. It also applies to a pull
request of a project with no stored policy when the organization has the
`secure` defaults profile (see [Built-in policy](#built-in-policy)).

When the project has no stored policy and the organization has the `legacy`
defaults profile, nothing is gated. Anybody can develop CI changes:

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

### Built-in policy

A new organization has the `secure` defaults profile. When a project of a
`secure` organization has no stored policy, each pull request gets the
built-in policy:

```yaml
version: 1
defaults:
  ci_source: base
  profile: standard
head_ci: []
```

- The eval uses the CI code of the base commit.
- A head that does not change `.reactorcide/` runs normally. No extra setup
  is necessary.
- A head that changes `.reactorcide/` runs the base CI. The CI change is a
  policy refusal, so `Reactorcide Checks` fails and tells why. A person with
  an `override_approvers` subject (by default `repository_write` or the
  project owner) comments `/reactorcide override-ci`. Then the head's own CI
  runs with `override_profile` (default `pr-untrusted`).
- A stored policy replaces the built-in policy. Add a `head_ci` rule to let a
  CI change run without `override-ci`.

The built-in policy applies to pull requests only. A push keeps the behavior
of a project with no stored policy: it runs its own CI code. Only a principal
with write access can push to the repository.

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

A project in a GitHub App installation posts a check run with the same name
instead of a commit status. The check run has a summary, line annotations,
and a Re-run button. See [GitHub App](./github-app.md).

| State | When |
|---|---|
| pending | The eval runs, or a workflow runs and no failure is known; the run gate holds the pull request; the webhook was over the rate limit (`Rate limited: nothing ran; redeliver the webhook or push again`); or the coordinator skipped the event (`Skipped: draft pull request`, or `Skipped: REASON` for a shed skip) |
| success | All workflows passed or were skipped, or no workflow matched |
| failure | The eval failed, timed out, or was cancelled; a workflow failed, was cancelled, or was not admitted; or a stored policy refused a CI change |

When the [required set](./required-set.md) of the project is on, only
required workflows count. An advisory or quarantined workflow does not fail
the status and does not keep it pending, also when it fails, is cancelled, or
is not admitted. A CI change that a stored policy refuses still fails the
status. With the set off (the default), every workflow counts.

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
be `Reactorcide Checks`. A required workflow also counts toward `Reactorcide
Checks`. An advisory workflow posts no status, also not its `status.context`
(see [Required Set](./required-set.md)). Thus require only `Reactorcide
Checks` in branch protection.

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
| Organization | `allow`, `deny` | `deny` for a new organization (`secure` defaults profile); `allow` for an organization from before migration 000055 |
| Project | `inherit`, `allow`, `deny` | `inherit` |

When the setting is `deny`, the trigger API returns HTTP 403 with
`dynamic_jobs_denied`, and the coordinator writes an audit record. The eval
result is not a dynamic job. The setting does not apply to it.

The trigger API accepts only the job token of the parent job, while the
parent is `scheduling` or `running`. Other credentials get HTTP 403, also the
admin token. A child job gets the event, the ref classes, and the trusted CI
authority of its parent, so a finished job cannot add jobs.

Change the setting with the CLI (`reactorcide orgs update ORG --dynamic-jobs
allow`, `reactorcide projects update PROJECT --dynamic-jobs inherit`), the
REST API (`dynamic_jobs` on `PUT /api/v1/organizations/NAME` and `PUT
/api/v1/projects/ID`), or the UI.

## Draft Pull Requests

Reactorcide reads the `draft` flag of a GitHub pull request and the `draft`
(or `work_in_progress`) flag of a GitLab merge request. The `draft_prs`
setting tells what a draft does:

| Level | Values | Default |
|---|---|---|
| Organization | `run`, `skip`, `skip_when_shedding` | `run` |
| Project | `inherit`, `run`, `skip`, `skip_when_shedding` | `inherit` |

- `run`: a draft runs like any other pull request. This is the behavior from
  before the setting.
- `skip`: a draft runs nothing.
- `skip_when_shedding`: a draft runs, except while the
  [shed level](#shed-levels) in force has the action `skip_drafts`.

A skipped draft records an event with the status `skipped` and a skip reason
of the kind `draft` or `shed`. The run gate does not apply to it. "Reactorcide
Checks" is `pending` with the text `Skipped: draft pull request`. A draft
that the shed level skips is `pending` with the text `Skipped: REASON`.
Reactorcide does not post `success` for a skipped event: a success can
satisfy a branch protection rule for a head that ran nothing. A draft cannot
merge, and the `ready_for_review` event posts a new status for the same head
that replaces the pending status.

The GitHub action `ready_for_review` is a `pull_request_updated` event with
`draft=false`. Thus each workflow that runs on `pull_request_updated` runs
when the draft becomes ready. No workflow file needs a new event name. The
action `converted_to_draft` starts nothing. A re-evaluation after
`approve-ci` or `override-ci`, and a rerun, always run, also for a draft.

Set the value with `reactorcide orgs update ORG --draft-prs skip`,
`reactorcide projects update PROJECT --draft-prs inherit`, the REST field
`draft_prs`, or the UI (Admin page for organizations, project page for
projects).

## Shed Levels

A shed level is overload protection. It does not give one tenant more
capacity than another. The global level and each organization level are one
of `normal`, `degraded`, or `critical`. The level in force for an
organization is the higher of the global level and the organization level.

Each level has actions. The built-in actions are:

| Action | `degraded` | `critical` |
|---|---|---|
| `skip_drafts`: skip drafts of projects with `draft_prs: skip_when_shedding` | yes | yes |
| `pause_schedules`: a schedule fire fails with the reason and starts no work | yes | yes |
| `lower_pr_priority`: all pull request work (evals and jobs) moves 100 down, one lane ([Priority and Lanes](./priority.md#shed-levels)) | yes | yes |
| `skip_advisory`: skip advisory workflows | no | yes |
| `refuse_manual_runs`: refuse manual runs from callers that are not organization administrators | no | yes |

`normal` has no actions. An organization can replace the actions of each
level. A refused manual run gets HTTP 503 with `Retry-After: 60` and the
error `shed_level`. The advisory workflow code reads
`shedding.SkipAdvisory(ctx, store, orgID)`.

A paused schedule fire has the status `failed` and the error `schedules are
paused: shed level degraded (global)`. The scheduler does not fire the missed
times again when the level goes back to `normal`.

Set the levels with the admin API token or an administrator session:

```bash
reactorcide shed get [--org ORG]
reactorcide shed set --level degraded --reason "worker outage"
reactorcide shed set --level critical --org example
reactorcide shed set-actions --org example --file shed-actions.yaml
reactorcide shed set-actions --org example --reset
```

`shed-actions.yaml` has the keys `degraded` and `critical`. Each key has the
five actions as `true` or `false`.

| REST | Purpose | Authority |
|---|---|---|
| `GET /api/v1/shed` | Global level | `organizations:read` |
| `PUT /api/v1/shed` with `{"level": "...", "reason": "..."}` | Set the global level | global `organizations:manage` (admin token) |
| `GET /api/v1/organizations/NAME/shed` | The state in force for the organization | `organizations:read` |
| `PUT /api/v1/organizations/NAME/shed` with `level`, `actions`, or `reset_actions`, and `reason` | Set the organization level or actions | `organizations:manage` for the organization |

The UI has the global level on the Admin page and the organization level on
the CI trust page of the organization (CSIL `get-shed-state` and
`set-shed-level`). The coordinator writes an audit record for each change:
`shed.set_global` or `shed.set_org`, with the old level, the new level, and
the reason.

## Rate Limits

Rate limits are overload protection, not tenant capacity. Each coordinator
replica keeps a token bucket for each key in memory. With N replicas behind a
load balancer, a key can get up to N times the rate. A restart empties the
buckets. A rate of `0` turns a limit off.

| Limit | Key | Environment variables | Default |
|---|---|---|---|
| Webhook deliveries | Project | `REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE`, `REACTORCIDE_WEBHOOK_RATE_LIMIT_BURST` | 600 a minute, burst 200 |
| REST API requests | Principal (API token) | `REACTORCIDE_API_RATE_LIMIT_PER_MINUTE`, `REACTORCIDE_API_RATE_LIMIT_BURST` | off (0), burst 300 |
| Federation token exchange (`POST /api/v1/federation/token`) | Source address (TCP peer, or `X-Forwarded-For` from a trusted proxy: `REACTORCIDE_TRUSTED_PROXY_CIDRS`) | `REACTORCIDE_FEDERATION_RATE_LIMIT_PER_MINUTE`, `REACTORCIDE_FEDERATION_RATE_LIMIT_BURST` | 300 a minute, burst 100 |
| Federation tokens issued | Organization and issuer (verified tokens only) | `REACTORCIDE_FEDERATION_ISSUER_RATE_LIMIT_PER_MINUTE`, `REACTORCIDE_FEDERATION_ISSUER_RATE_LIMIT_BURST` | 600 a minute, burst 200 |
| Replies to refused pull request commands | Pull request, and project | none (fixed) | 1 a minute, burst 3, for each pull request; 10 a minute, burst 20, for each project |
| Diagnosis posts ([Failure Diagnosis](./diagnosis.md#rate-limit)) | Principal | `REACTORCIDE_DIAGNOSIS_RATE_LIMIT_PER_MINUTE`, `REACTORCIDE_DIAGNOSIS_RATE_LIMIT_BURST` | 30 a minute, burst 10 |

The CSIL-RPC endpoint (`/csil/v1/rpc`: the UI service, the auth service, and
the worker service) has no rate limit. Workers call it often (lease polls,
log chunks, artifact chunks), and each worker has its own credential. UI
calls come from authenticated browser sessions through the webapp bridge.
Put the endpoint
behind the rate limit of the ingress when it is reachable from the internet.

The webhook limit is on because 600 deliveries a minute is far above the
traffic of a busy repository. The API limit is off because workers, jobs, and
automation use the REST API at rates that are not yet measured. Measure the
traffic before you set it.

Only a pull request or push delivery that the project processes counts
toward the webhook limit. A comment, a review, and a ping do not count. Over
the limit:

- The coordinator returns HTTP 429 with `Retry-After` (seconds).
- It records the event with the status `rate_limited`, writes the audit
  record `ci_event.rate_limited`, and posts "Reactorcide Checks" as pending.
- It starts no eval job. A redelivery of the same delivery (for example
  "Redeliver" in the GitHub webhook settings) evaluates the event. A new
  delivery of the same change is not a duplicate of a `rate_limited` or
  `skipped` event.

Over the API limit, the coordinator returns HTTP 429 with `Retry-After` before
it runs the request. The counter `reactorcide_rate_limited_total{kind}` counts
refused requests (`kind` is `webhook`, `api`, `federation`,
`federation_issuer`, or `diagnosis`).

## Manual Runs on a Ref

A principal with permission can start a new evaluated run on any branch, tag,
or pull request head of a project.

| Channel | Request |
|---|---|
| REST | `POST /api/v1/projects/{project_id}/runs` with `{"ref": "main"}`, `{"ref": "refs/tags/v1.2.0"}`, or `{"pr_number": 12}`, and an optional `"workflow"` |
| CLI | `reactorcide run --project PROJECT --ref REF [--workflow WORKFLOW]` or `--pr N` |
| CSIL | `start-run` |
| UI | The Run card on the project page |

The coordinator does these steps:

1. It resolves the commit through the VCS API: the branch head, the tag
   commit, or the current head of the pull request. The caller does not give
   the commit.
2. It records a `manual` event. The actor of the event is the principal that
   started the run. A manual run is never a duplicate of an earlier event.
3. It starts the eval job, as for a webhook delivery of that ref: a push for
   a branch, `tag_created` for a tag, and `pull_request_updated` for a pull
   request head. The run gate, the CI policy, the ref classes, and the
   execution profile of that ref apply. A run never gets the authority of an
   earlier run.

- A branch or a tag has no "before" commit. The default branch of the
  repository is its trusted CI base, as for a new branch that is pushed. The
  coordinator gets the default branch from the VCS API and refuses the run
  when it cannot.
- A pull request run uses the earlier actor of the head for the admission
  facts, as a rerun does.
- `workflow` names one workflow: a workflow ID, or a path such as
  `.reactorcide/workflows/build.yaml`. The eval then runs only that workflow,
  without its `on` and `paths` filters, as a schedule does. The decision
  reason says that a manual run selected it. Without `workflow`, the eval
  evaluates every workflow for the event.
- The project event settings (`target_branches`, `allowed_event_types`) still
  apply. A ref that they do not process gives `422` with the error
  `filtered`.
- An `Idempotency-Key` header on the REST request makes a repeat return the
  first event.
- Permission: the retry tier of the project (a project owner, an
  administrator, or a token with `jobs:retry` or `workflows:control`). A token
  also needs `jobs:submit`.
- At a shed level with `refuse_manual_runs` (by default `critical`), only an
  organization administrator can start a manual run.

The response has `event_id`, `status`, `eval_job_id`, `ref`, and
`commit_sha`.

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
   commit. The event gets the ref class `schedule`, and the classes of
   operator rules that match the event type `schedule` and the branch. The `on` and `paths` filters of the workflow do not apply.

The eval job of a fire goes to a queue as the eval job of a webhook does. When
egress control is on for the organization, the project, or the execution
profile, the eval job requires a worker with an egress proxy. See
[Egress Proxy](./egress-proxy.md) ("Automatic routing").

When the shed level in force pauses schedules, the fire fails with the reason
and starts no work. See [Shed Levels](#shed-levels).

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
