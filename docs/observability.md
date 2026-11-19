# Observability: Metrics, Rollups, Dashboards, Alerts, and SLOs

Reactorcide records data about its runs in two stores. Each store has a
different use:

- **Prometheus metrics** for alerts. The labels have low cardinality.
- **PostgreSQL rollups** for dashboards and long history. A rollup row has
  the dimensions that Prometheus must not have (project, workflow).

The pipeline owner and the CI owner use different parts:

| Owner | Owns | Gets |
| --- | --- | --- |
| Pipeline owner | Pass rate and duration of a workflow. The failure classes `job`, `timeout`, and `oom`. | The Pipelines dashboard. Alert rules in Reactorcide. |
| CI owner (infrastructure) | The `infra.*` failure classes, queue wait, eval latency, availability. | The Infrastructure dashboard, SLOs, and Prometheus alert rules. |

An infrastructure failure never pages a pipeline owner. A pipeline failure
is not an infrastructure SLO error. Cancels count for no owner. See
[Failure classes](../DESIGN.md#failure-classes).

CI sentiment is out of scope (owner decision D13).

## Prometheus Metrics

The rollup loop (see below) observes each finished job and each CI event one
time, after the loop watermark passes it. Only the replica that holds the
lease `observability-rollups` exports these metrics. Use `sum()` across
replicas. The delay is the loop interval plus 2 minutes.

| Metric | Type | Labels |
| --- | --- | --- |
| `reactorcide_job_duration_seconds` | histogram | `queue`, `lane`, `failure_class`, `owner_group`, `principal_kind` |
| `reactorcide_job_queue_wait_seconds` | histogram | `queue`, `lane`, `owner_group`, `principal_kind` |
| `reactorcide_job_infra_failures_total` | counter | `failure_class`, `queue`, `lane` |
| `reactorcide_eval_latency_seconds` | histogram | `source` |
| `reactorcide_alert_rules_firing` | gauge | `condition` |
| `reactorcide_rollup_last_success_timestamp_seconds` | gauge | `source` (`jobs`, `events`) |

Each coordinator replica also exports this gauge. It reads the database at
each scrape, so all replicas give the same value. Use `max()` across replicas,
not `sum()`. There is no series when no worker needs action.

| Metric | Type | Labels |
| --- | --- | --- |
| `reactorcide_workers_on_deactivated_enrollment_token` | gauge | `pool_id`, `enrollment_token_id` |

It counts the workers that have an active session from an enrollment token
that is now deactivated. See [Workers](./workers.md#enrollment-token-rotation).

Label values:

- `queue`: the queue UUID. `lane`: the priority lane, or `none` for a job
  from before lanes.
- `failure_class`: a failure class, `none` (a success), or `unknown` (a
  failure with no class).
- `owner_group`: the owner group of the workflow, `none` for no owner. The
  first 20 owner groups that a replica sees get their own value. Later groups
  get `other`. Set the limit with
  `REACTORCIDE_METRICS_OWNER_GROUP_LABEL_LIMIT`.
- `principal_kind`: `human`, `service`, or `system`.
- `source`: the source of the CI event (for example `webhook`).

No metric has a workflow, project, or job label. A large repository can have
thousands of workflows.

Other metrics that the CI owner uses:

| Metric | Source |
| --- | --- |
| `reactorcide_jobs_finished_total{status, failure_class, queue}` | Each replica, at the time of the status change |
| `reactorcide_job_infra_retries_total{failure_class}` | Each replica, at each automatic retry after an infrastructure failure |
| `reactorcide_queue_backlog_jobs`, `reactorcide_queue_backlog_oldest_age_seconds` | [Backlog](./workers.md#backlog-and-autoscaling). Use `max()`. |
| `reactorcide_outbound_event_outbox_lag_seconds` | [Outbound events](./outbound-events.md#metrics) |
| `up` | The Prometheus scrape of the coordinator |

Measures:

- **Duration** is `completed_at - started_at` of a job that started.
- **Queue wait** is the time from `created_at` to the time that a worker took
  the job (`scheduling_started_at`, else `started_at`).
- **Eval latency** is the time from the receipt of a CI event to the first
  commit status that Reactorcide published for it (`ci_events.first_status_at`,
  set by a trigger at the first publish). Events from before migration 000062
  have no value. An event with no published status is not measured.

## Rollups

Migration `000062_rollups_alerts_slos.sql` adds the rollup tables. The
coordinator writes them; nothing else changes them.

| Table | Rows |
| --- | --- |
| `job_rollups_hourly`, `job_rollups_daily` | One row for each bucket, org, project, workflow security ID, owner group, lane, ref class bucket, and actor kind |
| `job_actor_group_rollups_daily` | One row for each day, org, and group of the actor at the time of the run |
| `eval_rollups_hourly`, `eval_rollups_daily` | One row for each bucket, org, project, and event source |
| `control_plane_heartbeats` | One row for each hour, with one bit for each minute in which the coordinator was up |
| `observability_state` | The watermarks of the loop |

A job rollup row has:

- `runs`, `successes`, and failures by class: `failed_job`, `failed_timeout`,
  `failed_oom`, `failed_image_pull`, `failed_scheduling`,
  `failed_worker_lost`, `failed_platform`, `failed_unknown`, `cancelled`.
- `duration_p50_seconds`, `duration_p95_seconds`, and a duration histogram.
  Only pipeline runs (successes, `job`, `timeout`, `oom`) count. An
  infrastructure failure does not change a duration.
- `queue_wait_p50_seconds`, `queue_wait_p95_seconds`, and a queue wait
  histogram, for each run that a worker took.
- `cpu_core_hours` and `memory_gib_hours`: the average use of the run (from
  `job_resource_summaries`) multiplied by the run time.
  `cpu_reserved_core_hours` and `memory_reserved_gib_hours`: the CPU request
  and the memory limit multiplied by the run time. `measured_runs` is the
  count of runs with resource samples.

The percentiles of one row are exact (the same as `percentile_cont`). The
percentiles of a range of rows (a dashboard, an alert window, an SLO) are
estimates from the merged histograms. The histogram bounds are fixed
(`observability.Bounds`, 1 second to 1 day). The estimate interpolates in one
bucket.

The **ref class bucket** of a job is the first of `default`, `tag`, `pr`,
`schedule`, `manual` that the job has, else `other`. "Default branch" in a
dashboard or an alert rule is the bucket `default`.

The **owner group** comes from the owner snapshot on each job (workflow
owners). An empty owner group is "no owner" or "unknown".

### The rollup loop

One coordinator replica runs the loop. It holds the lease
`observability-rollups` in `coordinator_leases`. Each pass:

1. Reads the watermarks `jobs`, `summaries`, and `events`.
2. Finds the days with a change: jobs that finished after the watermark,
   resource summaries that were computed after the watermark, and CI events
   with a first status after the watermark.
3. Computes the hourly and daily rows of each changed day again from the raw
   rows, and replaces them in one transaction. Thus a pass is idempotent.
4. Moves the watermarks to `now - 2 minutes`. A transaction that commits a
   little late is then not missed.

Each hour, a pass also computes the last 48 hours again. A worker can report
an older completion time, and a resource summary can arrive later.

**Backfill.** When a watermark row is missing (the first start after the
migration), the pass reads all existing finished jobs and events, one day at
a time. With 7,500 jobs this is some hundred small queries. A backfill
observes no Prometheus metric. To compute all rollups again, delete the rows
of `observability_state`.

A finished job always has `completed_at`: a trigger sets it for a job that
finishes with no completion time (for example a cancel before a worker took
the job). Old rows with no `completed_at` use `updated_at`.

The loop also sets the heartbeat bit of the current minute every 15 seconds,
evaluates the alert rules every 5 minutes, and applies retention each hour.

### Configuration

| Setting | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_OBSERVABILITY_ENABLED` | `true` | Run the rollup loop |
| `REACTORCIDE_OBSERVABILITY_INTERVAL_SECONDS` | `60` | Time between passes |
| `REACTORCIDE_OBSERVABILITY_ALERT_INTERVAL_SECONDS` | `300` | Time between alert evaluations |
| `REACTORCIDE_ROLLUP_HOURLY_RETENTION_DAYS` | `35` | Keep hourly rows |
| `REACTORCIDE_ROLLUP_DAILY_RETENTION_DAYS` | `400` | Keep daily rows, heartbeats, and alert transitions |
| `REACTORCIDE_METRICS_OWNER_GROUP_LABEL_LIMIT` | `20` | Distinct `owner_group` label values |

## Dashboards

The UI page is **Insights** (`/app/insights`). It has three views. Each view
has the look-back ranges 7, 30, and 90 days. Each chart is an inline SVG with
a title, a legend for two or more series, a tooltip on each point, and a
table view.

| View | For | Shows |
| --- | --- | --- |
| Pipelines (`?view=pipeline`) | Pipeline owners (stories 4, 6) | Runs, pass rate on the default branch, duration p95 trend, the workflows by pass rate (with failure class mix and core-hours for each run), the failure class mix, flaky tests (from test results), and the alert rules |
| Infrastructure (`?view=infrastructure`) | CI owners (stories 7, 9) | The infrastructure failure rate and its classes, queue wait p50 and p95 for each lane, eval latency, the SLOs and error budgets, the backlog, and the worker counts |
| Usage (`?view=usage`) | Directors (stories 10, 11) | CPU core-hours, reserved core-hours, memory GiB-hours, runs, and pass rate for each group, over time |

The Usage view has two attributions:

- **Owner group** (default): the owner group snapshot on each job.
- **Group of the actor**: the groups that the actor of the run was a member
  of when the run started, from the group membership history. A run of an
  actor in two groups counts in both.

Visibility: each view reads only the rollups of the projects that the viewer
can see. A public project is visible with no login. A private project needs
view-private access. The queue list and the actor attribution need access to
every project of the organization. The CSIL operations are
`get-pipeline-dashboard`, `get-infra-dashboard`, and `get-usage-dashboard`.

Queue wait per queue is in Prometheus only (`reactorcide_job_queue_wait_seconds`).
The rollups have the lane, not the queue.

## Alerts

### Pipeline owner alert rules

The Insights page has an alert rules editor (CSIL `list-alert-rules`,
`create-alert-rule`, `update-alert-rule`, `delete-alert-rule`). A project
owner manages the rules of the project. An org admin manages the
`owner_group` rules with no project. The scope of a rule cannot change.

Reactorcide does not notify anyone. A rule emits `alert.firing` and
`alert.resolved` outbound events (see [Outbound Events](./outbound-events.md)).
The loop evaluates a rule only when an enabled subscription receives one of
these events (its scope, project, workflow, owner group, and event types
match the rule). A rule with no receiver keeps its state and does not count
in `reactorcide_alert_rules_firing`. If the subscriptions cannot be read, the
loop evaluates the rule.

An alert rule belongs to an organization. It has a scope and a condition:

| Field | Values |
| --- | --- |
| `scope` | `workflow` (a project and a workflow security ID), `project`, or `owner_group` (with an optional project) |
| `condition` | `pass_rate_below`: the pass rate is less than `threshold` (default 0.9) over the last `window_hours` (default 24, 1 to 720). `duration_regression`: the p95 duration over the window is more than the p95 of the `baseline_days` (default 7, 1 to 90) whole days before the window (daily rollups), multiplied by `factor` (default 1.5, 1 to 100). |
| `ref_class_bucket` | `default` (the default), another bucket, or empty for all |
| `min_runs` | Fewer pipeline runs in the window (or in the baseline) is "no data" (default 5) |
| `fire_after`, `resolve_after` | Evaluations in a row before a rule fires or resolves (default 2 and 2) |
| `resolve_margin` | Hysteresis (default 0.05) |

Only the failure classes `job`, `timeout`, and `oom` count. The pass rate is
`successes / (successes + job + timeout + oom failures)`. A rule with many
infrastructure failures and no pipeline failures does not fire.

Hysteresis: a firing pass-rate rule resolves only when the pass rate is at
least `threshold + resolve_margin`. A firing duration rule resolves only when
the p95 is at most `baseline x factor x (1 - resolve_margin)`. A value
between the limit and the margin keeps the rule firing. With no data, a rule
keeps its state.

Each transition writes a row to `alert_transitions` and an outbound event:
`alert.firing` or `alert.resolved` (see
[Outbound Events](./outbound-events.md#alert-events)). Create a subscription
with scope `workflow`, `project`, `owner_group`, or `org` to receive them. The
payload has the owner group, and for a workflow rule the owner contact and the
owner subjects of the workflow.

Who manages a rule: a project owner (`projects:manage`) for a rule with a
project; an org admin (`organizations:manage`) for an `owner_group` rule with
no project. The admin API token can do all.

```bash
reactorcide alerts create --org acme ci-pass --scope workflow --project app \
  --workflow ci --condition pass_rate_below --threshold 0.9 --window-hours 24
reactorcide alerts create --org acme build-slow --scope project --project app \
  --condition duration_regression --factor 1.5 --baseline-days 7
reactorcide alerts list --org acme
reactorcide subscriptions create --org acme --name ci-alerts --scope workflow \
  --project PROJECT_ID --workflow ci --url https://chat.example.com/hook \
  --event-type alert.firing --event-type alert.resolved
```

### CI owner Prometheus rules

`deployment/prometheus/reactorcide-ci-owner-alerts.yaml` is an example
`PrometheusRule` (Prometheus Operator). It has rules for the infrastructure
failure rate, infrastructure failures by class, queue wait p95 by lane, eval
latency p95, outbox lag, the oldest backlog job, stale rollups, a
coordinator that is down, low and exhausted SLO error budgets, and a worker on
a deactivated enrollment token. Change the thresholds to your SLO targets. For a
plain Prometheus, copy the `groups` list into a rule file.

## SLOs

Infrastructure owns these SLIs. Each one is computed from the daily rollups
over its window (default 30 days, the current day included).

| SLI | Good event | Total | Lane targets |
| --- | --- | --- | --- |
| `eval_latency` | A CI event with its first status within `threshold_seconds` | CI events with a first status | No |
| `queue_wait` | A job that a worker took within `threshold_seconds` | Jobs that a worker took | Yes |
| `infra_failure_rate` | A finished job with no `infra.*` failure class | Finished jobs that were not cancelled | Yes |
| `availability` | A minute with a heartbeat | Minutes since the first heartbeat in the window | No |

Built-in targets (when no target is stored):

| SLI | Objective | Threshold |
| --- | --- | --- |
| `eval_latency` | 95% | 120 s |
| `queue_wait` | 95% | 300 s |
| `infra_failure_rate` | 99% | |
| `availability` | 99.5% | |

A lane with no stored target uses the target of lane `""` of the SLI.

**Error budget remaining** = `1 - bad / ((1 - objective) x total)`. It is
negative when the budget is overspent. States: `ok`, `warning` (less than
25% left), `exhausted` (none left), `no_data`.

**Availability** measures this: in each minute, a coordinator replica held
the rollup lease and wrote a heartbeat to the database. Thus it measures
that the control plane (one coordinator and PostgreSQL) was up. It does not
measure the HTTP path in front of the coordinator, or Corndogs. For those,
use the Prometheus `up` metric and the rule `ReactorcideCoordinatorDown`.
The time before the first heartbeat in the window (for example before an
install) does not count.

```bash
reactorcide slo list --org acme
reactorcide slo set --org acme queue_wait --lane pr --objective 0.9 --threshold-seconds 900
reactorcide slo set --org acme availability --objective 0.999
reactorcide slo status --org acme
reactorcide slo delete --org acme queue_wait --lane pr
```

The Infrastructure dashboard shows the same statuses. Only an org admin
(`organizations:manage`) can set or delete a target.

### SLO metrics

Reactorcide does not alert on SLOs. It exports the SLO analysis as gauges,
and your observability tool alerts on them. Each coordinator replica computes
the gauges from the rollups at most once a minute, so all replicas give the
same value: use `max()` across replicas, not `sum()`. The gauges exist only
when `REACTORCIDE_OBSERVABILITY_ENABLED` is true. Organizations that are not
active have no series.

| Metric | Meaning |
| --- | --- |
| `reactorcide_slo_objective_ratio` | The objective of the target (for example `0.95`) |
| `reactorcide_slo_compliance_ratio` | Good / total over the window. No series when there is no data. |
| `reactorcide_slo_error_budget_remaining_ratio` | Error budget left. `0` or less is exhausted. `1` when there is no data. |
| `reactorcide_slo_window_good`, `reactorcide_slo_window_total` | The good and the total counts of the window (minutes for `availability`) |
| `reactorcide_slo_threshold_seconds` | The threshold of a good event (`eval_latency`, `queue_wait`) |
| `reactorcide_slo_p95_seconds` | The estimated p95 over the window (`eval_latency`, `queue_wait`, only with data) |
| `reactorcide_slo_window_days` | The window length |
| `reactorcide_slo_last_computed_timestamp_seconds` | The last successful computation of the replica (no labels) |

Labels: `org` (the organization name), `sli`, and `lane` (empty for an SLI
with no lanes, or before any job has a lane). A failed computation keeps the
previous values; alert on a stale `reactorcide_slo_last_computed_timestamp_seconds`
to find it. The example rules `ReactorcideSLOErrorBudgetLow` (less than 25%
left) and `ReactorcideSLOErrorBudgetExhausted` are in
`deployment/prometheus/reactorcide-ci-owner-alerts.yaml`.

## REST

All routes are under `/api/v1/organizations/ORG/` and work with the admin
API token. Reads need `projects:read` in the organization.

| Method and path | Action |
| --- | --- |
| `GET observability/rollups?granularity=hour\|day&days=N[&from=&to=&project=&workflow=&owner_group=&lane=&ref_class=]` | Rollup rows, the histogram bounds, and their totals |
| `GET observability/pipeline?days=7\|30\|90[&project=]` | The Pipelines dashboard |
| `GET observability/infrastructure?days=` | The Infrastructure dashboard, with backlog and worker counts |
| `GET observability/usage?days=&attribution=owner\|actor` | The Usage dashboard |
| `GET observability/status` | The watermarks of the rollup loop |
| `GET`, `POST alert-rules` | List, create |
| `GET`, `PATCH`, `DELETE alert-rules/NAME` | Show (with the last transitions), change, delete. The scope of a rule cannot change. |
| `GET slos` | The targets, stored and built-in |
| `GET slos/status` | The SLI statuses and error budgets |
| `PUT`, `DELETE slos/SLI[?lane=]` | Set, delete a target |

Alert rule and SLO target changes write audit records (`alert_rule.create`,
`alert_rule.update`, `alert_rule.delete`, `slo_target.set`,
`slo_target.delete`).

The CLI `reactorcide rollups show` and `reactorcide rollups dashboard`
read these routes.
