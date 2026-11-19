# Outbound Events

Reactorcide sends phase events of runs to outbound sinks: a node starts, a
node finishes, an event is evaluated, a workflow completes. A subscription
selects the events and the sink. The first sink is a webhook. Other sinks
(a message queue or a bus) are a later addition behind the same interface
(owner decisions D7, D22, D27).

A sink can never delay or stop a run. The events are emitted only.

## Event Types

| Type | When | `status` | `result` |
| --- | --- | --- | --- |
| `node.pre_start` | A worker has the job. The container did not start. | `scheduling` or `running` | empty |
| `node.started` | The job container runs. | `running` | empty |
| `node.post_complete` | The job (or a node with no job) has a final status. | final status | `completed`, `failed`, `cancelled`, `timeout` |
| `node.skipped` | The node was skipped (`if`, or a condition). | `skipped` | `skipped` |
| `event.evaluated` | The eval of an event ended. | `evaluated` or `failed` | the same as `status` |
| `event.admitted` | The eval decided to run a workflow. | `run` | `run` |
| `event.not_admitted` | The execution profile refused a workflow. | `not_admitted` | `not_admitted` |
| `workflow.completed` | A workflow has a final status. | final status | `success`, `failed`, `cancelled`, `skipped`, `not_admitted` |
| `alert.firing` | A pipeline owner alert rule fires. | `firing` | `firing` |
| `alert.resolved` | A firing alert rule resolves. | `resolved` | `resolved` |

A job that is outside a workflow (for example the eval job) also sends
`node.*` events. Its payload has no `node` object.

A job that goes to `running` with no `scheduling` status sends
`node.pre_start` and then `node.started`. A job that the coordinator retries
after an infrastructure failure sends its phase events again; `job.attempt`
tells the attempt.

## Payload (Schema Version 1)

Each event is one JSON object:

| Field | Meaning |
| --- | --- |
| `id` | The stable event ID (a ULID in UUID form). A retry and a replay send the same ID. |
| `type` | The event type. |
| `schema_version` | `1`. A change that removes or renames a field increases it. |
| `occurred_at` | The time of the status change (RFC 3339, UTC). |
| `sequence` | The position of the event in its order key. It increases. It can have gaps. |
| `order_key` | `workflow:<id>`, `event:<id>` (an event with no workflow), or `job:<id>` (a job outside a workflow). |
| `org`, `project` | ID and name. `project` is absent for a job with no project. |
| `ci_event` | The CI event ID, when known. |
| `workflow` | ID, name, and security ID. |
| `node` | ID and name. |
| `job` | ID, name, and attempt (1 for the first run). |
| `actor` | The principal that started the run: `principal_id`, `kind` (`human`, `service`, `system`), `display_name`. |
| `repo`, `ref`, `sha` | The repository, the ref, and the commit. |
| `ref_classes` | The ref classes of the run ([Ref Classes](./ref-classes.md)). |
| `owner_group` | The owner group of the workflow, from its trusted owner snapshot ([Owners and Contact](./workflow-design.md#owners-and-contact)). Empty when there is none or it is unknown. |
| `status`, `result`, `failure_class` | See the table above. `failure_class` is a class of [failure classes](../DESIGN.md#failure-classes), or empty. |
| `details` | Facts of one type: `exit_code`, `reason`, `error`, `source`, `event_type`, `pr_number`, `source_file`. |
| `links.ui` | The page of the run in the web UI. |

Examples (IDs are shortened):

```json
{"id": "0193a6f0-…-01", "type": "node.pre_start", "schema_version": 1,
 "occurred_at": "2026-11-16T10:00:00.12Z", "sequence": 3, "order_key": "workflow:0193a6e1-…",
 "org": {"id": "…", "name": "acme"}, "project": {"id": "…", "name": "app"},
 "ci_event": {"id": "0193a6d0-…"},
 "workflow": {"id": "0193a6e1-…", "name": "ci", "security_id": "ci"},
 "node": {"id": "…", "name": "build"}, "job": {"id": "…", "name": "build", "attempt": 1},
 "actor": {"principal_id": "…", "kind": "human", "display_name": "octocat"},
 "repo": "github.com/acme/app", "ref": "refs/heads/main", "sha": "abc123",
 "ref_classes": ["default_branch"], "owner_group": "", "status": "scheduling",
 "result": "", "failure_class": "", "links": {"ui": "https://ci.example.com/app/workflows/0193a6e1-…"}}
```

```json
{"id": "…-02", "type": "node.started", "status": "running", "result": "", "…": "the same fields"}
```

```json
{"id": "…-03", "type": "node.post_complete", "status": "failed", "result": "failed",
 "failure_class": "job", "details": {"exit_code": 2}, "…": "the same fields"}
```

```json
{"id": "…-04", "type": "node.skipped", "status": "skipped", "result": "skipped",
 "node": {"id": "…", "name": "docs"}, "details": {"reason": "skipped by if: no changed file matches paths include [docs/**]"}}
```

```json
{"id": "…-05", "type": "event.evaluated", "order_key": "event:0193a6d0-…",
 "status": "evaluated", "result": "evaluated",
 "details": {"source": "webhook", "event_type": "push"}, "links": {"ui": "https://ci.example.com/app/events/0193a6d0-…"}}
```

```json
{"id": "…-06", "type": "event.admitted", "status": "run", "result": "run",
 "workflow": {"id": "0193a6e1-…", "name": "ci", "security_id": "ci"},
 "details": {"source_file": ".reactorcide/workflows/ci.yaml"}}
```

```json
{"id": "…-07", "type": "event.not_admitted", "status": "not_admitted", "result": "not_admitted",
 "workflow": {"security_id": "deploy", "name": "deploy"}}
```

```json
{"id": "…-08", "type": "workflow.completed", "status": "failed", "result": "failed",
 "details": {"error": "…"}}
```

### Alert Events

The rollup loop writes `alert.firing` and `alert.resolved` to the outbox when
a pipeline owner alert rule changes state ([Observability](./observability.md#alerts)).
No trigger writes them. The order key is `alert:<rule id>`. The payload has
`org`, `project` (for a rule with a project), `workflow.security_id` (for a
workflow rule), `owner_group`, `status`, and `result`. It has no `job`,
`node`, `ci_event`, or `actor`, and `failure_class` is empty: a subscription
with a failure class filter or an actor kind filter does not get alert events.
`details` has:

| Field | Meaning |
| --- | --- |
| `alert` | `rule_id`, `name`, `description`, `condition`, `scope`, `state`, `value`, `limit`, `runs`, `threshold`, `factor`, `window_hours`, `ref_class_bucket`, `counted_failure_classes` (`job`, `timeout`, `oom`), and for `duration_regression` `baseline` and `baseline_days` |
| `owner_contact` | `channel`, `help_url`, `email` of the workflow owner (workflow rules with a known owner) |
| `owner_subjects` | The owner subjects of the workflow |
| `dashboard_url` | The Insights page, when `REACTORCIDE_VCS_BASE_URL` is set |

```json
{"id": "…-09", "type": "alert.firing", "order_key": "alert:0193b0…",
 "workflow": {"security_id": "ci", "name": "ci"}, "owner_group": "payments",
 "status": "firing", "result": "firing", "failure_class": "",
 "details": {"alert": {"name": "ci-pass", "condition": "pass_rate_below", "value": 0.62,
   "limit": 0.9, "runs": 21, "window_hours": 24, "counted_failure_classes": ["job", "timeout", "oom"]},
   "owner_contact": {"channel": "#payments-ci"}}}
```

A test send (`subscriptions test`) has the type `test.ping`. It has `id`,
`type`, `schema_version`, `occurred_at`, `org`, and `subscription`.

## Webhook Requests

The webhook sink sends `POST` with the JSON payload as the body and these
headers:

| Header | Value |
| --- | --- |
| `Content-Type` | `application/json` |
| `X-Reactorcide-Event-Id` | The event ID |
| `X-Reactorcide-Event-Type` | The event type |
| `X-Reactorcide-Schema-Version` | `1` |
| `X-Reactorcide-Timestamp` | Unix seconds of the send |
| `X-Reactorcide-Delivery-Id` | The delivery ID |
| `X-Reactorcide-Signature` | `sha256=` + hex(HMAC-SHA256(secret, timestamp + "." + body)) |

A 2xx answer is a delivery. Every other answer, a timeout (10 seconds by
default), and a connection error are failures. A redirect is not followed;
it is a failure.

### Verify the Signature

Use the raw body as received. Refuse an old timestamp (for example older
than 5 minutes), to stop a replay by a third party. Compare in constant
time. Drop an event ID that you already processed.

```python
import hashlib, hmac, time

def verify(secret: bytes, headers, body: bytes) -> bool:
    timestamp = headers["X-Reactorcide-Timestamp"]
    if abs(time.time() - int(timestamp)) > 300:
        return False
    expected = "sha256=" + hmac.new(secret, timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, headers["X-Reactorcide-Signature"])
```

```go
func verify(secret []byte, timestamp string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hmac.Equal([]byte("sha256="+hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}
```

Test vector: secret `secret`, timestamp `1700000000`, body `{}` gives
`sha256=b8569b78799ff9e3cbff0fc2d63a33a2b57f3282abd07c37ae5e8e7d79a5f163`.

### Destinations

- The URL must use HTTPS. HTTP is permitted only when the coordinator starts
  with `--allow-insecure-transport` (development only). No environment
  variable can permit HTTP.
- The sink refuses loopback, private, link-local (also cloud metadata
  addresses), carrier-grade NAT, multicast, and reserved addresses. It checks
  each resolved address when it connects, so a DNS answer cannot move a
  request to a refused address. It does not use an HTTP proxy from the
  environment.
- An operator can permit networks with
  `REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS` (comma-separated CIDR
  prefixes or addresses).
- The URL must not contain user information. Put a token in the query if the
  receiver needs one; the delivery log and the audit log do not store the
  URL query.
- A refused destination makes the delivery `dead_letter` at once.

## Subscriptions

A subscription has:

| Part | Values |
| --- | --- |
| Name | Lowercase letters, digits, and hyphens. Unique in the organization. |
| Owner principal | The caller, or `--owner`. A human, or a service principal of the organization. When the owner is disabled, nothing is delivered. |
| Scope | `org`; `project` (a project); `workflow` (a workflow security ID in a project); `owner_group` (an owner group, optional project). |
| Filters | Event types, ref classes (any), results, actor kinds, failure classes. An empty filter matches everything. A results filter does not match an event with no result (`node.pre_start`, `node.started`). A failure class filter does not match an event with no failure class. `reactorcide diagnosis setup` makes a subscription with the failure classes `job`, `oom`, and `timeout` ([Failure Diagnosis](./diagnosis.md)). |
| Sink | `webhook` with a URL. |
| Signing secret | Made at random (or read from `--secret-file`). It is encrypted with the coordinator master keys. It shows one time. |
| Max attempts | `0` uses the coordinator default (`REACTORCIDE_OUTBOUND_EVENTS_MAX_ATTEMPTS`, 12). |

Who manages a subscription:

- An org subscription, and an owner_group subscription with no project: an org
  admin (`organizations:manage`).
- A project or workflow subscription: a project owner (`projects:manage`).
- The admin API token can do all, so the feature works with no logins. A
  service principal token gets the same authority from its roles.

Create, change, replay, and delete write audit records
(`subscription.create`, `subscription.update`, `subscription.replay`,
`subscription.delete`). The records hold no secret and no URL query.

```bash
reactorcide subscriptions create --org acme --name diagnosis \
  --scope project --project PROJECT_ID \
  --url https://diag.example.com/reactorcide \
  --event-type node.post_complete --result failed --result timeout
reactorcide subscriptions list --org acme
reactorcide subscriptions test --org acme SUBSCRIPTION_ID
reactorcide subscriptions deliveries --org acme SUBSCRIPTION_ID --status dead_letter
reactorcide subscriptions replay --org acme SUBSCRIPTION_ID --all-dead-letters
```

The UI page is **Event subscriptions** (`/org/subscriptions`, linked from the
Administration page). It lists the subscriptions of an organization, makes a
subscription, sends a test event, and shows the delivery log with replay. A
project has the same page for its subscriptions (`/projects/PROJECT_ID/subscriptions`).
The pages use the CSIL operations `list-event-subscriptions`,
`get-event-subscription`, `create-event-subscription`,
`update-event-subscription`, `delete-event-subscription`,
`list-event-deliveries`, `get-event-delivery`, `replay-event-delivery`, and
`test-event-subscription`.

REST (under `/api/v1/organizations/ORG/subscriptions`):

| Method and path | Action |
| --- | --- |
| `GET /[?project=ID]`, `POST /` | List, create |
| `GET`, `PATCH`, `DELETE /ID` | Show, change (`enabled`, filters, `webhook_url`, `max_attempts`, `rotate_secret`), delete |
| `POST /ID/test` | Send `test.ping` now |
| `GET /ID/deliveries[?status=&limit=]` | The delivery log, newest first |
| `GET /ID/deliveries/DELIVERY` | One delivery with its attempts |
| `POST /ID/deliveries/DELIVERY/replay` | Replay one delivery |
| `POST /ID/replay-dead-letters` | Replay every dead-lettered delivery |

## Delivery

1. A database trigger writes the event to the `event_outbox` table in the
   same transaction as the status change. A restart cannot lose an event, and
   a rolled-back change sends nothing. An error in the trigger is logged as a
   warning; the status change continues and that event is lost. The trigger
   writes nothing when the organization has no enabled subscription (one
   index lookup). Thus an organization with no subscription adds no outbox
   rows. An event that occurs before you create (or enable) a subscription
   is not recorded and is never delivered.
2. The dispatcher runs on one coordinator replica (lease `outbound-events` in
   `coordinator_leases`). It assigns the sequence, stores the payload, and
   makes one delivery for each matching subscription. It reads only rows
   that are older than a short lag (`REACTORCIDE_OUTBOUND_EVENTS_LAG_MS`,
   default 2000 ms), in the order `(occurred_at, outbox_id)`. `occurred_at`
   is the start time of the transaction that wrote the row. Thus when two
   transactions change one workflow at the same time, the one that started
   first gets the lower sequence, also when it commits later. A transaction
   that is open for longer than the lag can still come out of order.
3. For each (subscription, order key), only the first open delivery is in
   flight. The dispatcher submits one Corndogs task for it on the internal
   queue `reactorcide.internal.outbound-events`, with a keyed submit
   (`SubmitKeyedTask`), and submits the next one after it completes.
4. A delivery loop on each coordinator replica claims the task and calls the
   sink. Workers never get these tasks: a worker queue is a UUID, and the
   worker queue set drops every internal queue.
5. A failure retries after 10 s, 20 s, 40 s, and so on, at most one hour, with
   20% random spread. After the attempt limit, the delivery is `dead_letter`.

Guarantees:

- **At least once.** A receiver can get an event more than one time. Drop
  duplicates by the event ID.
- **Order per workflow.** The events of one order key go to one subscription
  in sequence order.
- **No order across workflows.**
- **Dead letter.** A dead-lettered delivery does not stop the next events of
  its workflow. A replay sends it again, also after later events.
- **Replay.** A replay of a delivered or dead-lettered delivery sends the same
  event ID and payload again, with a new attempt budget.
- A disabled subscription gets no new events. Its open deliveries go to
  `dead_letter`; replay them after you enable it.

The coordinator keeps the delivery state in its database. A Corndogs task
that is not current (a reset delivery, or a duplicate) completes with no
send. A delivery that stays `queued` or `sending` for 10 minutes goes back to
`pending`. Without Corndogs, events stay in the outbox until a coordinator
with Corndogs runs. A Corndogs server before 0.8.0 has no keyed submit; the
coordinator then uses the legacy operations and logs a warning. Upgrade
Corndogs to 0.8.0.

Dispatched events and their final deliveries are deleted after
`REACTORCIDE_OUTBOUND_EVENTS_RETENTION_DAYS` (14). A replay is possible
only before that. Undispatched events older than the retention are deleted
too. Every coordinator replica runs this cleanup each hour, also when the
dispatcher is off (`REACTORCIDE_OUTBOUND_EVENTS_ENABLED=false`, or no
Corndogs), so the outbox cannot grow without a limit.

## Pull: WebSocket Stream

A consumer can also read the live events of one workflow:
`GET /api/v1/workflows/stream/WORKFLOW_ID` (WebSocket). A service, instance,
or user token needs `workflows:read` for the organization of the workflow. A
service principal token intersects it with its roles. A job token is refused.
The first frame is the current workflow status. The stream is best effort
(PostgreSQL LISTEN/NOTIFY); to get every event, use a subscription.

## Configuration

| Setting | Default | Meaning |
| --- | --- | --- |
| `--allow-insecure-transport` | off | Permit HTTP webhooks (development only) |
| `REACTORCIDE_OUTBOUND_EVENTS_ENABLED` | `true` | Run the dispatcher and the delivery loop |
| `REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS` | empty | Networks that a webhook can reach although they are private |
| `REACTORCIDE_OUTBOUND_EVENTS_MAX_ATTEMPTS` | `12` | Attempts before `dead_letter` |
| `REACTORCIDE_OUTBOUND_EVENTS_TIMEOUT_SECONDS` | `10` | Timeout of one send |
| `REACTORCIDE_OUTBOUND_EVENTS_DELIVERY_WORKERS` | `4` | Concurrent sends on each replica |
| `REACTORCIDE_OUTBOUND_EVENTS_RETENTION_DAYS` | `14` | Retention of dispatched and undispatched events; `0` keeps them |
| `REACTORCIDE_OUTBOUND_EVENTS_LAG_MS` | `2000` | Age of an outbox row before the dispatcher reads it (occurrence order) |
| `REACTORCIDE_VCS_BASE_URL` | | The base of `links.ui` |

## Metrics

| Metric | Labels |
| --- | --- |
| `reactorcide_outbound_event_deliveries_total` | `sink`, `result` (`delivered`, `failed`, `dead_letter`, `refused`, `stale`) |
| `reactorcide_outbound_event_delivery_latency_seconds` | `sink` |
| `reactorcide_outbound_event_outbox_lag_seconds` | none: the age of the oldest undispatched event |
| `reactorcide_outbound_events_dispatched_total` | `outcome` (`matched`, `unmatched`) |

## Database

Migration `000058_event_subscriptions.sql` adds `event_outbox`,
`event_outbox_sequences`, `event_subscriptions`, `event_deliveries`,
`event_delivery_attempts`, and the outbox triggers on `jobs`,
`workflow_nodes`, `workflow_instances`, `ci_events`, and
`ci_event_workflow_decisions`. It changes no existing row. The
`-- +goose Down` section removes all of it.
