# LinkKeys trust-guard requests

From: LinkKeys (`docs/trust-change-control.md`, `.reactorcide/plugins/trust_guard.py`).
Date: 2026-11-14.

LinkKeys gates trust-critical pull requests on a coordinator approval:
`/reactorcide approve linkkeys-trust-anchors <profile> <policy-revision>`. The
guard node reads `REACTORCIDE_CI_APPROVALS`, `REACTORCIDE_SHA`, and
`REACTORCIDE_DIFF_BASE`, and accepts only an approval with that exact scope, an
allowed subject, and the exact head and base SHA. It never reads git author
fields. The coordinator policy grants the node `base_nodes` authority.

The guard is only as strong as the facts the coordinator gives it. These are
the gaps we found.

## R1 — Reserve coordinator facts in job environments

Status: open

**Problem.** `worker/trigger_processor.go` builds a node's env from the parent
job's `JobEnvVars`, then applies `spec.Env` from the workflow/job YAML on top.
So an `environment:` block can set or replace `REACTORCIDE_CI_APPROVALS`,
`REACTORCIDE_ACTOR_SUBJECTS`, `REACTORCIDE_SHA`, `REACTORCIDE_DIFF_BASE`, and
the other event facts. `runnerlib/src/eval.py` already rejects
`_RESERVED_AUTHORITY_KEYS` as YAML fields, but not as env names.

The job API (`POST` job with `job_env_vars`, `job_handler.go`) also accepts
any names. We have not checked whether such a job can post a commit status
that a branch ruleset counts.

**Outcome needed.** Event facts that the coordinator sets (`REACTORCIDE_*`
from the webhook and eval job, at least `ACTOR_SUBJECTS`, `CI_APPROVALS`,
`SHA`, `DIFF_BASE`, `HEAD_*`, `BASE_*`, `PR_*`, `HEAD_ACTOR`) cannot be set or
replaced by repository YAML, base or head, nor by an API-submitted job. Reject
the YAML with a clear error, as for authority fields. A job without a webhook
event never reports a status into a PR's required checks.

**Verify.** A workflow node with `environment: {REACTORCIDE_CI_APPROVALS: "[]"}`
fails evaluation with a reserved-name error. A node without it sees the
coordinator's value.

## R2 — Content approvals, separate from head-CI approvals

Status: open

**Problem.** An approval's `workflow_scope` is free text and `*` is a head-CI
wildcard (`trigger_processor.go`: `approval.WorkflowScope == id ||
approval.WorkflowScope == "*"`). LinkKeys uses the scope
`linkkeys-trust-anchors`, which works only because no workflow has that id. A
"run this PR's CI" approval and an "I approve this trust change" approval are
different decisions, but they share one record type, and the content approval
must name a profile and policy revision that mean nothing for it.

**Outcome needed.** A content approval kind, for example
`/reactorcide approve-content <label>`: pinned to head + base SHA, invalidated
on push like now, never used for head-CI admission, and `*` never matches it.
Delivered to jobs in its own field or variable.

**Verify.** `/reactorcide approve-content linkkeys-trust-anchors` creates an
approval that head-CI admission ignores and that appears in the job's facts.

## R3 — An approval re-evaluates the pull request

Status: open

**Problem.** `processApprovalComment` stores the approval and returns. Facts
are snapshotted into `JobEnvVars` only when a `pull_request` webhook arrives
(`webhook_handler.go`), and a retry reuses the old env. A push makes a new head
and invalidates the approval. So after an approval, the only way to see it is
to close and reopen the PR.

**Outcome needed.** A new approval (content or head-CI) for the current head
starts a fresh evaluation of that head with current facts. Or: retry refreshes
event facts from the coordinator instead of copying the old env.

**Verify.** Approve on a PR whose `linkkeys-trust-guard` failed for lack of an
approval. The guard re-runs on the same head and passes, with no push and no
reopen.

## R4 — Manage groups

Status: open

**Problem.** The coordinator resolves `reactorcide_group:<name>` subjects
(`ListGroupsForUser`, for users with a verified VCS identity link), but no CLI
command or API route creates a group or adds members. LinkKeys wants the
provider-neutral approver `reactorcide_group:linkkeys-trust-maintainers`,
because vcsstar becomes a second job source and the same people must be able
to approve from either provider.

**Outcome needed.** `reactorcide groups create|add-member|remove-member|list`
(org-scoped, audited), and a documented way to verify a VCS identity link.

**Verify.** Create the group with two members. An approval comment by either
member, from GitHub or vcsstar, records the group subject.

---

# Reactorcide response

From: Reactorcide. Date: 2026-11-14. Each finding was checked against the code at
commit `0381109`.

## Summary

The guard cannot pass today, and this is not caused by R1–R4. Two gaps that
the requests do not name block it first:

- **B1 (Reactorcide).** No PR comment approval is stored.
- **B2 (LinkKeys).** The required status `LinkKeys pull request` stops when
  `0381109` is deployed.

Reactorcide order of work: B1, R3, R2, R1, R4.

## B1 — Comment approvals are never stored (Reactorcide, open)

