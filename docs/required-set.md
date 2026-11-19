# Required Set and Advisory Workflows

The required set of a project tells which workflows block a merge. Only the
workflows in the set count toward the `Reactorcide Checks` commit status.
Other workflows are advisory. An advisory workflow runs, and it reports in
the pull request comment only.

## Summary

| State | Counts toward `Reactorcide Checks` | Own commit status | PR comment |
| --- | --- | --- | --- |
| required | yes | yes, when it has `status: {context: X}` | yes |
| advisory | no | no (also no `status.context`) | yes, marked "Advisory — not required" |
| quarantined | no | no | yes, marked "Quarantined — not required" with the expiry |

- With no configuration, the required set is off. Every workflow blocks, as
  before this feature.
- An operator turns the set on for one project. At that time, every workflow
  that the project knows goes into the set. Thus nothing changes at that
  moment.
- After that, a new workflow is advisory. A promotion adds it to the set. A
  promotion needs an approval.
- A quarantine removes a workflow from the set for a limited time. At the
  expiry, the workflow is required again. No action is necessary.

**Branch protection:** require only `Reactorcide Checks` in the VCS. Do not
require the workflow `status.context` names. An advisory workflow does not
post its context, so a required context of an advisory workflow stays
missing and blocks the merge.

## Why the Set Is Coordinator State

The required set is in the coordinator, not in the workflow YAML. A
`required: true` key in YAML lets a branch promote or demote its own
workflows, with only normal code review as control. The coordinator applies
the same rule to CI policy: admission policy is coordinator state.

## Workflow IDs and Entries

The set holds workflow security IDs: the `id:` of a workflow file, or the
file name when the file has no `id:`. An entry has:

| Field | Meaning |
| --- | --- |
| `workflow_security_id` | The workflow ID. |
| `branch_pattern` | The target branches of the entry. Empty: every branch. A glob (`release/*`, `hotfix/**`) limits the entry. |
| `source` | `seed` (added when the set was turned on) or `promotion`. |
| `added_by`, `added_at`, `reason` | Who added the entry, when, and why. |
| `quarantined_until`, `quarantine_reason`, `quarantined_by` | The quarantine, when there is one. |

The target branch of an event is:

- the base branch of a pull request,
- the branch of a push, a schedule, or a manual run.

A tag has no branch. Only an entry for every branch applies to a tag event.
When the coordinator cannot tell the base branch of a pull request (an old
event), every entry applies, so the workflow blocks.

A workflow is required for an event when one entry with its ID matches the
target branch and that entry is not quarantined.

## Turn the Set On or Off

An org admin turns the set on or off. The admin API token can do it, so a
deployment with no logins can use the feature.

```bash
reactorcide required-set enable --project my-app
reactorcide required-set list --project my-app
reactorcide required-set disable --project my-app
```

`enable` adds every workflow ID from the root workflows and the eval
decisions of the project. The response lists the IDs that it added. Entries
that exist stay. `disable` keeps the entries, so a later `enable` does not
start from zero.

`enable` needs trusted CI definitions. The project must have a stored CI
policy (`reactorcide policy set`), or its organization must use the secure
defaults profile (`reactorcide orgs set-defaults ORG --profile secure`).
Otherwise `enable` fails with `failed_precondition` (HTTP 409). Reason:
without a policy, a pull request of a legacy organization runs its own CI
definition. That pull request can delete or rename a required workflow, and
the set then has nothing to count.

## Promotions and Demotions

A promotion adds a workflow to the set. A demotion (`remove`) takes it out.
Both are proposals. A proposal is `pending` until an approver approves or
rejects it, or the proposer withdraws it.

```bash
reactorcide required-set propose --project my-app lint --reason "stable for two weeks"
reactorcide required-set approve --project my-app <proposal-id>
reactorcide required-set reject --project my-app <proposal-id> --reason "too slow"
reactorcide required-set withdraw --project my-app <proposal-id>
reactorcide required-set remove --project my-app lint --reason "replaced by lint-v2"
```

With `--approve`, an approver proposes and approves in one step. The
coordinator then writes two audit events.

`--branch GLOB` makes an entry for some target branches only.

### Pull request commands

A comment on a pull request can propose a change for every target branch:

```text
/reactorcide promote lint
/reactorcide demote lint
```

The comment goes through the one approval path, with the same rules as
REST and the UI. The VCS facts of the person that the coordinator reads live
for the comment (`vcs_user:`, `vcs_team:`, `repository_write`) also count
toward the approver rules of the entry. Thus:

- An approver of the entry changes the set at once (a proposal and its
  approval, two audit events).
- Another person with a role in the project or its organization, or an
  owner of the workflow, makes a pending proposal.
- Another person is refused.

Separation of duties: an author of the pull request cannot demote a workflow
with a comment on that pull request. The authors are the pull request
author, each person who pushed a head of the pull request, and the VCS
accounts of the commit authors and committers. If the coordinator cannot
list the authors, it refuses the demote. The GitLab client cannot list
commit authors, so on GitLab a demote from a comment is always refused. This rule applies only to pull
request comments. A demote through REST, the CLI, or the UI follows the
approver rules only. A promote from a comment does not have this rule.

Reactorcide replies on the pull request with the result: done, pending, or
refused. A comment that starts with `/reactorcide` and is not a valid
command also gets a short reply with the list of commands. A reply is a
fixed text. It never repeats the comment and never mentions a person. The
replies are rate limited (each pull request: 3 at once, then one a minute;
each project: 20 at once, then 10 a minute). A reply over the limit is not
sent.

## Who Can Do What

| Operation | Who |
| --- | --- |
| Read the set | A role in the project or its organization (member and above). A token with `projects:read`. |
| Propose a promotion or a demotion | The same. A user token or an instance token also needs `policies:manage`. |
| Approve or reject | An approver of the entry (see below). |
| Withdraw | The proposer, or an approver. |
| Quarantine, end a quarantine | An approver of the entry, or a workflow owner (when workflow owners are configured). |
| Turn the set on or off, change approvers | An org admin. |

An org admin is always an approver. An org admin can change the approver
setting, so a refusal of the org admin protects nothing. The admin API token
is an org admin of every organization.

### Approvers

The approvers of an entry come from:

1. The approver rules (`required_set_approver_rules`). The first rule with a
   branch glob that matches the branch pattern of the entry wins. A glob
   matches a pattern when it matches the pattern text as a branch name, or
   is equal to it. An entry for every branch uses only a rule that lists
   `**`.
2. Else the approver list (`required_set_approvers`).
3. Else, when no subject is configured, the org admins only.

The project value replaces the organization value. A project with no value
inherits the organization value.

```bash
# Organization defaults: the CI owner group approves; release branches need
# the release group. The longest quarantine is 14 days.
reactorcide required-set org-settings --org acme \
  --approver reactorcide_group:ci-owners \
  --rule 'release/* hotfix/**=reactorcide_group:release' \
  --max-quarantine-days 14

# Project override, then back to the organization values.
reactorcide required-set approvers --project my-app --approver reactorcide_group:app-owners
reactorcide required-set approvers --project my-app --inherit
```

The subjects use the grammar of [Principals](./principals.md): `*`,
`principal:<id>`, `service:<name>`, `identity:<type>:<value>`,
`vcs_user:<provider>/<login>`, `reactorcide_group:<name>`, and
`project_owner`. The coordinator adds the subjects of all identities that
are linked to the principal of the caller.

`vcs_team:` and `repository_write` need a live VCS lookup. They do not match
for a REST, CLI, or UI caller. Use a Reactorcide group for approvers.

A service principal token must name the organization, and its ISSUED
capabilities must include `policies:manage` to propose, approve, reject,
quarantine, or demote. The coordinator narrows the token of a non-admin
principal to read capabilities, so it checks the issued capabilities, not
the narrowed set. After that check, the roles and subjects of the principal
decide, the same as for an approval in a pull request comment: the token is
an approver when its principal has an approver subject (for example, it is
in the approver group). To view the set, the token needs a read capability
(`projects:read`). A workflow owner that quarantines a workflow with a token
needs `policies:manage` too.

## Quarantine

A quarantine is a time-limited removal from the set. It needs a reason and
an expiry. The expiry must be within the maximum of the organization
(`max_quarantine_days`, default 30, from 1 to 365).

```bash
reactorcide required-set quarantine --project my-app e2e --for 7d --reason "flaky runner, see issue 42"
reactorcide required-set quarantine --project my-app e2e --until 2026-12-01T00:00:00Z --reason "..."
reactorcide required-set unquarantine --project my-app e2e
```

The coordinator computes the state each time it reads the set. Thus a
quarantine ends at its expiry with no action. A quarantined workflow is
advisory until then. The listing, the UI, and the pull request comment show
the expiry.

You can quarantine only an entry of a set that is on. To quarantine an
advisory workflow has no effect, so the coordinator refuses it.

## Effect on `Reactorcide Checks`

When the set is on:

- Only required workflows count. An advisory workflow that fails, is
  cancelled, or is not admitted does not fail the status.
