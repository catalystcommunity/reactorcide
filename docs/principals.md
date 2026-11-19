# Principals

A principal is anything that can act in Reactorcide. There are three kinds:

| Kind | What it is | Organization |
| --- | --- | --- |
| `human` | A person. A Reactorcide user, or a VCS login from a signed webhook. | None. A human can act in many organizations. |
| `service` | A program, for example a deploy tool or a bot. | One organization. An instance token has a service principal with no organization. |
| `system` | The system principal "Reactorcide". | None. |

The kind is for reporting. Policy can also use it. Authority comes from roles
and groups in the same way for each kind (owner decision D4).

The system principal has the fixed UUID
`00000000-0000-0000-0000-000000000001`. It is the actor when no other actor
exists, for example a schedule fire. It has no roles and no privileges.

Every feature works with no logins. The default deployment uses an admin API
token through the CLI and the REST API. Public repositories stay publicly
visible.

## Identities

A principal has one or more identities. One identity has one principal.

| Type | Value | Source |
| --- | --- | --- |
| `user` | The Reactorcide user UUID | Made with the user |
| `vcs` | `PROVIDER/LOGIN`, lowercase | A signed webhook, an admin link, or a LinkKeys link |
| `linkkeys` | `SUBJECT@DOMAIN` and `HANDLE@DOMAIN`, lowercase | A LinkKeys login |
| `vcsstar` | The VCSStar identity | An admin link |
| `token` | The API token UUID | Made with the token |
| `federated` | `ISSUER\|SUBJECT` | A token exchange of inbound federation ([Inbound Federation](./federation.md)) |

A human principal of a user has the same UUID as the user. A service
principal made from a token that existed before principals has the same UUID
as the token.

Reactorcide keeps the identities in step with their sources:

- A new user gets a human principal.
- A LinkKeys login adds both LinkKeys identity forms to the principal of the
  user.