`processApprovalComment` adds `vcs_user:<provider>/<login>` as the first
subject. `CIApproval.Validate()` (`models/approval.go`) does not accept
`vcs_user:`. The first `CreateCIApproval` fails, the handler returns the
error, and the webhook responds 400. No approval is stored, and this includes
the group subject. Unit tests did not find it, because
`WebhookMockStore.CreateCIApproval` does not validate.

Effect on LinkKeys: `TRUST_APPROVERS` holds two `vcs_user:` subjects and one
group. No comment can satisfy the guard.

Reactorcide fix: `Validate` accepts `vcs_user:`, as `cipolicy` already does.
The handler skips a subject that is not valid and does not fail the full
comment. A test uses the real store.

Workaround until the fix: an approval through the UI or
`POST /api/v1/approvals` with subject `reactorcide_group:linkkeys-trust-maintainers`.
This needs the group (see R4).

## B2 — Required status changes on deploy (LinkKeys, action needed)

Commit `0381109` posts one status for each event: `Reactorcide Checks`. It
does not post workflow-name statuses for new events. `LinkKeys pull request`
will not be posted, and every PR will wait for it.

LinkKeys action:

1. Change the `ci-checks` ruleset to require `Reactorcide Checks`. Do this at
   the time of the coordinator deploy. If you do it before the deploy, PRs
   block because the current coordinator does not post that status.
2. Do not add `status: {context: LinkKeys pull request}` to `pr.yaml` as a
   substitute. Any workflow that gets head CI can declare the same context.
   Only the coordinator can post `Reactorcide Checks`, because that name is
   reserved.
3. Update `docs/trust-change-control.md` §6 (GitHub) and add a threat table
   row: "Post a passing status with the required name" → "Required status is
   `Reactorcide Checks`, which only the coordinator posts."

## R1 — Reactorcide response: accepted, smaller scope

The override is real. `buildJobFromTrigger` applies `spec.Env` on top of the
parent env. Runnerlib writes `SHA`, `HEAD_*` and `BASE_*` again after the
YAML env, but it does not do this for `CI_APPROVALS`, `ACTOR_SUBJECTS` and
`DIFF_BASE`.

For the guard, this is defense in depth. It is not an open path. A
`base_nodes` node gets its env only from base YAML, so head YAML cannot change
it. Your threat table already states this.

Changes to the request:

- Reactorcide enforces the reserved names in the coordinator
  (`buildJobFromTrigger`). Runnerlib also rejects them, to give a clear error.
  The coordinator is the authority because the eval output comes from inside
  a job container.
- The API part is dropped. API-submitted jobs have no VCS metadata, so they
  post no commit status (`MetadataFromJob` returns nil). "A job without a
  webhook event never reports a status" is already true.

LinkKeys action: none.

## R2 — Reactorcide response: accepted

Planned design:

- A `kind` column on approvals: `head_ci` or `content`.
- Head-CI admission uses only `kind = head_ci`. `*` never matches `content`.
- A content approval has no profile or policy revision, so a policy edit does
  not invalidate it. It stays pinned to the head and base SHA, and a push
  invalidates it.
- Command: `/reactorcide approve-content <label>`.
- Jobs get content approvals in `REACTORCIDE_CONTENT_APPROVALS`.

LinkKeys action (when R2 ships): the guard reads
`REACTORCIDE_CONTENT_APPROVALS`, and the help text names the new command. For
the change period, the guard can also accept the old scoped approval, to
prevent a gap during the deploy.

## R3 — Reactorcide response: accepted, first option

When a new approval matches the current PR head, the coordinator records a CI
event and starts a new evaluation. It uses facts from the provider at that
time. The event has its own idempotency key. The "retry refreshes facts"
option is rejected: a retry repeats the same facts, and a retry of the guard
node alone does not do head-CI admission again. This also corrects head-CI
approvals, which have the same problem.

LinkKeys action: none. Until R3 ships, push a commit or close and reopen the
PR after the approval.

## R4 — Reactorcide response: partly available now

The request says that no route creates a group. That is not correct:

- Groups: the UI can create groups and add members (CSIL `CreateGroup`,
  `AddGroupMember`, `ListGroupMembers`). An org admin must do this.
- Identity links: `POST` identities (REST, `organizations:manage`). Links
  made this way have `verified_by=admin`, which `ListGroupsForUser`
  resolution accepts. Login matching is not case-sensitive.

Still open: a CLI (`reactorcide groups ...`, `reactorcide identities ...`)
and docs. An approval from vcsstar also needs vcsstar support as a
coordinator VCS provider, which is a separate item.

LinkKeys action:

1. Ask the Reactorcide operator to create `linkkeys-trust-maintainers` with
   identity links for the approvers. This can be done now.
2. Correct §6 of `docs/trust-change-control.md`: it says the guard accepts no
   group until R4. The group works after B1 is fixed, or through the
   UI/API approval workaround now.

## LinkKeys checklist

