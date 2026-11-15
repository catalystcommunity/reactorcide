# CI Approvals Plan

Status: decided 2026-11-15; implementation in progress. This plan replaces the
approval model on branch `todpunk/linkkeys-requests` before it deploys.
Nothing on that branch is committed or deployed, so the old comment forms
and approval kinds are removed with no compatibility period.

## Goal

A person approves a pull request head. The person does not supply workflow
names, profiles, or policy revisions. The coordinator evaluates the current
head under the current configuration and policy. Each approval means one
clear thing:

| Command | Meaning |
| --- | --- |
| `/reactorcide approve-ci` | Run this head on trusted base CI. The head's changes to CI are ignored. |
| `/reactorcide override-ci` | Run this head on its own CI code. |
| `/reactorcide accept [LABEL]` | I accept shared responsibility for this change. Not related to CI. |
| `/reactorcide revoke` | Withdraw my approvals for this head (see Revoke). |
| `/reactorcide retry` | Retry the failed and cancelled jobs of this head's workflows. |
| `/reactorcide rerun` | Run all workflows of this head again. A push of an empty commit also does this. |

## Owner Decisions

- D1: A run gate is an org or project setting. It is the equivalent of the
  GitHub first-time contributor approval. A setting selects the authority of
  an approved gated run: trusted base CI with no secrets (the default), or
  trusted base CI with the normal profile and secrets.
- D2: The policy document is too large. Simple items become org and project
  settings. Users with access edit them in the UI. The CLI and the API also
  set them, with the same authorization as today, including an admin token
  when there are no users.
- D3: The remaining policy stays a document. The UI gets a configurator for
  it, not a text editor. A manager or a product person must be able to use
  it.
- D4: An approval binds to the head SHA only. A new base SHA or a new policy
  revision does not cancel it. A policy change applies to approvals that
  already exist.
- D5: The label of `accept` is optional. One review counts for all roles of
  the reviewer.
- D6: A required review can require "any person who is not an author of the
  code or of the pull request". Some compliance programs need this.
- D7: Required reviews are native. The coordinator evaluates them and posts
  their status. No job is necessary.
- D8: `retry` retries only the failed parts of the workflows.
- D9: Revoke uses "last state wins" for each role (see Revoke).
- D10: All channels (PR comment, PR review, CLI, REST, UI) use one approval
  path with the same effects.
- D11: Re-evaluate and re-run are different operations. Use the correct word.
- D12: The approvals stay in Reactorcide. Reactorcide gets facts from each
  VCS. VCSStar integration comes later.

## Design

### 1. Settings and policy

Settings use the existing pattern: a column on `organizations` and on
`projects`, with `inherit` on the project (see `duplicate_events`).

Proposed settings (open question Q6):

- `run_gate`: `none`, `forks`, `first_time_contributors`,
  `outside_contributors` (no `repository_write`).
- `gated_run_authority`: `no_secrets` (default) or `normal`.
- `run_approvers`: subjects that can `approve-ci` and `retry`.
- `override_approvers` and `override_profile` (default `pr-untrusted`), if
  Q1 selects option (b).
- `required_reviews` (see 3).

The policy document keeps the `head_ci` rules: automatic authority by
workflow, path, actor, event, and branch, plus `base_nodes`.

The UI configurator edits the typed `CiPolicyDocument` that CSIL already
has. It shows a form for each rule and it explains each field in plain
words. It does not show YAML. It uses `policy explain` to show the effect
of a change before save.

### 2. Approval kinds

| Kind | Made by | Effect |
| --- | --- | --- |
| `run` | `approve-ci` | Satisfies the run gate for the head. |
| `head_ci` | `override-ci` | Lets head CI run (see Q1). |
| `accept` | `accept`, an approved PR review | Counts toward required reviews. |

An approval row has: project, PR, head SHA, kind, optional label, approver
subjects, the person, the time, and the state (`approved` or `revoked`).
Remove `workflow_scope`, `execution_profile`, `policy_revision`, and
`base_sha` from new approvals. Replace migration `000046`; do not add
another migration on top of it.

The profile of a run always comes from settings or from a policy rule. It
never comes from the approval.

### 3. Required reviews

A required review has an ID, optional paths, an optional label, the subjects
that can satisfy it, and a count. A subject can be `non_author`: any person
who is not an author of the change (see Q2).

The coordinator evaluates required reviews on each event and on each
approval or revoke. It posts the result as a status. The merge block comes
from the VCS required status. A required review does not gate CI.

An `accept` with no label counts for every required review that the
reviewer can satisfy. An `accept` with a label counts only for required
reviews with that label.

### 4. Revoke

The state is kept for each pair of (required review, role). The last
action for the pair wins.

