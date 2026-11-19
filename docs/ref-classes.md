# Ref Classes

A ref class is a label that the coordinator gives to the ref of an event. For
example, a push to the default branch gets `default`, and a pull request from
a fork gets `pr` and `fork`. An operator can add more classes with rules, for
example `production`, `release`, or `test`.

Ref classes are coordinator metadata (owner decision D3). Any policy can read
them. They are not part of one feature.

## Trust

The coordinator computes the classes from trusted facts only:

- The signed VCS event: the event type, the ref, the pull request base, the
  fork relation, and the default branch of the repository.
- The schedule record, for a schedule fire.
- The authenticated submit, for a manual submit.
- The rules that the coordinator stores.

Workflow code cannot set or change a class. A branch can change its own
workflow code, so the classes do not come from it. Repository YAML and trigger
output cannot set `REACTORCIDE_REF_CLASSES`.

The head branch of a pull request does not match a rule. The author of a pull
request names the head branch, so a fork can use the name `main`. A rule
matches the base branch of a pull request with `base_branch_glob`.

## Start Easy

The built-in rules are permissive (owner decision D17). No consumer restricts
by class until an operator configures it. An existing deployment does not
change when it gets ref classes.

## Built-in Rules

The built-in rules always apply.

| Rule | Class | When |
| --- | --- | --- |
| `builtin:default-branch` | `default` | A push to the default branch of the repository |
| `builtin:tag` | `tag` | A tag event. Never a manual event. |
| `builtin:pull-request` | `pr` | A pull request event (opened, updated, merged, closed) |
| `builtin:fork` | `fork` | A pull request from a fork |
| `builtin:schedule` | `schedule` | A schedule fire |
| `builtin:manual` | `manual` | A manual event: an eval submit through the API, or a manual run on a ref |

Notes:

- The default branch comes from the signed webhook. A schedule fire does not
  know the default branch, so it does not get `default`. Add a rule for the
  schedule branch if you need a class for it.
- A re-evaluation after an approval and a rerun of a pull request head get the
  classes of the pull request.

## Operator Rules

A rule belongs to an organization, or to one project. A rule has match facts
and a list of classes. A rule adds its classes when all of its match facts are
true. A match fact that you do not set is not a condition.

| Field | Matches |
| --- | --- |
| `event_types` | The event type: `push`, `tag_created`, `pull_request_opened`, `pull_request_updated`, `pull_request_merged`, `pull_request_closed`, `schedule`, or `manual` |
| `branch_glob` | The branch of a push, a schedule, or a manual submit. Never a pull request head. |
| `tag_glob` | The tag name |
| `base_branch_glob` | The base branch of a pull request |
| `is_pr` | `true`: only pull requests. `false`: never pull requests. |
| `is_fork` | `true`: only pull requests from a fork. `false`: never a fork pull request. |
| `is_default_branch` | `true`: a push to the default branch, or a pull request to it |
| `classes` | The classes that the rule adds. At least one. |
| `position` | The order of the rule in its scope. It changes only the order of the reasons. |
| `enabled` | A disabled rule does not apply. |

In a glob, `*` and `?` stay in one `/` segment, and `**` crosses segments.
For example, `release/*` matches `release/1.2`, and `v*` matches `v1.2.3`.

A class has lowercase letters, digits, `.`, `_`, and `-`. It starts with a
letter or a digit and has at most 63 characters. A rule name has lowercase
letters, digits, and `-`.

### Rules only add classes

The rules are additive. The built-in rules always apply. Each enabled rule
that matches adds its classes. No rule removes a class. Thus a project rule
can add classes, but it cannot remove a class that a built-in rule or an
organization rule gives. The coordinator evaluates the organization rules
first, then the project rules.

### Manual submits

A manual submit (an API submit with `REACTORCIDE_JOB_KIND=eval`, or a job with
no event and no parent) always has the event type `manual`. The submitter
states the branch, and no signed webhook confirms it. Thus:

- A manual submit gets the class `manual`.
- A rule that names event types applies to a manual submit only when it names
  `manual`.
- A rule with no `event_types` applies to a manual submit too, but only when
  it has no ref condition.
- A ref condition (`branch_glob`, `tag_glob`, `base_branch_glob`,
  `is_default_branch`) applies to a manual event only in a rule that names
  the event type `manual`. Thus a rule "branch `main` gives `production`"
  with no event types never gives `production` to a manual submit or a
  manual run on `main`.
- A manual event never gets `default` or `tag`.

A manual run (`POST /api/v1/projects/{id}/runs`) is a manual event too. The
coordinator resolves its ref from the VCS, but a principal chooses the ref,
so the same rules apply. A manual run of a pull request keeps the `pr`
class, and the `fork` class for a fork pull request, because the
coordinator reads those facts from the VCS.

Give every rule that adds a protected class (for example `production`) the
event types that it needs, for example `--event push`.

### Classes do not change

The coordinator sets the classes of an event, a workflow, and a job when it
stores the row. After that, an update never changes them: the database
triggers keep the stored classes (and the facts and reasons of an event),
also when an update writes NULL, `{}`, or other classes. A retry or a re-run
copies the classes of the original run.

### Who can change rules

| Scope | Who |
| --- | --- |
| Organization | An org admin, or a token with `policies:manage` for the organization (the admin token) |
| Project | A project owner (an org admin is an owner of each project), or a token with `policies:manage` |

A principal with a role in the organization can read the rules and use
explain. Each change writes an audit record (`ref_class_rule.set`,
`ref_class_rule.delete`).

## CLI

The CLI uses `REACTORCIDE_API_URL` and `REACTORCIDE_API_TOKEN`, or the
`--api-url` and `--token` flags. It works with the admin token and no login.

```bash
# An organization rule: a push to the default branch is production.
reactorcide ref-classes rules set --org example production \
  --class production --event push --default-branch true

# A project rule: release branches and semantic version tags are releases.
reactorcide ref-classes rules set --project PROJECT_ID release-branches \
  --class release --event push --branch 'release/*'
reactorcide ref-classes rules set --project PROJECT_ID release-tags \
  --class release --event tag_created --tag 'v*'

reactorcide ref-classes rules list --org example
reactorcide ref-classes rules list --project PROJECT_ID
reactorcide ref-classes rules delete --project PROJECT_ID release-tags

# Explain the classes of a stored event, or of facts that you give.
reactorcide ref-classes explain --project PROJECT_ID --event-id EVENT_ID
reactorcide ref-classes explain --org example --event push --branch main --default-branch main
reactorcide ref-classes explain --org example --event pull_request_opened \
  --head-branch feature --base-branch main --fork
```

`--project` takes a project ID or a project name. `explain` is also available
as `reactorcide ref-classes rules explain`.

## REST API

| Method and path | Action |
| --- | --- |
| `GET /api/v1/organizations/ORG/ref-class-rules` | List the organization rules and the built-in rules |
| `GET`, `PUT`, `DELETE /api/v1/organizations/ORG/ref-class-rules/NAME` | Read, create or replace, or delete an organization rule |
| `POST /api/v1/organizations/ORG/ref-classes/explain` | Explain facts or a stored event |
| `GET /api/v1/projects/PROJECT/ref-class-rules` | List the project rules, the organization rules (`inherited`), and the built-in rules |
| `GET`, `PUT`, `DELETE /api/v1/projects/PROJECT/ref-class-rules/NAME` | Read, create or replace, or delete a project rule |
| `POST /api/v1/projects/PROJECT/ref-classes/explain` | Explain facts or a stored event of the project |

The body of `PUT` has the rule fields above. `enabled` is `true` when you
omit it. The body of `explain` is `{"event_id": "..."}` or
`{"facts": {"source": "webhook", "event_type": "push", "branch": "main",
"default_branch": "main"}}`.

