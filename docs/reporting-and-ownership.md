# Reporting and Ownership

This document tells how to control what Reactorcide writes to the VCS, how to
compare Reactorcide with another CI system, how to import the code owners
file, and how owners accept the path dependencies of workflows.

| Feature | Setting | Default | Plan item |
| --- | --- | --- | --- |
| Report modes | `report_mode` | `full` | A3 |
| Agreement report | `agreement_contexts` | none (off) | A3 |
| Code owners import | `codeowners_import` | `off` | A2 |
| Path acceptance | `path_acceptance`, `path_owners` | `off`, none | A7 |

With no configuration, nothing changes: Reactorcide writes statuses and the
pull request comment, the VCS enforces code owners, and any workflow can claim
any path.

## Settings

Each setting has an organization value and a project value. A project value
that is absent inherits the organization value. An empty list is a value: an
empty `agreement_contexts` turns the agreement report off for the project.
The settings are in tables of their own (migration 000056), not in the
project row.

| Setting | Values | Who changes the project value |
| --- | --- | --- |
| `report_mode` | `full`, `statuses_only`, `silent` | A project owner or an org admin |
| `agreement_contexts` | Status context and check names (a glob is allowed) | A project owner or an org admin |
| `codeowners_import` | `off`, `on` | An org admin |
| `codeowners_cache_ttl_seconds` | 10 to 86400 (default 600) | An org admin |
| `path_acceptance` | `off`, `warn`, `enforce` | An org admin |
| `path_owners` | Registry rules: `{pattern, owners}` | An org admin |

Only an org admin changes the organization values. The admin API token is an
org admin of every organization, so a deployment with no logins can use each
feature. Each change writes an audit event: `reporting.settings` (project) or
`reporting.org_settings` (organization), with the values before and after.

```bash
reactorcide reporting show --project my-app
reactorcide reporting set --project my-app --report-mode silent --agreement-context ci/build
reactorcide reporting set --project my-app --inherit report_mode
reactorcide reporting org-show --org acme
reactorcide reporting org-set --org acme --report-mode statuses_only
```

`org-set` replaces all organization values. A value that you do not give gets
its default. A comma splits a CLI flag value. Put a name that has a comma in
the file of `--agreement-contexts-file` (one name on each line).

## Report Modes

The report mode tells which VCS writes a project gets.

| Mode | Commit statuses | Pull request comment |
| --- | --- | --- |
| `full` | yes | yes |
| `statuses_only` | yes | no |
| `silent` | no | no |

The mode applies to every VCS write of Reactorcide:

- `Reactorcide Checks`, the workflow `status.context` statuses, and the
  legacy statuses (`Reactorcide Jobs`, `Reactorcide CI Policy`,
  `reactorcide/eval`).
- `Reactorcide Reviews` (required reviews).
- The shared pull request comment and the legacy rolling comments.

In every mode, the results stay in Reactorcide. The UI and the API show the
events, the workflows, the reviews, and the `Reactorcide Checks` state.

Use `silent` or `statuses_only` to run Reactorcide next to another CI system.
In that period, do not require `Reactorcide Checks` in branch protection.

Notes:

- When the coordinator cannot read the settings, it uses `silent`. Thus a
  project that selected `silent` gets no status or comment because of a
  database error.
- In `silent`, the coordinator records the `Reactorcide Checks` state as
  published. After you change the mode to post statuses, the coordinator
  posts the status of an event at the next change of the event.
- In `statuses_only` and `silent`, the reconciler marks the comment of a pull
  request as written and writes nothing. After you change the mode to `full`,
  the next update of the pull request writes the full comment.

## Agreement Report

The agreement report compares the Reactorcide result of each commit with the
result of another CI system on the same commit. It is the evidence for a
cut-over from that CI system.

1. Set `agreement_contexts` to the names of the other CI system: commit
   status contexts and check run names. A name can be a glob, for example
   `ci/*` or `build (ubuntu*`. Reactorcide contexts never count.
2. When an event of the project gets a final `Reactorcide Checks` result
   (success, or failure, error, cancelled), the coordinator stores a row:
   project, commit SHA, Reactorcide result. A later event of the same commit
   with another result starts the comparison again. This occurs in every
   report mode, also in `silent`.
3. A background pass reads the combined commit status and the check runs of
   the commit from the VCS API. Only one coordinator replica runs the pass
   (lease `ci-agreement`). The other CI system can finish later, so the pass
   reads again with a backoff: 1 minute, then 2, 4, and more, up to 1 hour.
