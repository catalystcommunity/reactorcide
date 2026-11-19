# Workflow Design

This document describes the v1 workflow model. It builds on the existing `triggers.json` mechanism: jobs can still emit follow-up work, but the coordinator owns dependency evaluation, state merging, and external status reporting.

## Goals

- Run independent jobs in parallel.
- Wait for dependencies without occupying worker slots.
- Make every run, wait, and skip decision explainable in the data model.
- Allow jobs to publish workflow state for later jobs.
- Support dynamic fan-out with literal `for_each` lists.
- Expose one VCS status per event, `Reactorcide Checks`, not one per job or workflow.
- Render a rolling PR comment that explains workflows, jobs, waits, skips, and failures.

## Execution Model

A workflow is a coordinator-owned DAG. Jobs are execution units inside a workflow.

1. A job emits `triggers.json`, usually through `runnerlib.workflow`.
2. The coordinator creates a workflow instance if one does not already exist for the parent job.
3. Triggered jobs become workflow nodes.
4. Ready nodes are submitted to Corndogs as normal jobs.
5. Waiting nodes remain in workflow tables and do not consume worker slots.
6. When a job reaches a terminal state, the coordinator merges its workflow output, records events, and reevaluates dependent nodes.
7. The workflow reaches a terminal status when every node is terminal.

## Trigger Shape

V1 workflow nodes are declared dynamically through `triggers.json`, either by writing the file directly or by using `runnerlib.workflow`.

```json
{
  "type": "trigger_job",
  "workflow": {
    "name": "Reactorcide Jobs",
    "vars": {
      "image_tag": "abc123"
    }
  },
  "jobs": [
    {
      "job_name": "build",
      "job_file": ".reactorcide/jobs/build.yaml"
    },
    {
      "job_name": "test",
      "depends_on": ["build"],
      "condition": "all_success",
      "for_each": ["unit", "integration"],
      "item_var": "SUITE",
      "job_file": ".reactorcide/jobs/test.yaml"
    },
    {
      "job_name": "report-failure",
      "depends_on": ["test"],
      "condition": "any_failed",
      "job_file": ".reactorcide/jobs/report.yaml"
    }
  ]
}
```

The normal trigger fields still work: `job_name`, source fields, `container_image`, `job_command`, `code_dir`, `job_dir`, `working_dir`, `run_as_user`, `run_as_administrator`, `priority`, `timeout`, `capabilities`, `image_pull_secrets`, `worker_class`, `characteristics`, `resources`, `quarantine_policy`, and `env`.

The `job_file` field in this example needs a workspace on the coordinator.
The trigger API (`POST /api/v1/jobs/{job_id}/triggers`) has no workspace. It
refuses a trigger with `job_file` (`job_file ... requires workspace-backed
trigger processing`). Runnerlib eval resolves each `job_file` of a workflow
YAML before it sends the triggers. A job script that calls `trigger_job()`
must send complete jobs (`container_image` and `job_command`).

### Multiple workflows per event

One event can produce more than one workflow. Use the `workflows` array. Each entry is a named workflow with its own `jobs`. This is the form the eval job emits when it finds `.reactorcide/workflows/*.yaml` files.

```json
{
  "type": "trigger_job",
  "workflows": [
    {
      "name": "Reactorcide PR",
      "jobs": [
        { "job_name": "test-go", "container_image": "golang:1.26", "job_command": "go test ./..." }
      ]
    },
    {
      "name": "Docs",
      "jobs": [
        { "job_name": "build-docs", "container_image": "alpine", "job_command": "make docs" }
      ]
    }
  ]
}
```

The parent evaluation job spawns each workflow. It is not a workflow member in
the execution model. Each workflow gets its own name. The event posts one
status, `Reactorcide Checks`, for all its workflows. See
[Events, Checks, and Schedules](./events-and-schedules.md).

The coordinator processes each workflow in a separate database transaction.
It publishes the workflow status after that transaction commits. A refusal in
one workflow does not stop the other workflows in the request.

If an execution profile cannot admit a workflow node, the coordinator stores
the workflow and the failed node. It sets the disposition to `not_admitted`.
It also stores the reason in the workflow, the workflow event, the audit log,
the VCS status description, and the pull-request report. No job row exists for
the refused node because the node did not start.

The trigger API returns HTTP 201 when it records all workflow outcomes. Its
response includes `accepted`, `not_admitted`, or `submission_failed` for each
workflow. A malformed request or a storage failure remains an API error.

The `workflows` array takes precedence. When it is absent, the legacy `workflow` plus `jobs` form collapses to one workflow. When the workflow name is empty, the coordinator uses the default name `Reactorcide Jobs, repo: <name>`.

