# Event-Driven CI Plan

Status: approved, 2026-11-14. Owner decisions:

- D1: semantic duplicates (new delivery ID, same repo, event type, commit
  and PR) run again by default. An org or project setting can make them
  duplicates.
- D2: dynamic jobs are allowed by default. Org and project can deny.
- D3: push events go through CI policy.
- D4: keep the Commit Status API.
- D5: schedules live in the coordinator API and point to a workflow
  definition in the repo on a branch.

## Goal

An incoming event (webhook, schedule, manual submit) is a first-class
record. One eval job answers "what must run for this event?". Workflows
and jobs are declarative. Policy stays in the coordinator API and can
change at any time. Every workflow and every job can say why it runs or
why it does not run. GitHub shows one status, "Reactorcide Checks".

## Current State (summary of findings)

- The eval job is a plain `jobs` row. Its name is `eval: PR #N ... on
  repo`. Event facts are in env vars and `jobs.notes`. No event row
  exists.
- No webhook delivery is deduplicated. `X-GitHub-Delivery` is not read.
  A redelivery creates a new eval, new workflows and new jobs.
- The eval writes matched workflows only. Unmatched workflows, the
  changed-file list, matched paths and policy reasons are in the eval
  log only. `workflow_events` are written but no API serves them.
- Push evals get no policy, actor or approval facts. A push runs the CI
  code of the pushed commit.
- GitHub statuses: one context per root workflow (the workflow name),
  plus `reactorcide/eval` and `Reactorcide CI Policy`. Commit Status
  API only. `cancelled` maps to `pending`. `handlePRMerged` can post
  per-node statuses.
- Job conditions are `depends_on` plus `condition` (all_success,
  always, any_failed). Per-job `paths` are removed by runnerlib.
- Dynamic jobs (`runnerlib trigger`, `trigger_job()`) are always on. No
  org or project setting controls them.
- No scheduler exists. Every background loop runs on every coordinator
  replica. No lease or leader utility exists. Corndogs has no delayed
  tasks, but `SubmitKeyedTask` gives keyed, at-most-once submits.

## Design

### 1. Events

New table `ci_events`:

- `event_id` (ULID), `org_id`, `project_id`, `source` (webhook,
  schedule, manual, retry), `provider`, `delivery_id`, `event_type`,
  `repo`, `ref`, `branch`, `commit_sha`, `base_sha`, `pr_number`,
  `actor`, `schedule_id`, `received_at`, `status` (received, evaluating,
  evaluated, failed, duplicate), `eval_job_id`, `summary jsonb` (small,
  display-safe facts only; never secrets or raw payloads).
- `idempotency_key text UNIQUE`.
  - Webhook: `provider:delivery_id`. A redelivery returns the existing
    event and creates nothing.
  - Semantic key also checked: `provider:repo:event_type:sha:pr`.
    Setting `duplicate_events` on org (default) and project (override:
    inherit, run, skip). Default `run`: a new delivery ID with the same
    semantic key starts a new eval. With `skip`, it is recorded as a
    `duplicate` event that points to the first event.
  - Schedule: `schedule:<schedule_id>:<fire_time>`.
  - Manual: client-supplied key, or none.
- Eval job name: `Eval event <type> <subject>`, for example
  `Eval event push main (abc1234) on repo` or
  `Eval event pull_request #12 updated on repo`. Facts move from
  `notes` to the event row. The job keeps `event_id`.
- The eval writes its trigger result with `operation_id = event_id`
  instead of a random UUID. A retried eval for the same event then
  cannot create a second copy of a workflow (existing unique index on
  parent job + operation + security ID changes to event + security ID).

### 2. Eval Result Is a Decision Record

The eval reports every workflow file, not only matches. New table
`ci_event_workflow_decisions`: `event_id`, `workflow_security_id`,
`workflow_name`, `source_file`, `decision` (run, skipped, invalid,
not_admitted), `reasons jsonb`, `workflow_id` (when created).

`reasons` is structured, for example:

- `{kind: "event", matched: "push"}`
- `{kind: "branch", pattern: "main", value: "main"}`
- `{kind: "paths", include: ["api/**"], matched_files: ["api/x.go"],
  matched_count: 14}` (file list capped, count exact)
- `{kind: "schedule", schedule_id, cron, fire_time}`
- `{kind: "policy", rule_id, ci_origin, profile, reasons: [...]}`
  (keeps `cipolicy.Decision.Reasons`, which today is discarded)
- `{kind: "manual", actor}`

The full changed-file list is stored once per event in object storage
(it can be large), with a capped preview in the event summary.

Unmatched workflows are stored as `skipped` decisions, not as workflow
rows, so the workflow list does not fill with empty rows.

### 3. One Workflow Folds the Eval In

When an event creates exactly one workflow, the eval job shows as the
first node of that workflow ("Evaluate event"), in the node table and
the graph. With zero or several workflows, the event has its own page
that lists every workflow decision. The workflow list keeps the
current fold rule.

### 4. Declarative Workflows, Conditional Jobs, Reasons

- Job-level `if` with declarative predicates only, no expression
  language: `paths`, `branches`, `events`, `schedule`. Runnerlib stops
  removing per-job `paths`.