- [ ] B2: Change the ruleset to `Reactorcide Checks` at the coordinator deploy.
- [x] B2: Update the §6 GitHub text and add the status-spoofing threat row. (LinkKeys, 2026-11-14)
- [ ] R4: Get `linkkeys-trust-maintainers` and its identity links created.
- [x] R4: Correct the §6 text about groups. (LinkKeys, 2026-11-14)
- [x] Guard help text: until B1 ships, name the UI/API group approval as the
      working path. (LinkKeys, 2026-11-14)
- [x] R2 (later): Read `REACTORCIDE_CONTENT_APPROVALS`, and accept both forms
      during the change period. (LinkKeys, 2026-11-14, done early. The guard
      takes the label from `label` when `kind` is `content`, else from
      `workflow_scope`, and refuses any other `kind`. Tell us if the field
      name differs.)

---

# Implementation status (Reactorcide)

Date: 2026-11-15. This section replaces the status of 2026-11-14. Nothing is
committed or deployed. It deploys together with `0381109` (Reactorcide
Checks). Migration: `000046_ci_approval_kinds.sql`. Design and owner
decisions: `CI_APPROVALS_PLAN.md`. Reference: `docs/organizations-and-ci-policy.md`,
section "Run Gate, Approvals, and Required Reviews".

The Reactorcide owner changed the approval model before the deploy. The R2
and R3 forms of 2026-11-14 (`/reactorcide approve WORKFLOW PROFILE REV`,
`/reactorcide approve-content LABEL`, `REACTORCIDE_CONTENT_APPROVALS`) are
removed. They were never deployed.

| Item | Status | Notes |
|---|---|---|
| B1 | Done | `vcs_user:` is a valid approver subject. |
| R1 | Done | Reserved event facts, in runnerlib and the coordinator. `REACTORCIDE_CONTENT_APPROVALS` is no longer a fact; `REACTORCIDE_RUN_AUTHORITY` is new. |
| R2 | Replaced | Required reviews are native. The coordinator evaluates them and posts the status `Reactorcide Reviews`. No guard job has to read approvals. |
| R3 | Replaced | `accept` and `revoke` update `Reactorcide Reviews` at once. They start no CI. `approve-ci` and `override-ci` re-evaluate the head. |
| R4 | Done | `reactorcide groups ...` and `reactorcide identities ...`. |

## The new commands

| Comment | Meaning |
|---|---|
| `/reactorcide accept [LABEL]` | Accept shared responsibility for the change. Counts toward required reviews. |
| `/reactorcide revoke` | Withdraw your approvals for every role you have. |
| `/reactorcide approve-ci` | Release the run gate: run on trusted base CI. |
| `/reactorcide override-ci` | Run the head's own CI. |
| `/reactorcide retry`, `/reactorcide rerun` | Retry failed jobs, or run every workflow again. |

An approval binds to the head SHA only. A base change does not cancel it. An
approved GitHub pull request review is an accept. A line
`/reactorcide accept linkkeys-trust-anchors` in the review body gives it the
label.

## What LinkKeys does now

1. **Configure the required review** in the coordinator (an org admin, or a
   project owner, because this makes the project more strict):

   ```yaml
   # linkkeys-trust.yaml
   required_reviews:
     - id: linkkeys-trust-anchors
       description: Trust anchors, their loaders and verifiers, and the CI directory
       paths:
         - crates/liblinkkeys/src/lib.rs
         - crates/liblinkkeys/src/crypto.rs
         # ... the rest of the trust-critical list in docs/trust-change-control.md
         - .reactorcide/**
       accept:
         - vcs_user:github/todpunk
         - vcs_user:github/junipuff
         - reactorcide_group:linkkeys-trust-maintainers
       count: 1
   ```

   ```bash
   reactorcide trust set --org ORG --project linkkeys --file linkkeys-trust.yaml
   ```

   Use the real repository paths. A review with `paths` applies only when the
   pull request changes one of them.
2. **Require `Reactorcide Reviews`** in the LinkKeys ruleset, in addition to
   `Reactorcide Checks` (B2). Do this at the deploy.
3. **Remove the approval read from `trust_guard.py`.** The guard no longer has
   to find an approval. `REACTORCIDE_CI_APPROVALS` has a new shape (approval
   actions: `kind`, `action`, `label`, `person`, `subjects`, `head_sha`). Its
   state needs the "last action for each role wins" rule, so do not
   recalculate it in a job. The guard can keep checks that are not about
   approvals.
4. **Tell approvers** to comment `/reactorcide accept linkkeys-trust-anchors`,
   or to approve the review with that line in the body.
5. **Group (optional).** After the deploy, an operator can run:

   ```bash
   reactorcide groups create --org ORG linkkeys-trust-maintainers
   reactorcide groups add-member --org ORG linkkeys-trust-maintainers USERNAME
   reactorcide identities link --org ORG --provider github --login LOGIN --username USERNAME
   ```

   The organization name is not confirmed. The `vcs_user:` subjects work
   without the group.

A note on self-accepts: the subjects in `accept` are alternatives. The pull
request author can accept their own change when the author has a listed
subject. To prevent that, add a second review for the same paths with
`accept: [non_author]`. Then a person with a role who did not write the change
must also accept. LinkKeys did not ask for this; decide if it is necessary.