- A revoke cancels the approval of each role of the revoker that the
  required review accepts. It also cancels the approval of another person
  who has the same role.
- An approval through a role that the revoker does not have stays.
- A role that the required review does not accept is not changed by an
  approval or a revoke.

### 5. Re-evaluate, re-run, retry

- **Re-evaluate**: the coordinator calculates the decisions again for the
  head. It starts only the workflows whose decision changed (for example
  "not run" to "run", or base CI to head CI). An `accept` or a `revoke`
  starts no workflow. It only updates the review status.
- **Re-run**: all workflows of the head run again. No command does this.
  A push or a reopen does it.
- **Retry**: `/reactorcide retry` calls `RetryUnsuccessfulJobs`
  (`internal/jobcontrol/retry.go`) for each workflow of the head.

A re-evaluation starts only when the approval changes a decision. This
replaces the current check for a non-`vcs_user:` subject.

### 6. VCS facts

- Handle `pull_request_review`. An `approved` review is an `accept`. The
  review body can name a label. A review never makes a `run` or a `head_ci`
  approval.
- Get authors for D6 from the provider (see Q2). Never use git author
  fields alone.
- Get first-time contributor facts from GitHub `author_association`.

### 7. Keep from the current branch

B1 (`vcs_user:` subjects), R1 (reserved event facts), R4 (groups and
identity CLI and REST), and the single approval code path stay. Replace the
comment parser, the approval kinds, the migration, and the re-evaluation
trigger.

## Resolved Questions (2026-11-15)

The owner accepted each recommendation:

- Q1: (b). `override-ci` is a separate grant from `override_approvers` and
  runs with `override_profile`. A `head_ci` rule with `approval:` is also
  satisfied by an `override-ci` approval from a listed subject; that rule's
  profile then applies.
- Q2: authors are the PR author, each actor of a head update of the PR, and
  each commit author and committer that the provider links to an account. The
  last person who pushed cannot approve.
- Q3: for `non_author`, the role is the person.
- Q4: a count is the number of different people with the role who approved
  after the last revoke for that role.
- Q5: "changes requested" is a revoke by that reviewer. A dismissed review
  cancels only that reviewer's approval.
- Q6: the list in 1. A project can make a setting more strict. Only an org
  admin can make it less strict. Required reviews add: org reviews apply to
  each project, and a project adds its own.
- Q7: an org admin for org settings, a project owner for project settings,
  and tokens with `policies:manage`.
- `/reactorcide rerun` is added. Permission: `run_approvers`, the same as
  `retry`.

## Implementation Notes

- Fact found during design: with no stored policy, a same-repository PR runs
  its own CI with no gate (`useEventCIWithoutPolicy`). A fork PR runs base CI
  with `defaults.profile`, and secrets are available to it unless the job
  design prevents it. A run gate is the control for this.
- Default `run_gate` is empty (no gate), so a deploy does not change which
  PRs run. An org admin selects a gate.
- `no_secrets` uses the profile `pr-untrusted` for every node of the gated
  run. The coordinator sets this in the reserved fact
  `REACTORCIDE_RUN_PROFILE`.
- A gated event has status `awaiting_approval` and no eval job.
  "Reactorcide Checks" is pending with a description that names
  `/reactorcide approve-ci`.
- Required reviews post their own status, `Reactorcide Reviews`. They are
  not related to CI, so "Reactorcide Checks" does not include them.
- Approval rows are append-only actions (`approved` or `revoked`). State is
  calculated from the rows. A provider object ID (comment ID or review ID)
  makes a redelivery a no-op.
- Re-evaluation after an approval drops a workflow whose decision (CI
  origin and profile) is the same as the last evaluation of the same head
  that ran it.

## Work Breakdown

| ID | Work | State |
| --- | --- | --- |
| W1 | Migration 000046 (replace): approvals as actions, settings table, event status `awaiting_approval`, event source `approval` | done |
| W2 | Models: approval, settings (effective value, strictness check) | done |
| W3 | Store: approval actions, settings, authors and pushers of a PR | done |
| W4 | VCS: `pull_request_review`, author association, commit identities, object IDs | done |
| W5 | One approval service: parse commands, record, effects (re-evaluate, reviews status, retry, rerun) | done |
| W6 | Run gate in PR processing; checks status for `awaiting_approval` | done |
| W7 | Trigger processor: override grant, gated profile, unchanged-decision drop | done |
| W8 | Required reviews evaluator and `Reactorcide Reviews` status | done |
| W9 | REST, CLI, CSIL: approvals, revoke, settings | done |
| W10 | UI: settings forms and the policy configurator | done |
| W11 | Runnerlib reserved env sync | done |
| W12 | Docs, tests, `linkkeys_trust_requests.md`, agent-learning | done |
