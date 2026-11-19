# Priority and Lanes

Priority is the only capacity mechanism of Reactorcide (owner decision D10).
Reactorcide has no capacity limits per tenant and no fair share. Corndogs
gives a worker the next task of its queues by priority (higher first), then by
age (older first). Resource-aware admission reads the queue heads in the same
order (see [Worker Operation](./workers.md#resource-aware-admission)).

The coordinator computes the priority of each job from two parts:

- A **lane**. A lane is a coordinator rule on trusted facts of the job. The
  first lane that matches gives the **base** priority.
- A **group shift**. A group of the actor can move the priority up or down by
  a bounded value.

## The facts that a lane reads

The coordinator takes each fact from its own records. Workflow code cannot set
or change a fact.

| Fact | Source |
| --- | --- |
| Ref classes | The classes of the job, else of its workflow, its event, or its parent job ([Ref Classes](./ref-classes.md)) |
| Event type | The CI event of the job. A manual event always has the type `manual`. |
| Event source | `webhook`, `schedule`, `manual`, `retry`, `approval`, or `rerun` |
| Draft | `true` for the work of a draft pull request (the signed webhook) |
| Job kind | `eval` for the eval job of an event, else `work` |
| Retry | `true` for a retry of a job |

A job without a CI event (an API submit that is not an eval) gets the ref
class `manual`. A store without CI events uses the pull request number of the
signed webhook, and gives the class `pr`.

## Built-in lanes

The coordinator evaluates the lanes in the order of their position. The
first lane that matches wins.

| Position | Lane | Matches | Base |
| --- | --- | --- | --- |
| 10 | `eval-pr-draft` | Eval job, class `pr`, draft | 5 |
| 20 | `eval-pr` | Eval job, class `pr` | 10 |
| 30 | `eval` | Every other eval job | 5 |
| 50 | `backfill` | Class `backfill` | -400 |
| 100 | `release` | Class `release`, `production`, or `tag` | 100 |
| 200 | `default-branch` | Class `default` (a push to the default branch) | 0 |
| 300 | `manual` | Class `manual` | 0 |
| 400 | `pr-ready` | Class `pr`, not a draft | -100 |
| 500 | `pr-draft` | Class `pr`, draft | -200 |
| 600 | `schedule` | Class `schedule` | -300 |
| 700 | `branch` | Event type `push` (a branch that is not the default branch) | -100 |
| 1000 | `standard` | Every job | 0 |

The default order is: release and production > default branch and manual >
pull request ready for review and other branches > pull request draft >
schedules > backfill. The eval jobs stay ahead of all ordinary work except
release work, so an event gets its decision quickly.

No built-in rule gives the class `backfill`. To use the `backfill` lane, add a
ref class rule that gives the class, for example to the manual runs of a
backfill service principal.

### Change from the priorities before lanes

Before lanes, eval jobs had priority 10 (pull request) or 5 (other). Every
other job had priority 0, or the priority of its job specification.

- The eval priorities do not change, except the eval of a draft pull request.
  It now has 5, not 10.
- Work of a push to the default branch, and manual work, keep 0.
- Release work (class `release`, `production`, or `tag`) now has 100. It runs
  before the evals.
- Work of a pull request now has -100 (ready) or -200 (draft), and work of a
  push to another branch has -100. It waits for the work of the default
  branch.
- Work of a schedule now has -300.
- The priority of a job specification (`job.priority`) and of an API submit
  is now a **request** inside the lane. See [Requested priority](#requested-priority).

To keep the old order, replace the built-in lanes with base 0. For example:
`reactorcide lanes set --org ORG pr-ready --base 0`.

## Group shifts

A group of the organization has a priority shift from -50 to 50. 0 is no
shift. The default is 0.

The shift applies to the work that the actor starts:

- The actor is the actor of the CI event: the sender of the webhook, the
  principal of a manual run, or the system principal for a schedule (no
  groups, no shift).
- A job without an event uses the actor of the job (the submitter), else the
  actor of its parent job.
- The owner group of the workflow (plan item B2) also gives a shift, from the
  trusted owner snapshot of the workflow.

### Combine rule

```text
final = clamp(base + group + requested + shed, -1000, 1000)
```

- `group` is the **largest** shift of the groups that have a shift. The groups
  of the actor and the owner groups count together. A group with shift 0 has
  no shift. When all shifts are negative, the shift nearest to 0 applies
  (for example -5, not -20).
- `requested` is the requested priority, clamped to -50..50.
- `shed` is the shed shift (see [Shed levels](#shed-levels)).

The decision record shows each shift. A group shift that is not the largest
has `applied: false`.

## Requested priority

A job specification can set `job.priority`, and an API submit can set
`priority`. The coordinator does not use this value as the priority. It adds
the value, clamped to -50..50, to the base of the lane. The built-in lanes of
work jobs are 100 apart, so a request cannot move a job into another lane.
The coordinator ignores a request for an eval job.

## Retries and re-runs

- A **retry** of a job (`reactorcide jobs retry`, the UI, a retry with shifts)
  keeps the lane, the shifts, and the priority of the original job. The groups
  of the person who retries do not apply. Only a lane with `retry: true` can
  change the lane of a retry. Then the retry gets the base of that lane and
  keeps the group shifts and the request of the original job.
- A retry of a job that was made before lanes keeps the stored priority of the
  original job.
- An automatic infrastructure retry and a lost-job recovery put the same job
  row back in the queue. The job keeps its priority.
- A **workflow retry** and a **workflow re-run** compute the priority of each
  job again, with the current lanes. The actor stays the actor of the event.
  Thus the groups of the person who re-runs do not apply.
- An event from an approval, from a `/reactorcide rerun` command, or from a
  retry starts work again that another actor started. The actor of such an
  event gives no group shift.

## Shed levels

When the shed level in force has `lower_pr_priority` (the degraded and the
critical levels by default), the coordinator adds -100 to all pull request
work: evals and work jobs with the class `pr`. This moves pull request work
one lane down. For example, a pull request eval goes from 10 to -90, below the
work of the default branch. See [Events, Checks, and Schedules](./events-and-schedules.md#shed-levels).

The Go function `priority.ShedAdjustment` gives this shift. Every submit path
applies it through `priority.Assign`.

## Operator lanes

An organization or a project can add lanes and replace lanes. A lane has:

| Field | Meaning |
| --- | --- |
| `name` | Lowercase letters, digits, and `-`. The name of a built-in lane replaces that lane. |
| `base` | The base priority, -500 to 500 |
| `position` | The evaluation order. Lower is first. |
| `match.classes_any` | The job has one of these ref classes |
| `match.classes_none` | The job has none of these ref classes |
| `match.event_types` | The event type is one of these |
| `match.draft` | `true`: only draft work. `false`: never draft work. |
| `match.job_kind` | `eval` or `work` |
| `match.retry` | `true`: a lane for retries only |
| `enabled` | A disabled lane does not match. A disabled lane with the name of a built-in lane turns that lane off. |

A match fact that you do not set is not a condition. An empty match matches
every job.

Scopes:

- An organization lane replaces the built-in lane with the same name.
- A project lane replaces the organization lane or the built-in lane with the
  same name, for that project only.
- When you set a lane with the name of a built-in lane (or, for a project, of
  an organization lane) and you do not give a match, the lane keeps the match
  and the position of the lane that it replaces. Only the base changes.
- A new lane needs a match.
- Delete a lane to make the replaced lane apply again.

### Who can change lanes and shifts

| Change | Who |
| --- | --- |
| Organization lane | An org admin, or a token with `policies:manage` for the organization (the admin token) |
| Project lane | A project owner, or a token with `policies:manage` |
| Group shift | An org admin, or a token with `organizations:manage` |

A principal with a role in the organization can read the lanes and use
explain. Each change writes an audit record (`priority_lane.set`,
`priority_lane.delete`, `group.priority_shift`).

## CLI

The CLI works with the admin token and no login.

```bash
# The effective lanes in evaluation order.
reactorcide lanes list --org example
reactorcide lanes list --project PROJECT_ID

# Change only the base of a built-in lane.
reactorcide lanes set --org example pr-ready --base -50

# A new project lane for hotfix work.
reactorcide lanes set --project PROJECT_ID hotfix --base 200 --position 5 \
  --class hotfix --kind work

# Turn the schedule lane off for a project (schedules use "standard").
reactorcide lanes set --project PROJECT_ID schedule --base -300 --disabled

# Make the built-in lane apply again.
reactorcide lanes delete --org example pr-ready

# Explain a stored job, or facts.
reactorcide lanes explain --org example --job-id JOB_ID
reactorcide lanes explain --org example --class pr --draft --kind work --requested 10

# Group shifts.
reactorcide groups set-priority-shift --org example release-team 20
reactorcide groups list --org example
```

## REST API

| Method and path | Action |
| --- | --- |
| `GET /api/v1/organizations/ORG/priority-lanes` | List the organization lanes, the built-in lanes, the effective lanes, and the bounds |
| `GET`, `PUT`, `DELETE /api/v1/organizations/ORG/priority-lanes/NAME` | Read, create or replace, or delete an organization lane |
| `POST /api/v1/organizations/ORG/priority/explain` | Explain facts or a stored job |
| `GET /api/v1/projects/PROJECT/priority-lanes` | List the project lanes, the organization lanes (`inherited`), and the effective lanes |
| `GET`, `PUT`, `DELETE /api/v1/projects/PROJECT/priority-lanes/NAME` | Read, create or replace, or delete a project lane |
| `POST /api/v1/projects/PROJECT/priority/explain` | Explain facts or a stored job of the project |
| `PUT /api/v1/organizations/ORG/groups/GROUP/priority-shift` | Set the shift of a group: `{"shift": 20}` |

The body of `PUT .../priority-lanes/NAME`:

```json
{"base": -50, "position": 400, "description": "Ready pull requests",
 "match": {"classes_any": ["pr"], "draft": false}, "enabled": true}
```

`base` is required. Omit `match` and `position` to keep them from the
replaced lane.

The body of explain is `{"job_id": "..."}`, or facts: `ref_classes`,
`event_type`, `event_source`, `draft`, `job_kind`, `retry`, `actor` (a
principal UUID), `requested`.

The job response has `priority`, `priority_lane`, and `priority_reason`.

## Decision record

Each job stores its lane (`jobs.priority_lane`) and a structured reason
(`jobs.priority_reason`):

```json
{"kind": "priority", "lane": "pr-ready", "rule_id": "builtin:pr-ready",
 "value": "builtin", "base": -100, "final": -85,
 "shifts": [{"source": "group", "group": "release-team", "value": 10, "applied": true},
            {"source": "requested", "value": 5, "applied": true}],
 "detail": "priority -85: lane pr-ready (builtin) base -100, group release-team +10, requested +5"}
```

`value` is the scope of the lane. `rule_id` is `builtin:NAME` or the UUID of
the stored lane. A shift source is `group`, `owner_group`, `requested`, or
`shed`. A workflow node adds the same reason to its decision reasons when it
submits its job. A job made before lanes has no reason.

## UI

- The CI trust page of the organization has a "Priority lanes" card. It shows
  the effective lanes in evaluation order. An org admin can add, replace, and
  reset lanes.
- The "Execution" page of a project has the same card for the project lanes.
- The Groups page has a "Priority shift" field for each group.
- The job page shows the priority and how the coordinator computed it.
- The workflow page shows the priority reason of each submitted node.

The UI uses the CSIL operations `list-priority-lanes`, `put-priority-lane`,
`delete-priority-lane`, `explain-priority`, and `set-group-priority-shift`.

## Database

Migration `000064_lanes_and_priority.sql`:

- Adds `priority_lanes`.
- Adds `groups.priority_shift` (default 0, -50..50).
- Adds `jobs.priority_lane` (default `''`) and `jobs.priority_reason`.

Existing jobs keep their stored priority, and Corndogs keeps their tasks. The
`-- +goose Down` section removes all of it.