4. The row is `complete` when each configured name has a final result. The
   other result is `failure` when one matching context failed, else
   `success`. Neutral and skipped check runs count as success. `agree` is
   true when the two results are equal.
5. After 24 hours, the pass stops. A row with no other result is
   `no_other`. A row with some final results is `complete` with them.

When a VCS read fails, the error goes into `last_error`. The row stays
`pending`, and the pass reads again with the backoff.

```bash
reactorcide reporting agreement --project my-app --since 30d
reactorcide reporting agreement --project my-app --all --format json
```

The summary has the agreement rate (agreed / compared), the count of commits
where Reactorcide failed and the other CI passed, the count where Reactorcide
passed and the other CI failed, and the list of disagreements with links to
the commits. The project page shows the same summary for the last 30 days.

The VCS credential of the project must be able to read commit statuses and
check runs. A credential that cannot read check runs gets the commit statuses
only. The first implementation reads GitHub only.

## Code Owners Import

The VCS enforces code owners first. By default, Reactorcide changes nothing
(owner decision D8). With `codeowners_import: on`, Reactorcide also imports
the code owners file into its required reviews:

- The coordinator reads the file from the TRUSTED base SHA of the pull
  request through the VCS API. The head of the pull request cannot change the
  owners of its own change.
- The locations are `.github/CODEOWNERS`, `CODEOWNERS`, and
  `docs/CODEOWNERS`. The first file found is the file.
- The coordinator caches the parsed file for each (repository, base SHA).
  The TTL is `codeowners_cache_ttl_seconds` (default 10 minutes).
- For each changed file, the last matching rule decides the owners. Files
  with the same owner set make one derived required review. A rule with no
  owners means that the file has no owner, so it needs no review.
- Each derived review needs one accept from one owner of its set.

The patterns have the GitHub semantics:

| Pattern | Matches |
| --- | --- |
| `*` | Every file |
| `*.js` | Each `.js` file, at any level |
| `/build/logs/` | Each file in `build/logs` at the root, and in its subdirectories |
| `docs/*` | The files in `docs` at the root, not the files in its subdirectories |
| `apps/` | Each file in a directory `apps`, at any level |
| `**/logs` | Each file in a directory `logs`, at any level |
| `/apps/github` (no owners) | The files in `apps/github` have no owner |

A pattern with a slash at the start or in the middle is relative to the root.
`!` (negation) and `[ ]` (ranges) do not work, as in GitHub. The coordinator
skips a line with invalid syntax and logs a warning. The other lines apply.

The owners become subjects:

| Owner | Subject |
| --- | --- |
| `@org/team` | `vcs_team:org/team` |
| `@user` | `vcs_user:github/user` |
| `name@example.com` | `principal:UUID` when the principal resolver finds a LinkKeys identity or a user with that email. Else the coordinator ignores the owner and logs a warning. |

A person accepts as for every required review: `/reactorcide accept`, an
approved pull request review, or `/reactorcide accept LABEL` with the ID of
the derived review. The ID is `codeowners-`, the owner names, and a short
hash of the full owner set, for example `codeowners-acme-payments-1a2b3c4d`.
The hash is always there: `acme/a-b`, `acme/a.b`, and `acme/a_b` give the
same names part, and the team `acme/a` with the user `b` gives the same
names part as the team `acme/a-b`. Each owner set gets one review. The
comment and the `Reactorcide Reviews` status show the full ID. A team
accept needs the live team lookup of the VCS (a comment or a review).

Fail closed:

- When the coordinator cannot list the changed files, every owner set of the
  file applies.
- When the coordinator cannot read the file, one review
  `codeowners-unavailable` applies. Nobody can accept it. The next evaluation
  reads the file again.
- With the import on, the coordinator posts `Reactorcide Reviews` also when
  no review applies. Thus a branch rule that requires it can pass.

## Required Reviews in the Pull Request Comment

The pull request comment has a "Required reviews" section. Each required
review has its own line: its state (waiting, accepted, does not apply), its
ID, its description, the accept count, the people who accepted, and the
subjects that can accept. Thus the `Reactorcide Reviews` status is not
opaque. The section belongs to the pull request head. It stays in the comment
across evaluations of the head.

## Path Acceptance

A workflow can claim any path with `paths.include`. Path acceptance lets the
owner of an area accept or refuse that a workflow of another owner depends on
paths in the area.

The path ownership map is:

- the code owners file of the trusted base SHA, when `codeowners_import` is
  `on`, or