- A job whose `if` is false gets a node with status `skipped` and a
  structured reason (same shapes as section 2). A job skipped by
  `depends_on`/`condition` already has `decision_reason`; it changes to
  the same structured shape.
- Serve `workflow_events` and node reasons through `get-workflow`.

### 5. Dynamic Jobs Setting

- `dynamic_jobs` on org (default) and project (override: inherit,
  allow, deny). Enforced in `SubmitTriggers` for `trigger_type =
  runnerlib` (not for the eval result).
- Default: allow (D2).
- A refused trigger fails the calling job's trigger call with a clear
  error and writes an audit record.

### 6. Policy for Push Events

Push evals get actor, approval and policy facts like PR evals (D3). A
push that changes `.reactorcide/` runs the pushed CI code only when a
policy rule allows it. Otherwise it runs the CI code of the push's
`before` commit. When a project has no policy, the built-in default
allows actors with `repository_write`, so behavior stays the same until
an operator tightens it. A push with no `before` commit (new branch)
uses the default branch head as the base.

### 7. GitHub Status: "Reactorcide Checks"

- One commit status per event, context `Reactorcide Checks`. It covers
  the eval, every workflow the event created, and policy refusals.
  - pending while the eval or any workflow runs
  - success when every workflow is success or skipped, or no workflow
    matched
  - failure when the eval fails, a workflow fails or is not admitted,
    or a policy violation blocks a required workflow
  - `cancelled` maps to failure (today it stays pending forever)
- `target_url` is the event page, or the workflow page when there is
  one workflow.
- A workflow can publish an extra status with `status: {context:
  "lint"}` in its YAML. It does not replace `Reactorcide Checks`.
- `reactorcide/eval` and `Reactorcide CI Policy` are no longer posted.
  Fix the `handlePRMerged` per-node status leak.
- Keep the Commit Status API (D4). The Checks API needs a GitHub App.
- Deployment note: branch protection rules must require
  `Reactorcide Checks` instead of the old workflow names.

### 8. Scheduled Builds

- Schedules live in the coordinator API (D5). A schedule row names a
  project, a branch, a workflow reference (explicit workflow ID or the
  workflow file path under `.reactorcide/workflows/`), a cron
  expression, a timezone and an enabled flag. The UI and CLI manage it.
- The repo holds the definition. If the referenced workflow is missing
  on the branch at fire time, the fire is recorded with decision
  `invalid` and reason `workflow not found`, the schedule shows the
  error, and the schedule is NOT deleted. Deleting a schedule is an
  explicit API action.
- Table `ci_schedules` (project, branch, workflow_ref, cron, timezone,
  enabled, next_fire_at, last_fire_at, last_event_id, last_error) and
  `ci_schedule_fires` with `UNIQUE (schedule_id, fire_time)`.
- One scheduler at a time: new `coordinator_leases` table (`name`,
  `holder`, `expires_at`, `renewed_at`). A replica takes the lease with
  a conditional update, renews it every 10s, and the lease expires
  after 30s. Another replica takes over when the holder stops renewing.
  The unique fire row makes a fire exactly-once even if two replicas
  briefly both think they hold the lease.
- A fire resolves the branch head SHA through the VCS API, creates a
  `schedule` event, and starts an eval that runs only the scheduled
  workflow on that commit.
- Missed fires while no scheduler ran: fire once for the newest missed
  time, not once per missed time.
- The lease utility is general. Later, move the other loops (report
  reconciler, reaper, prunes) to it.

### 9. UI

- Event page: event facts, eval job link, every workflow decision with
  its reasons, link to the changed-file list.
- Workflow page: a closed `<details>` section, "Why is this workflow
  running?". It shows the trigger (event, branch, schedule, manual),
  the matched paths and files, and the policy decision.
- Node table and graph: a reason for every node, including skipped
  nodes ("Skipped: no changed file matches `docs/**`").
- Project page: schedules (enable, disable, next fire), dynamic-jobs
  setting.

## Work Breakdown

1. Events table, idempotency, eval naming, `operation_id = event_id`.
2. Decision records from runnerlib eval; coordinator stores them.
3. Job-level `if`, skipped nodes with structured reasons, serve
   `workflow_events`.
4. `Reactorcide Checks` aggregate status, per-workflow extra status,
   remove old contexts, fix merge leak.
5. Dynamic-jobs setting.
6. Push-event policy facts.
7. Lease utility, schedules, scheduler loop, schedule events.
8. UI: event page, "why" section, node reasons, project settings.
9. Docs (STE): DESIGN.md, docs/workflow-design.md,
   docs/organizations-and-ci-policy.md, migration and branch-protection
   notes.

Items 1-2 come first. Items 3, 4, 5, 6 and 7 can then run in parallel.
Item 8 needs the CSIL contracts from items 1-7.

## Compatibility

- Existing jobs without an event row keep working. The UI shows them as
  before.
- Old `reactorcide/eval` and per-workflow statuses on already-open PRs
  stay until the next event on the PR.
- Runnerlib and coordinator must deploy together. The coordinator
  accepts both the old trigger result (matches only) and the new one
  (decisions) for one release.
