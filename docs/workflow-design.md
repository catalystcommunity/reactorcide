# Workflow Design

This document describes the v1 workflow model. It builds on the existing `triggers.json` mechanism: jobs can still emit follow-up work, but the coordinator owns dependency evaluation, state merging, and external status reporting.

## Goals

- Run independent jobs in parallel.
- Wait for dependencies without occupying worker slots.
- Make every run, wait, and skip decision explainable in the data model.
- Allow jobs to publish workflow state for later jobs.
- Support dynamic fan-out with literal `for_each` lists.
- Expose one stable VCS status/check per workflow, not one per job.
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

The normal trigger fields still work: `job_file`, `job_name`, source fields, `container_image`, `job_command`, `code_dir`, `job_dir`, `working_dir`, `run_as_user`, `priority`, `timeout`, `capabilities`, and `env`.

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
the execution model. Each workflow gets its own name and status check.

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

## VCS Status

Individual workflow jobs do not publish their own commit statuses. V1 publishes one aggregate context named after the workflow, defaulting to:

```text
Reactorcide Jobs
```

Status mapping:

| Workflow state | VCS state |
|---|---|
| evaluating, running | pending |
| success, skipped | success |
| failed | failure |

If a workflow evaluates and does not need to run, it should become `skipped` with a success status and an event log explaining why.

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
- Skip/reason panel sourced from `workflow_events`.
- Var history with source node/job and conflict events.

Existing job pages remain useful, but workflow-triggered jobs should clearly show their parent workflow and node.

## Future Work

- UI workflow views backed by `workflow_events`.
- Static workflow definition files if they prove useful.
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