- the coordinator registry `path_owners`, else. A rule is a pattern of the
  code owners syntax and a list of owner subjects. The last matching rule
  wins. An empty owner list means that the path has no owner.

The owner of a workflow is its owner group and its owner subjects, from its
trusted definition (see [Workflow Design](./workflow-design.md)). A workflow
with no owner is foreign to every owned area.

At admission, the coordinator checks each matched file of each workflow that
the eval selected by its paths filter:

- A file with no owner, or in an area of the workflow owner, counts.
- A file in an area of another owner counts only when an active acceptance
  exists: the same workflow, an owner of the area, and a pattern that matches
  the file.

| `path_acceptance` | Effect of a matched file with no acceptance |
| --- | --- |
| `off` | None. |
| `warn` | The workflow runs. Its decision has a `path_acceptance` reason with the files and the text "paths in X are not accepted by owner Y". |
| `enforce` | The file does not count. When no matched file stays, the workflow is `skipped` with the reason "paths in X are not accepted by owner Y". When other files stay, the workflow runs, and the reason lists the files that did not count. |

When the eval capped the list of matched files and the full changed-file list
is not known, `enforce` acts as `warn` for that workflow. When the ownership
map or the acceptances cannot be read, path acceptance does not apply to the
event, and the coordinator logs why. Each flag, drop, and skip writes an
audit event (`path_acceptance.flag`, `path_acceptance.drop`,
`path_acceptance.skip`).

### Acceptances

An acceptance has: the project, the workflow security ID, the path pattern,
the owner subject, the state, who requested and who accepted, and when.

| State | Meaning |
| --- | --- |
| `requested` | A workflow owner asks the owner of the area. |
| `accepted` | The owner accepts. Only this state counts at admission. |
| `rejected` | The owner refuses the request. |
| `revoked` | One of the two sides ended the request or the acceptance. |

| Operation | Who |
| --- | --- |
| Request | A workflow owner, a project owner, an owner of the path, or an org admin |
| Request and accept at one time (`--accept`) | An owner of the path, or an org admin |
| Accept, reject | An owner of the path, or an org admin |
| Revoke | An owner of the path, a workflow owner, a project owner, or an org admin |

An owner of the path is a person with the owner subject: a principal
subject, a Reactorcide group, or a Reactorcide group that is linked to the
VCS team (see [Groups linked to VCS teams](./principals.md#groups-linked-to-vcs-teams)). One open row
(requested or accepted) exists for each workflow, pattern, and owner. Each
change writes an audit event: `path_acceptance.request`,
`path_acceptance.accept`, `path_acceptance.reject`, `path_acceptance.revoke`.

```bash
reactorcide path-acceptances request --project my-app --pattern /payments/ \
  --owner reactorcide_group:payments --reason "reads the payment schema" build
reactorcide path-acceptances list --project my-app --state requested
reactorcide path-acceptances accept --project my-app <acceptance-id>
reactorcide path-acceptances revoke --project my-app --reason "moved" <acceptance-id>
```

A build tool with its own visibility rules can enforce the same thing inside
the build. Reactorcide does not depend on it (owner decision D6).

## Interfaces

| Channel | Operations |
| --- | --- |
| REST | `GET`, `PUT /api/v1/projects/{project}/reporting`; `GET /api/v1/projects/{project}/agreement?since=7d&limit=500`; `GET`, `POST /api/v1/projects/{project}/path-acceptances`; `POST .../path-acceptances/{id}/accept`, `/reject`, `/revoke`; `GET`, `PUT /api/v1/organizations/{org}/reporting-settings` |
| CLI | `reactorcide reporting show`, `set`, `org-show`, `org-set`, `agreement`; `reactorcide path-acceptances list`, `request`, `accept`, `reject`, `revoke` |
| CSIL (UI) | `get-reporting-settings`, `update-reporting-settings`, `get-agreement-report`, `list-path-acceptances`, `request-path-acceptance`, `decide-path-acceptance`, `get-org-reporting-settings`, `put-org-reporting-settings` |
| UI | The "Reporting and ownership" card on the project page (with the project path owner registry). The "Reporting defaults" card on the organization CI trust page (with the organization path owner registry). |

`{project}` is a project ID, a project name, or a repository URL. `{org}` is
an organization name or ID.

The `PUT .../reporting` body changes only the values that it has. `inherit`
lists the values to clear: `report_mode`, `agreement_contexts`,
`codeowners_import`, `codeowners_cache_ttl_seconds`, `path_acceptance`,
`path_owners`.