The coordinator cannot read `job_file` references from an API submission, because it has no workspace. runnerlib resolves each `job_file` before it submits, so the `workflows` form always contains complete job specs.

## Conditions

V1 keeps conditions intentionally small:

| Condition | Meaning |
|---|---|
| `all_success` / `all_success(needs)` | Every dependency completed successfully |
| `any_failed` / `any_failed(needs)` | At least one dependency failed, timed out, or was cancelled |
| `always` / `always()` | Run once dependencies are terminal, regardless of result |

Skipped dependencies count as terminal but not successful. Named predicates such as `success("node")` and `failed("node")` are future extensions.

A job can also have an `if` block with declarative predicates: `paths`,
`branches`, `events`, and `schedule`. A job whose `if` is false becomes a
`skipped` node with structured reasons. See
[Conditional Jobs](./events-and-schedules.md#conditional-jobs).

## For Each

`for_each` expands one trigger into one workflow node per literal list value:

```json
{
  "job_name": "test",
  "depends_on": ["plan"],
  "for_each": ["unit", "integration"],
  "item_var": "SUITE"
}
```

This creates `test[0]` with `SUITE=unit` and `test[1]` with `SUITE=integration`. A dependency on `test` means the whole expanded group. The item value and index are also stored on each workflow node for debugging and UI display.

Expression-based `for_each` over workflow vars is future work.

## Workflow Variables

Reactorcide reserves the short `RC_WF_` prefix for workflow metadata in the
job environment:

| Variable | Meaning |
|---|---|
| `RC_WF_ID` | Workflow instance id |
| `RC_WF_NODE_ID` | Workflow node id |
| `RC_WF_NODE_NAME` | Display node name, such as `test[0]` |
| `RC_WF_RUN_ID` | Node run id |
| `RC_WF_VARS_FILE` | JSON file containing all workflow vars visible to the job |
| `RC_WF_VARS_JSON` | Inline JSON with the same vars, set on remote leases |
| `RC_WF_OUTPUT_FILE` | JSON file the job writes to publish vars and outputs |

User vars:

- Jobs read workflow vars from `RC_WF_VARS_FILE` (or `RC_WF_VARS_JSON` when
  set). Workflow vars are not injected as individual environment variables.
- `for_each.item_var: SUITE` injects `SUITE` for the expanded job.

`RC_WF_VARS_FILE` is a flat JSON object:

```json
{
  "image_tag": "abc123",
  "matrix": ["unit", "integration"],
  "build.image_digest": "sha256:..."
}
```

Jobs publish state by writing `RC_WF_OUTPUT_FILE`:

```json
{
  "vars": {
    "image_tag": "abc123"
  },
  "outputs": {
    "image_digest": "sha256:..."
  }
}
```

`runnerlib.workflow` exposes `set_workflow_var(key, value)`, `set_workflow_output(key, value)`, and `workflow_vars()` helpers for this file contract.

## State Merge Rules

Every merge produces durable events.

- Each node owns its `outputs`.
- Node outputs are exposed as workflow vars named `<node_name>.<key>`.
- Workflow-level `vars` may be set by jobs through `RC_WF_OUTPUT_FILE`.
- If one job writes a var key, accept it.
- If multiple jobs write the same var key with the same JSON value, accept it and record a deduplicated merge.
- If multiple jobs write the same var key with different JSON values, mark the workflow failed.

Future merge strategies can add append, append-unique, object-merge, or explicit last-writer-wins behavior. Last-writer-wins should not be the default because it makes parallel behavior hard to debug.

## Owners and Contact

A workflow can name its owner and a contact. Both keys are optional.

```yaml
id: payments
name: "Payments"
owners:
  # One primary group: a Reactorcide group name, or vcs_team:ORG/SLUG.
  group: payments-ci
  # Optional: more owner subjects (at most 20).
  subjects:
    - vcs_user:github/alice
contact:
  channel: "#payments-ci"          # at most 100 characters
  help_url: https://wiki.example/payments-ci   # http or https, at most 500
  email: payments-ci@example.com   # one plain address
```

Rules:

- `owners.group` is one Reactorcide group name (lowercase letters, digits,
  and hyphens) or `vcs_team:ORG/SLUG`. `reactorcide_group:NAME` is also
  accepted and stored as `NAME`.
- `owners.subjects` names people only: `principal:`, `service:`,
  `identity:`, `vcs_user:`, `vcs_team:`, or `reactorcide_group:` subjects.
  `*`, `repository_write`, `project_owner`, `workflow_owner`, and
  `principal_kind:` are not owner subjects.
- An `owners` block must set `group` or `subjects`.
- An invalid `owners` or `contact` block makes the workflow file invalid. The
  event page shows the error.

### The trusted definition sets the owner

The coordinator reads `owners` and `contact` from the TRUSTED CI definition
only. A pull request cannot change the owner of the workflow that it runs.

| Run | Owner source |
| --- | --- |
| Policy candidates (base and head CI), head CI not granted | The base definition. |
| Policy candidates, head CI granted (policy rule or override-ci) | The base definition, not the head. |
| A workflow that only the head has | No owner. |
| One CI checkout at a trusted ref (see below) | That definition. |
| One CI checkout at the pull request head (no stored CI policy) | The latest earlier trusted snapshot of the same workflow (`prior`), else unknown. |
| A push to a branch that is not the default branch, a pull request to such a branch, a schedule, a manual run | The latest earlier trusted snapshot (`prior`), else unknown. |
| A child workflow from a job's trigger output | The parent workflow. A job cannot set an owner. |
| A retry or a re-run | The original run (`retry`). |

### Trusted refs

The revision alone is not enough: the ref of the CI definition must be
trusted too. The coordinator writes an owner from the definition (source
`ci`) only when the CI definition came from:

- the default branch (ref class `default`; an operator rule can add the
  class to other protected branches);
- a pull request whose base is the default branch (the base SHA, or the
  merge commit of a merged pull request);
- a tag (ref class `tag`);
- the separate CI repository of the project (`default_ci_source_url`).

A push to another branch is pusher-controlled. With no stored policy its CI
is the pushed commit. With a policy, the base is the `before` commit of the
push, which the same pusher wrote. Thus such a push behaves like a pull
request head: it copies the prior trusted snapshot or records unknown.

The workflow row stores `owner_trusted` and `owner_ref_classes` (the ref
classes of the run). A database trigger refuses a `ci` snapshot that is not
trusted. Only a trusted snapshot gives authority (workflow owner
capabilities, owner approvals, quarantine by an owner). The "latest owner"
of a workflow (`owners.ForWorkflow`, used where no run is in view) is the
newest trusted `ci` snapshot; copies (`prior`, `parent`, `retry`) do not
count as the latest.

### Snapshot

The coordinator stores the owner on each workflow instance and each job when
it creates the row (`owner_group`, `owner_subjects`, `owner_contact`, and on
the workflow `owner_source` and `owner_ci_sha`). The snapshot does not change
after creation, so attribution stays correct after a team changes. A database
trigger copies it from the workflow to its jobs and from a parent workflow to
a child workflow, and keeps it on every update.

`NULL` means unknown: rows from before migration 000053, and runs with no
trusted definition. An empty group with no subjects means that the trusted
definition names no owner.

The snapshot feeds:

- the "why" section of the workflow page, the job page, and the event page;
- the workflow entry of the pull-request report (`Owner ... · contact ...`);
- the `owner_group` OIDC claim (docs/oidc.md);
- the `owner_group` field of outbound events;
- the priority shift of the owner group (docs/priority.md);
- the `workflow_owner` approver subject and workflow roles
  (docs/organizations-and-ci-policy.md).

### Upgrade order

runnerlib ignores top-level workflow keys that it does not know. An older
runnerlib reads a file with `owners:` and `contact:` without an error and
sends no owner. An older coordinator ignores the `owners` and `contact`
fields of a workflow batch. To use owners:

1. Deploy the coordinator with migration 000053.
2. Deploy the runner image with the new runnerlib.
3. Add `owners:` and `contact:` to the workflow files.

A repository can add the keys before step 2. The owner is then unknown until
the new runnerlib runs the eval.

## Decision Events

Every meaningful workflow decision is queryable through `workflow_events`.

| Event | Purpose |
|---|---|
| `workflow_evaluated` | Workflow was created/evaluated |
| `node_waiting` | Node is blocked by dependencies |
| `node_skipped` | Node condition evaluated false |
| `node_submitted` | Node became a job and was submitted |
| `node_running` | Backing job was picked up by a worker |
| `node_completed` | Backing job reached a terminal state |
| `node_cancelled` | A user or an admin cancelled or killed the node |
| `node_retried` | A user retried the node |
| `workflow_var_set` | Workflow var was accepted or deduplicated |
| `workflow_var_conflict` | Workflow var merge failed |
| `workflow_status_changed` | Workflow status changed |

These events are the source of truth for "why did this run?" and "why did this not run?" UI views.

## Data Model

Workflow state uses dedicated workflow tables plus workflow foreign keys on `jobs`.

- `workflow_instances`: workflow identity, parent job, queue, VCS repo/PR/commit, aggregate status, comment marker, last error.
- `workflow_nodes`: node name/display name, dependencies, condition, job spec, job id, item data, status, decision reason, completion time, last successful duration.
- `workflow_vars`: key, JSON value, value hash, source node/job.
- `workflow_events`: workflow/node/job, event type, reason, details JSON.
- `jobs`: `workflow_id`, `workflow_node_id`, `workflow_run_id`, and `workflow_node_name` link concrete job runs back to workflow nodes.
- `workflow_instances` and `jobs`: the owner snapshot (see [Owners and Contact](#owners-and-contact)).

## VCS Status

Individual workflow jobs do not publish their own commit statuses. Each event
publishes one aggregate context:

```text
Reactorcide Checks
```

It includes the eval job, every workflow of the event, and policy refusals. A
workflow can publish one more context of its own with `status: {context:
<name>}`. See [Reactorcide Checks Status](./events-and-schedules.md#reactorcide-checks-status).

Workflows that have no event (created before events existed) keep the old
behavior: one context named after the workflow. For these, `cancelled` maps to
failure.

A workflow that does not match the event gets a `skipped` decision with
reasons. It does not get a workflow row.

## Shared Pull-Request Report

Workflow and policy updates write structured entries to the database. The VCS
report reconciler merges all current entries into one pull-request comment:

```text
<!-- reactorcide:report:v1 -->
## Reactorcide Report

| Job | Status | Duration | Reason |
|-----|--------|----------|--------|
| plan | ✅ succeeded | 4.000s | job finished with status completed |
| test[0] | 🟡 running | est 45.000s | dependencies satisfied and condition true |
| report-failure | ⏭️ skipped | - | condition any_failed(needs) is false |
```

Each workflow uses its stable workflow ID as the report section key. The policy
decision has its own section. The reconciler uses a database lock, revision
counters, and retries. A VCS write failure does not change the workflow result.

Estimated time comes from the most recent successful matching workflow node
for the same workflow name, node name, and project or repository context. When
no previous duration exists, the comment shows `-`.

## UI Implications

The UI should show workflows first and jobs underneath them:

- Workflow list filtered by repo, PR, commit, or event.
- Workflow detail with node graph/table, current vars, and events.
- If an evaluation job creates one root workflow, do not show a separate loose
  job row. Show the evaluation job as the first step in that workflow. Label it
  as the step that spawns the workflow jobs.
- If an evaluation job creates no workflow or more than one workflow, show the
  evaluation job as a separate loose job. A single workflow cannot represent
  these results.
- Node detail linked to existing job logs.
- Skip/reason panel sourced from node decision reasons and `workflow_events`.
- A closed "Why is this workflow running?" section with the event decision reasons and the owner and contact.
- Var history with source node/job and conflict events.

Existing job pages remain useful, but workflow-triggered jobs should clearly show their parent workflow and node.

## Future Work

- Expression-based `for_each`.
- Named condition predicates such as `success("node")`.
- Item-aware historical duration lookup for expanded `for_each` nodes.

## Stable Workflow Identity and CI Origin

A workflow can have an `id` field. Use a lowercase name with digits and single
hyphens. The `name` field remains a display label. If `id` is absent, runnerlib
derives a compatibility ID from the trusted workflow file path. A policy rule
that permits head CI must use an explicit stable ID.

One workflow uses one CI origin by default. Its jobs cannot mix base and head
definitions unless a coordinator policy rule names trusted base nodes for
that workflow. A `base_nodes` policy entry pins the named nodes to the exact
base CI SHA and a trusted execution profile, while the other nodes run head
CI with the untrusted profile. Head content can order dependencies between
these nodes, but it cannot define, change, or replace a policy-controlled
node. The coordinator records the effective authority on each node, and a
retry replays that recorded authority. See [Organizations and Coordinator CI
Policy](./organizations-and-ci-policy.md).

The runnerlib evaluation result carries the workflow ID, source file, CI
origin, safe workflow triggers, per-node authority overrides, and policy
violations. A policy violation does not remove safe workflow triggers. The
payload has no protocol version field.

A workflow author does not configure node authority in repository YAML.
Reserved authority fields such as `execution_profile` and `ci_origin` are
rejected in workflow job entries and job files. Write the control nodes as
normal nodes, give the workflow a stable `id`, and ask an operator to store
the policy rule that grants the node authority.

All workflows for one pull request write structured entries to one shared VCS
report. The workflow ID is the report section key.