- A VCS identity link (`reactorcide identities link`) gives the VCS identity
  to the principal of the user. When the identity belongs to another
  principal, the link is refused, except for a principal that only a webhook
  made (see [VCS logins](#vcs-logins)).
- A new service or instance token without a principal gets a service
  principal. A user token gets the principal of its user. A job token has no
  principal: it acts for its job.

### VCS logins

Reactorcide makes a human principal for a VCS login the first time that the
login is the sender of a signed webhook, or the author of a command in a pull
request comment or review. An unsigned source never makes a principal.

When an admin links that login to a user later, Reactorcide merges the
principal of the login into the principal of the user. Earlier runs keep
their actor reference. The resolver follows the merge, so these runs show the
user.

Only a principal that only a webhook made can merge. That is a human
principal with no organization, no `user`, `token`, or `federated` identity,
no manual group membership, and no role assignment (SQL function
`principals_webhook_only`). A link to an identity of any other principal is
refused (`409 Conflict`), and the identity stays with its principal. Reason:
a merge moves authority. For example, a merge of a service principal would
give its org roles to the user. To move such an identity, unlink it first.

## The principal resolver

The resolver maps one identity to its principal and to every linked identity.
Policy, approvals, and attribution use the resolver. Thus an approval by a VCS
login and a grant to a LinkKeys identity of the same person agree (owner
decision D19).

```bash
reactorcide resolve-principal --type vcs --value github/octocat
reactorcide resolve-principal --org example --type linkkeys --value alice@example.com
reactorcide resolve-principal --type principal --value PRINCIPAL_UUID
```

Without `--org`, the command needs global authority (the admin token or a
global admin). With `--org`, the command needs an org admin, and the
principal must be in the organization.

REST:

- `GET /api/v1/principals/resolve?type=TYPE&value=VALUE`
- `GET /api/v1/organizations/ORG/principals/resolve?type=TYPE&value=VALUE`

## Subjects

Approver lists, CI policy actors and approvals, and required reviews use
subjects. A person has a list of subjects. A command, a rule, or a review
matches when the person has one listed subject.

| Subject | Matches |
| --- | --- |
| `*` | Anyone who Reactorcide can identify. You must write it. |
| `principal:UUID` | One principal |
| `service:NAME` | A service principal of the organization of the project |
| `service:ORG/NAME` | A service principal of the organization `ORG` |
| `principal_kind:KIND` | Any principal of the kind `human`, `service`, or `system` |
| `identity:TYPE:VALUE` | One identity, for example `identity:linkkeys:alice@example.com` |
| `vcs_user:PROVIDER/LOGIN` | One VCS login |
| `vcs_team:OWNER/SLUG` | A member of a VCS team (live lookup). To store the membership, link a group to the team ([Groups linked to VCS teams](#groups-linked-to-vcs-teams)). |
| `reactorcide_group:NAME` | A member of a group of the organization |
| `repository_write` | A person with write access to the repository |
| `project_owner` | An owner of the project (approvals only) |
| `non_author` | A person who did not write the change (required reviews only) |

Rules:

- An omitted subject list never means anyone. Write `*` for anyone.
- The resolver adds the subjects of every linked identity. For example, a
  person who comments as `github/alice` also has
  `identity:linkkeys:alice@example.com` when that identity is linked to the
  same principal.
- The subjects of the person's own identities, `principal:`, `service:`,
  `principal_kind:`, and `*` are not roles. A `non_author` review counts only
  a person with a role: `repository_write`, a VCS team, a group, or
  `project_owner`. Any account can comment on a public repository.
- The actor is an opt-in policy fact. Nothing matches on the actor or on its
  kind unless a rule names it (owner decision D19).
- WARNING: `principal_kind:human` matches every VCS login, because each login
  that sends a signed webhook or a comment gets a human principal. On a
  public repository it is almost the same as `*`.
- A disabled principal has no principal subjects: no `principal:`,
  `principal_kind:`, `identity:`, `service:`, group, or `project_owner`
  subject, and its user groups and roles do not apply. The raw VCS login of
  a comment still gets what an unknown login gets (`vcs_user:` and the live
  VCS facts). The resolver follows a merge first, so a merged principal
  resolves to its target.

The eval job gets the actor subjects in `REACTORCIDE_ACTOR_SUBJECTS`. The
list includes `principal:UUID` and `principal_kind:KIND` of the sender.

## Actors of runs

Every run records its actor principal and the identity that it used:

| Column | Meaning |
| --- | --- |
| `actor_principal_id` | The principal that started the job, workflow, or event |
| `actor_identity` | `TYPE:VALUE` of the identity it used, for example `vcs:github/octocat` or `token:UUID` |

| Start | Actor |
| --- | --- |
| Signed webhook | The principal of the sender |
| API submit with a token | The principal of the token |
| API submit with a job token | The actor of the job of the token |
| UI session | The principal of the user |
| Schedule fire | The system principal |
| Retry | The principal that asked for the retry |
| Rerun or a re-evaluation after an approval | The re-runner or the approver. The sender facts for admission stay the facts of the earlier actor of the head. |
| Child job or child workflow | The actor of the parent |
| Workflow made by an eval or a trigger | The actor of the job that made it. A child workflow keeps the actor of its parent workflow. |

A database trigger gives a new job, workflow, event, approval, or audit record
an actor when the code does not set one: from the parent job, the workflow,
or the event, else the system principal.

The REST job and workflow responses have an `actor` object:

```json
{"principal_id": "…", "kind": "service", "display_name": "Release Bot", "identity": "token:…"}
```

The UI types `JobSummary.actor`, `WorkflowSummaryDetail.actor`, and
`CIEventDetail.actor_principal` have the same fields. Approvals record
`actor_principal_id`. Audit events record `actor_principal_id`.

## Service principals

An org admin, or the admin token, manages the service principals of the
organization (owner decision D24).

```bash
reactorcide principals create --org example release-bot --display-name "Release Bot"
reactorcide principals list --org example
reactorcide principals show --org example release-bot
reactorcide principals disable --org example release-bot
reactorcide principals enable --org example release-bot
```

A name has lowercase letters, digits, and hyphens. It starts with a letter or
a digit. It is unique in the organization.

### Roles and groups

A service principal gets authority from roles, the same as a user. Give it a
role through a group, or directly:

```bash
reactorcide principals add-group --org example release-bot deployers
reactorcide principals remove-group --org example release-bot deployers
reactorcide principals grant-role --org example --role admin release-bot
reactorcide principals grant-role --org example --role member --project app release-bot
reactorcide principals grant-role --org example --role owner --project app --workflow deploy release-bot
reactorcide principals roles --org example release-bot
reactorcide principals revoke-role --org example release-bot ASSIGNMENT_ID
```

An organization role is `admin` or `member`. A project role is `owner` or
`member`. A workflow role is `owner`. This command never gives a global
role. The UI role page also
accepts `principal_type: principal`.

### Tokens

Issue a token for a service principal:

```bash
reactorcide principals tokens create --org example release-bot \
  --capability jobs:submit --capability jobs:read
reactorcide principals tokens list --org example release-bot
reactorcide principals tokens revoke --org example release-bot TOKEN_ID
```

The command shows the token one time. Keep it in a secret store at once.

On a no-login instance (`REACTORCIDE_UI_AUTH_MODE=none`), a token issued
with no `--capability` gets all capabilities and all organizations, and the
roles of the principal do not narrow it. This is not for production. See
[No-login instances](./ui-auth.md#no-login-instances-single-admin). The
rules below apply to a token issued with a capability list, and to each
token on an instance with logins.

The effective authority of the token is its capabilities intersected with
the roles and groups of the principal, the same as a user token:

- A principal with a global admin role keeps the token scope as issued.
- A principal with no organization role has no organization scope. The token
  can do nothing.
- The token covers only the organizations where the principal has an
  organization role (`admin` or `member`).
- A `member` role keeps only the read capabilities:
  `organizations:read`, `projects:read`, `jobs:read`, `logs:read`,
  `workflows:read`, and also `diagnosis:write` (a diagnosis entry only adds
  labelled text; see [Failure Diagnosis](./diagnosis.md)).
- An `admin` role keeps the issued capabilities. It never adds capabilities.
  The issued capabilities stay only when the principal is an `admin` of each
  organization of the token. When it is a `member` of one of them, the token
  keeps only the read capabilities above, in all its organizations
  (`authz.NarrowServicePrincipalToken`).
- A project role does not widen the token scope for routes that check the
  token scope. The authorization resolver uses a project role where a
  decision names the project, for example `project_owner` in an approval.
- A disabled principal makes all its tokens invalid at once.

The caller cannot give a token capabilities that the caller does not have.
Only a caller with every capability (the admin token) can issue a token with
every capability.

A service principal token can give pull request commands through
`POST /api/v1/approvals`. Its person key is `principal:UUID`, and its
subjects are its own subjects, its groups, and `project_owner` when it has
that role.

### Federated service principals

A service principal can also authenticate with an external OIDC token, with
no static Reactorcide token. An org admin registers a trusted issuer and a
claim mapping to the principal. The workload exchanges its token at
`POST /api/v1/federation/token` for a short-lived token of the principal. The
issued token intersects the mapping capabilities with the roles of the
principal, the same as a token from `principals tokens create`. The exchange
records the identity `federated:ISSUER|SUBJECT` on the principal. See
[Inbound Federation](./federation.md).

A job can also prove its own identity to other systems with an OIDC token
that Reactorcide signs. Its claims include the actor principal and its kind.
See [OIDC Tokens for Jobs](./oidc.md).

### Tokens made before principals

The migration gave each existing service token and instance token a service
principal. These tokens keep their authority: their capabilities apply alone,
as before (`intersect_roles` is false). A token made later with
`reactorcide token create` works the same way. Only a token issued with
`reactorcide principals tokens create` intersects with roles. The admin token
keeps working everywhere without change.

## Groups linked to VCS teams

A group can follow one or more VCS teams (plan item B3). The members of each
linked team become members of the group after a sync. An org admin, or the
admin token, links the teams of the organization (owner decision D24). Only
GitHub teams are available now.

```bash
reactorcide groups link-team --org example platform acme/platform-eng
reactorcide groups teams --org example platform
reactorcide groups sync --org example platform
reactorcide groups members --org example platform
reactorcide groups history --org example platform
reactorcide groups unlink-team --org example platform acme/platform-eng
```

### Members

The effective members of a group are its manual members and its synced
members:

| Source | Made by | Removed by |
| --- | --- | --- |
| `manual` | `groups add-member`, the UI, or `principals add-group` | `groups remove-member` |
| `vcs_sync` | A sync of a linked team | The next sync after the person leaves the team, or `unlink-team` |

Rules:

- A sync never removes a manual member.
- A manual add of a synced member makes the member manual.
- A manual remove of a member that a linked team still has keeps the synced
  membership.
- A manual remove of a member that only a linked team gives is refused
  (`409 Conflict`). Remove the person from the team, or unlink the team.
- A member of two linked teams stays while one of the teams has the member.
- Reactorcide makes a principal for each team login that it has not seen
  (the same as for a signed webhook). A login that is linked to a user gives
  the membership to the principal of that user. A disabled principal gets no
  membership.
- A synced membership does not stop a merge of a principal that only a
  webhook made. The merge moves the membership, and it stays synced.

Synced members get everything that a group gives: the roles of the group,
the subject `reactorcide_group:NAME`, required reviews and approver lists,
the workflow owner group, and the priority shift of the group.

### Sync

- The coordinator syncs each linked team on a schedule. One coordinator
  replica runs the sync loop, under the coordinator lease `group-vcs-sync`.
- `REACTORCIDE_GROUP_VCS_SYNC_INTERVAL` sets the time between two syncs of
  one team. The default is `15m`. `0` stops the loop; then only
  `groups sync` syncs.
- A GitHub `team` or `membership` webhook makes the linked teams of the event
  due at once. The loop then syncs them in about 10 seconds. Reactorcide does
  not trust the payload for membership. It reads the team from GitHub.
- A team rename (`team` `edited`) moves the link to the new slug. The link
  keeps the GitHub team ID for this.
- `groups sync` syncs at once and shows the members, the additions, and the
  removals.

To get the webhooks, add an organization webhook in GitHub with the events
**Membership** and **Team**. An organization event has no repository, so
Reactorcide validates it with the global webhook secret
(`REACTORCIDE_VCS_GITHUB_SECRET`).

### Credentials

The first choice of the sync is a GitHub App installation on the team owner
account that the organization is entitled to: an installation that holds a
repository of a project of the organization, or any installation on an
account in the VCS owner allowlist of the organization. The App pool prefers
an installation that granted the `members` permission
([GitHub App](./github-app.md)).

Then the sync uses the VCS credential of a project of the organization with a
repository under the team owner: a GitHub App installation, a project
credential, or the organization credential. This is the same credential that
resolves `vcs_team:` subjects live for that project. The credential must be
able to read the team members (GitHub: `read:org`).

The sync uses the GLOBAL GitHub token only for an owner on the VCS owner
allowlist of the organization. Only a global admin (or the admin API token)
sets the list:

```bash
reactorcide orgs vcs-owners set acme --owner acme-corp
reactorcide orgs vcs-owners get acme
reactorcide orgs vcs-owners set acme          # clear the list
```

REST: `GET`, `PUT /api/v1/organizations/{org}/vcs-owner-allowlist` with the
body `{"owners": ["acme-corp"]}`. The change is in the audit log
(`organization.vcs_owner_allowlist`). With no credential and no allowlist
entry, the sync of the link fails with "no VCS credential of the
organization can read this team".

To link a team, the organization must have a project with a repository under
the team owner. Reason: without this rule an org admin could read the members
of the teams of any GitHub organization that the global token can see. A
project under an owner is not proof that the organization owns that owner:
any tenant can add a project with a repository URL under another owner.
Thus the global token needs the allowlist entry of a global admin.

### Failures

| Failure | Result |
| --- | --- |
| The team is not found (`404`), or the credential cannot see it | The synced members of the link are removed at once |
| Rate limit | The next attempt is after the rate limit reset |
| Other errors | The members stay. The next attempt is after a backoff: 1 minute, then double, at most 4 times the interval and at most 6 hours |
| No successful sync for `REACTORCIDE_GROUP_VCS_SYNC_MAX_STALE` (default `24h`) | The synced members of the link are removed |

The link records `last_synced_at`, `last_attempt_at`, `next_sync_at`,
`last_error`, and `failure_count`. `groups teams` and the UI show them.

### History

`group_membership_history` records each change of the effective membership:
the group, its name at that time, the principal, `added` or `removed`, the
source, and the time. A database trigger writes it, so each path (CLI, UI,
sync, merge, group delete) is recorded. The rows stay after the group is
deleted. Usage attribution uses it to find the groups of a principal at a
time.

### REST API

All routes are under `/api/v1/organizations/ORG/groups/GROUP`. A `GET` route
needs `organizations:read` or `organizations:manage` for the organization. A
change needs `organizations:manage`.

| Method and path | Action |
| --- | --- |
| `GET /teams` | List the linked teams with their sync status |
| `POST /teams` | Link `{"team": "OWNER/SLUG", "provider": "github"}` |
| `DELETE /teams/OWNER/SLUG` or `DELETE /teams/LINK_ID` | Unlink |
| `POST /sync` | Sync now: `{"results": [...]}` |
| `GET /members` | Members with `principal_id`, `source`, `manual`, `teams`, `vcs_logins` |
| `GET /history?principal=&since=&limit=` | Membership changes, newest first |

The audit log records `group.team_link`, `group.team_unlink`, and
`group.team_sync`.

### UI

The **Groups** page shows the linked teams of an open group: the member
count, the last sync, the next sync, and the last error. An org admin links,
unlinks, and syncs teams there. The member list shows the source and the
teams of each member. The page uses the CSIL operations
`list-group-vcs-links`, `link-group-vcs-team`, `unlink-group-vcs-team`,
`sync-group-vcs-teams`, and `list-group-member-details`.

## Identity links

Who can link an identity depends on the kind of the target principal:

| Target | Who can link |
| --- | --- |
| A service principal of the organization | An org admin, a global admin, or the admin token |
| A human (a user, or a VCS login) | A global admin or the admin token. The person can also prove a LinkKeys identity with a LinkKeys login. |

Reason: the subjects of a human apply in every organization. An org admin
who links a login to a human would give that human the subjects in other
organizations. This applies to `reactorcide principals link`,
`link-principal-identity`, `POST /{principal}/identities`, and the older
`POST /api/v1/vcs-identities` route.

```bash
reactorcide principals link --org example --type vcs --value github/alice PRINCIPAL_UUID
reactorcide principals link --org example --type linkkeys --value alice@example.com PRINCIPAL_UUID
reactorcide principals unlink --org example --type vcs --value github/alice PRINCIPAL_UUID
```

- An identity of a principal that only a webhook made (see
  [VCS logins](#vcs-logins)) merges into a human target.
- An identity of another principal is a conflict. Unlink it first.
- `user` and `token` identities are structural. You cannot link or unlink
  them.
- A `federated` identity comes from a registered issuer, not from a link.

A linked identity gives its subjects to the principal in every organization.
Link only identities that you verified.

## REST API

All routes are under `/api/v1/organizations/ORG/principals`. A `GET` route
needs `organizations:read` or `organizations:manage` for the organization. A
change needs `organizations:manage`. The caller must be an org admin (or a
global admin). A token issue and a token revoke also need `tokens:manage`.

| Method and path | Action |
| --- | --- |
| `GET /` | List principals (`?kind=` filters) |
| `POST /` | Create a service principal: `{"name", "display_name"}` |
| `GET /resolve?type=&value=` | Resolve an identity in the organization |
| `GET /{principal}` | Show a principal (a service name or a UUID) |
| `POST /{principal}/disable`, `POST /{principal}/enable` | Change the status of a service principal |
| `POST /{principal}/identities` | Link `{"identity_type", "identity_value"}` |
| `DELETE /{principal}/identities?type=&value=` | Unlink |
| `POST /{principal}/groups`, `DELETE /{principal}/groups/{group}` | Group membership |
| `GET`, `POST /{principal}/roles`, `DELETE /{principal}/roles/{assignment}` | Direct roles |
| `GET`, `POST /{principal}/tokens`, `DELETE /{principal}/tokens/{token}` | Tokens |

## UI

The **Principals** page (`/org/principals`, linked from the Groups page and
the Administration page) lists the principals of the organization. An org
admin creates service principals, links and unlinks identities, changes group
membership, issues and revokes tokens, and sees the subjects of each
principal. The page uses CSIL operations that authorize the caller:
`list-principals`, `get-principal`, `create-service-principal`,
`set-principal-status`, `link-principal-identity`,
`unlink-principal-identity`, `set-principal-group`, `issue-principal-token`,
`revoke-principal-token`, and `resolve-principal`.

The **Federated issuers** card on the same page registers trusted OIDC
issuers and claim mappings (`list-federation-issuers`,
`save-federation-issuer`, `delete-federation-issuer`,
`save-federation-mapping`, `delete-federation-mapping`). See
[Inbound Federation](./federation.md).

The event page shows the principal of the event actor.

## Database

Migration `000048_principals.sql`:

- Adds `principals` and `principal_identities`, and the system principal.
- Backfills one human principal for each user, one service principal for
  each service or instance token, and one human principal for each VCS login
  in `ci_events` and `ci_approvals`.
- Adds `actor_principal_id` (and `actor_identity`) to `jobs`,
  `workflow_instances`, `ci_events`, `ci_approvals`, and `audit_events`, and
  backfills them. A row with no derivable actor gets the system principal.
- Adds `api_tokens.principal_id` and `api_tokens.intersect_roles`.
- Adds `group_members.principal_id`. `group_members.user_id` stays for users
  and is now nullable. The primary key is `(group_id, principal_id)`.
- Lets `role_assignments.principal_type` be `principal`.
- Adds the triggers that keep principals in step and that set default actors.

The `-- +goose Down` section removes all of it. It deletes group members and
role assignments that only a principal can have.

Migration `000054_group_vcs_links.sql`:

- Adds `group_vcs_links`, `group_vcs_link_members`, and
  `group_membership_history`.
- Adds `group_members.source` (`manual` or `vcs_sync`). Each existing member
  is `manual`, so nothing changes for existing groups.
- Backfills one `added` history row for each existing member, at its
  `added_at`.
- Changes `principals_merge` to keep the source of each membership, and
  `principals_webhook_only` to ignore synced memberships.
- The `-- +goose Down` section removes the synced memberships, the tables,
  and the column, and restores the 000048 functions.