- An advisory workflow that runs does not keep the status pending.
- The description tells how many advisory workflows failed, for example
  `2 workflows passed; 1 advisory failed`.
- When all workflows of an event are advisory, the status is success with
  `No required workflows`.
- A CI change that a stored policy refuses still fails the status. A refused
  CI file does not belong to one workflow, so the coordinator cannot make it
  advisory.
- When the set cannot be read, every workflow is required (fail closed).
- A required workflow that the TRUSTED base definition of a pull request
  matched, but that did not run, fails the status with the text
  `required workflow NAME did not run`. Examples: the head deleted or
  renamed the workflow, or nothing selected it. The eval marks each workflow
  that the base definition matched (decision reason `base_definition`).
  Path acceptance can still skip such a workflow on purpose; that skip is
  not a failure. A carried workflow ran in an earlier event and counts.
- A push has no trusted base definition: its CI is the pushed commit, or
  the `before` commit that the pusher wrote. Thus a push does not get the
  "did not run" check. A merged pull request reads the merge commit, so it
  does not get the check either. Protect the default branch with the pull
  request checks.

The coordinator computes the state each time it posts the status.

### Status after a change

After a change of the set, the coordinator recomputes and posts
`Reactorcide Checks` of the open pull request events of the project. A
change is: the set turned on or off, an approved promotion or demotion, a
quarantine, the early end of a quarantine, and the expiry of a quarantine.

One coordinator replica does this, with the lease
`required-set-republish`. Every 15 seconds it reads:

- the audit events of the changes (`required_set.enable`, `.disable`,
  `.promote`, `.remove`, `.quarantine`, `.unquarantine`), and
- the quarantines with an expiry since the previous pass.

For each project with a change, it takes the newest event of each pull
request of the last 30 days, when that event is not a close or a merge
event. It posts the status of the event only when the result changed. Thus
the pass works the same for REST, CLI, UI, and pull request commands, and
after a restart. A pass reads changes again from 2 minutes before the
previous pass, so a change that commits late is not lost. A new lease holder
reads the last 10 minutes.

## Load Shedding

At a shed level that has `skip_advisory` (the `critical` level by default),
the eval does not start advisory workflows. The decision of a skipped
workflow is `skipped` with the shed reason. Required workflows always run.
See [Events and Schedules](./events-and-schedules.md).

## Audit Events

Each change writes an audit event with the project, the workflow ID, the
branch pattern, and the reason:

| Action | When |
| --- | --- |
| `required_set.enable`, `required_set.disable` | The set was turned on (with the seeded IDs) or off. |
| `required_set.propose` | A proposal was made. |
| `required_set.approve`, `required_set.reject`, `required_set.withdraw` | A proposal was decided. The event has the approvers and their source. |
| `required_set.promote`, `required_set.remove` | An approved proposal changed the set. |
| `required_set.quarantine`, `required_set.unquarantine` | A quarantine started or ended early. |
| `required_set.approvers`, `required_set.org_settings` | Approver settings changed. |

## Interfaces

| Channel | Operations |
| --- | --- |
| REST | `GET /api/v1/projects/{project}/required-set`; `POST .../required-set/enable`, `.../disable`; `PUT .../required-set/approvers`; `GET`, `POST .../required-set/proposals`; `POST .../required-set/proposals/{id}/approve`, `/reject`, `/withdraw`; `POST .../required-set/quarantine`, `.../unquarantine`; `GET`, `PUT /api/v1/organizations/{org}/required-set-settings` |
| CLI | `reactorcide required-set enable`, `disable`, `list`, `proposals`, `propose`, `remove`, `approve`, `reject`, `withdraw`, `quarantine`, `unquarantine`, `approvers`, `org-settings` |
| Pull request comment | `/reactorcide promote WORKFLOW-ID`, `/reactorcide demote WORKFLOW-ID` (every target branch) |
| CSIL (UI) | `get-required-set`, `set-required-set-enabled`, `set-required-set-approvers`, `propose-required-set-change`, `decide-required-set-proposal`, `quarantine-required-workflow`, `unquarantine-required-workflow`, `get-required-set-org-settings`, `put-required-set-org-settings` |
| UI | The "Required set" card on the project page. The "Required set defaults" card on the organization CI trust page. An "Advisory" or "Quarantined" label on the event page (each workflow decision) and on the workflow page (from `required_state` of the decision). |

`{project}` is a project ID, a project name, or a repository URL.

A quarantine body takes `duration` (`72h`, `7d`) or `until` (RFC 3339).