The job, workflow, and event responses have `ref_classes`.

## UI

- The event page shows the classes, the facts, and the rule that gave each
  class.
- The workflow page shows the classes in "Why is this workflow running?".
- The job page shows the classes.
- The CI trust pages of the organization and of the project have a "Ref
  classes" section. It shows the built-in rules, the organization rules, and
  the rules of the scope, and it has an explain form. An editor can add,
  replace, and delete rules.

The UI uses the CSIL operations `list-ref-class-rules`, `put-ref-class-rule`,
`delete-ref-class-rule`, and `explain-ref-classes`.

## Where Classes Go

- The event stores its classes, the facts, and a reason for each class
  (`ref_classes`, `ref_class_facts`, `ref_class_reasons` on `ci_events`).
- The eval job, each workflow, and each job store the classes of the event.
  A database trigger copies the classes from the parent job, the workflow, or
  the event when the code that makes a row does not set them. Thus child jobs,
  dynamic jobs, retries, and reruns keep the classes of the original run.
- Each decision record of an event has a reason
  `{kind: "ref_class", classes, rule_ids}`. `rule_ids` lists the operator
  rules that added classes.
- Each job gets `REACTORCIDE_REF_CLASSES`: the classes as a comma-separated
  list, for example `fork,pr`. The value is empty when the job has no
  classes. Use it to select caches or test instances without trust logic in
  job code. Do not use it as an authority in job code; use a coordinator
  consumer for that.

## Consumers

Each consumer is optional and off by default.

### Secret grants

A grant can require or deny classes:

```bash
reactorcide secret-grants --project github.com/example/repo \
  set deploy-production \
  --secret-path deploy/production \
  --ref-class-any production \
  --ref-class-none fork
```

`--project` is an option of `secret-grants`. Put it before the subcommand.
See [Secrets](./secrets.md#ref-class-conditions).

### CI policy

A `head_ci` rule can have a `ref_classes` condition, and `defaults` can select
the default profile by class. See
[Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md#ref-classes-in-policy).

### Worker classes

A worker class can require classes with `ref_classes_any`. A workflow node
that selects the worker class runs only when the classes of its workflow have
one of the listed classes. Otherwise the node is not admitted. See
[Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md#worker-classes).

### Egress proxy

The coordinator sends the classes of a job to the egress proxy in the job
identity (`ref_classes`). The flow records show them. A secret grant with
`delivery: proxy` gets to the proxy only when its class conditions match
the job, as a grant with `delivery: env` does. See
[Egress Proxy](./egress-proxy.md).

### Later consumers

OIDC claims, cache namespaces, outbound events, lanes, and required-set
approver rules read the classes through the Go package
`internal/refclass`: `refclass.ForJob(job)`, `refclass.ForWorkflow(workflow)`,
`refclass.ForEvent(event)`, and `refclass.Match{Any, None}.Satisfied(classes)`.

## Database

Migration `000049_ref_classes.sql`:

- Adds `ref_class_rules`.
- Adds `ref_classes` to `ci_events`, `workflow_instances`, and `jobs`, and
  `ref_class_facts` and `ref_class_reasons` to `ci_events`.
- Adds `ref_classes_any` and `ref_classes_none` to `secret_grants`, and
  `ref_classes_any` to `worker_classes`.
- Backfills existing events with the built-in classes that their stored facts
  give: `tag`, `pr`, `fork` (from the eval job), `schedule`, and `manual`.
  The default branch of an old push was not stored, so an old push gets no
  `default` class. Workflows and jobs get the classes of their event.
- Adds the triggers that copy classes to new workflows and jobs, and that keep
  the stored classes on every update (classes do not change after insert).
- A manual event of the backfill gets no `tag` class.

The `-- +goose Down` section removes all of it.
