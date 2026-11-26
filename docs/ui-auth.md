# UI Auth, RBAC, and Management Suite

This document covers the management UI's login system, role-based access control,
public/private visibility, and credential-rotation workflow. This doc describes the
shipped behavior.

The webapp (`webapp/`) is a thin bridge and a single-page application (SPA,
`webapp/ui/`). The **coordinator is the sole authorizer**. The bridge sends
each SPA call to the coordinator over CSIL-RPC (`POST /csil/v1/rpc`, mounted
by `coordinator_api/internal/uiapi`) with the session of the browser. The
webapp holds no coordinator API token. The coordinator validates and
authorizes each operation again. The SPA does not show controls that the
caller has no capability for. This is a UX convenience, not a security
boundary.

The webapp coordinator URL must use HTTPS. The webapp sends user sessions on
this connection. For an isolated development network, add
`--allow-insecure-transport` to the `reactorcide-web serve` command. This option
has no environment variable equivalent. Reactorcide also rejects a redirect
from HTTPS to HTTP.

## Auth modes

Set with `REACTORCIDE_UI_AUTH_MODE` on the **coordinator**. Default: `none`.

| Mode | Behavior |
|------|----------|
| `none` | No login. Everyone is an anonymous viewer. Trusted-LAN/single-tenant posture: anonymous callers may view public projects/jobs, gracefully **cancel** jobs/workflows, and **retry** them. They cannot kill jobs, manage secrets, or use an admin/management surface. No login flow runs. The only session is the optional bootstrap-admin session (see below). An instance that ran with logins does not start in this mode (see [Login history guard](#login-history-guard)). |
| `local-rp` | DNS-less LinkKeys login. The coordinator generates (or loads) its own local-RP Ed25519 identity bundle via the LinkKeys Go SDK and acts as its own minimal relying party — no separate LinkKeys RP deployment needed. Good for small/self-hosted setups. |
| `rp` | The coordinator is an app registered with a full LinkKeys RP/IDP deployment reachable over TCP CSIL-RPC. Use this when you already run (or federate with) a LinkKeys RP. |

Anonymous browsing (public projects/jobs) works the same in every mode; what changes is
whether a login flow exists at all and what an anonymous caller can do beyond viewing.

### No-login instances (single admin)

> **WARNING: not for production.** This mode is for a homelab or for an
> open source user with one administrator. Use `local-rp` or `rp` for a
> shared or production instance.

With `REACTORCIDE_UI_AUTH_MODE=none`, no user can log in. The admin API
token (`reactorcide token create --name admin`, an instance token) is the
administrator of all organizations. Tokens that you make on such an
instance get all permissions when you do not give a list:

| How you make the token | No list given | List given |
|---|---|---|
| `reactorcide token create` (writes to the database) | All capabilities and all organizations (an instance token). This is the same in all modes. | Only the given `--org` and `--capability` values. |
| `POST /api/v1/tokens` | All capabilities and all organizations. The caller must have all capabilities and all organizations. This is the same in all modes. | Only the given values, inside the authority of the caller. |
| `reactorcide principals tokens create`, REST `POST /api/v1/organizations/{org}/principals/{p}/tokens`, UI `issue-principal-token` | **No-login mode only:** all capabilities and all organizations. The token is an instance token of the principal. The roles of the principal do not narrow it (`intersect_roles` is false), so it passes each check that the admin token passes. Only a global caller (the admin token, or a global admin) can issue it. | The given capabilities in the organization of the principal, intersected with the roles of the principal (as with logins). |

The admin token and a wide service principal token (an instance token with
all capabilities and all organizations, issued on a no-login instance) can
give pull request commands in every auth mode: REST `POST /api/v1/approvals`,
UI `pr-command`, and `reactorcide approvals`. These tokens need no user. They
act as the instance administrator. A wide token keeps this after you turn on
logins. If you do not want it, revoke the token. The rules of these commands:

- The token must have `workflows:control` (a wide token has it).
- The token passes every approver check of `approve-ci`, `override-ci`,
  `retry`, and `rerun`.
- An approval records the subject `project_owner`. A global admin is the
  owner of each project. The person of the approval is the principal of the
  token (`principal:UUID`), or `token:UUID` for the admin token, which has no
  principal.
- `accept`, `promote`, and `demote` use the normal rules of required reviews
  and of the required set, with the subject `project_owner`.

With a login mode, these tokens have no person and get 403 for a pull
request command.

With a login mode (`local-rp` or `rp`), a service principal token with no
capability list has all capabilities only in the organization of the
principal, intersected with the roles of the principal. The coordinator
reads the mode when it issues the token. A token that it issued in
no-login mode keeps all permissions when you change to a login mode later.
Revoke such tokens (`reactorcide principals tokens revoke`) before you
enable logins.

### Login history guard

Mode `none` is the default. If a production instance loses its
`REACTORCIDE_UI_AUTH_MODE` setting (for example, a Helm values file or an
environment file without it), the instance would become a no-login
instance with all the no-login behaviours. The login history guard stops
this.

The no-login behaviours are:

- A service principal token with no capability list gets all capabilities
  and all organizations (the table above).
- An anonymous caller can cancel and retry public jobs and workflows.
- The admin token and wide tokens can give pull request commands.

The coordinator decides all of them with one function
(`config.NoLoginMode`). That function is true only when the mode is `none`
**and** the guard accepted that mode at startup. A process that did not run
the guard gets no no-login behaviour.

The guard uses the table `auth_login_history` (migration 000073). It holds
one row or no row:

1. At each start in mode `local-rp` or `rp`, the coordinator writes the row
   (`source=startup`, the mode, and the time).
2. Migration 000073 writes the row (`source=existing_data`) when the
   database already has data that only a login mode makes: a LinkKeys login
   identity (not the synthetic bootstrap-admin identity), a LinkKeys
   principal identity from a login, a stored login credential (the local-RP
   identity bundle or the RP API key), a trusted identity, or a trusted
   domain pattern. The row then has the mode that the stored credential
   shows, or `unknown`. Thus an instance that ran with logins before this
   version is protected at the first start of this version.
3. At a start in mode `none`, when the row exists, the coordinator does not
   start. The error names `REACTORCIDE_UI_AUTH_MODE` and
   `REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE`. When the coordinator cannot
   read the row, it does not start.

To correct the error, set `REACTORCIDE_UI_AUTH_MODE` to the login mode of the
instance.

To make an instance a no-login instance on purpose, start it one time with
`REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE=true`. The coordinator deletes
the row, logs a warning, and starts in mode `none`. Then remove the
override. Later starts in mode `none` need no override. A later start in a
login mode writes the row again.

> **CAUTION:** A no-login instance gives the admin token and wide tokens
> authority over every organization. Do not use the override on a shared
> instance. Revoke wide tokens before you enable logins again.

The webapp holds no coordinator credential. It reads the mode only to show
or hide the login link. It cannot give a permission that the coordinator
refuses.

## Environment variables

All of these are read by the **coordinator** (`coordinator_api/internal/config/config.go`,
`config_auth.go`) unless noted. Values below are placeholders — never put real secrets in
job YAML, env files checked into a repo, or command output.

| Variable | Required when | Purpose |
|----------|----------------|---------|
| `REACTORCIDE_UI_AUTH_MODE` | always (has a default) | `none` (default) \| `local-rp` \| `rp`. Misconfiguration fails fast at startup (`ValidateUIAuthMode`) rather than silently falling back to `none`. An instance that ran with logins does not start with `none` (see [Login history guard](#login-history-guard)). |
| `REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE` | optional (default `false`) | Set to `true` for one start to change an instance that ran with logins to mode `none` on purpose. The coordinator deletes the login history and logs a warning. Remove it after that start. |
| `REACTORCIDE_LOCAL_RP_NAME` | `mode=local-rp` | Display name (audit/UI metadata only, never an identity input) used when generating this coordinator's local-RP identity bundle. |
| `REACTORCIDE_LINKKEYS_RP_ADDR` | `mode=rp` | `host:port` TCP CSIL-RPC address of your LinkKeys RP server. The RP server must be LinkKeys 0.16.11 or later: the wire format changed in that release, and an older server does not decode the requests. **The coordinator dials this address itself, so it must be reachable from the coordinator, not only from a browser.** In Kubernetes, a public hostname often resolves to a load-balancer IP that pods cannot reach; use the in-cluster service address (for example `linkkeys.linkkeys-ns.svc.cluster.local:4987`) instead. The TLS pin below, not the hostname, is the trust anchor, so an internal address is just as safe. |
| `REACTORCIDE_LINKKEYS_RP_FINGERPRINTS` | `mode=rp` | Comma-separated pinned SPKI SHA-256 fingerprints for the RP server's TLS certificate (mirrors the RP's own `_linkkeys` DNS TXT record). |
| `REACTORCIDE_LINKKEYS_RP_API_KEY` | `mode=rp`, first boot only | The RP server's API key. Presented once on first boot; the coordinator persists it encrypted (Fernet, under the active master key) in `auth_credentials` (`name='rp_api_key'`) and you can drop the env var afterward — same env-or-DB convention as `REACTORCIDE_MASTER_KEYS`. |
| `REACTORCIDE_LINKKEYS_RP_APPLICATION_ID` | removed | Do not set it. LinkKeys 0.16.11 removed `application_id` from `Rp/sign-request`. The RP server names the application from the API-key account. Set the `application_id` on that LinkKeys application account (Admin `set-application-id`). The LinkKeys migration gives an existing API-key account the `application_id` of its username, so the consent of users does not change. If the variable is set, the coordinator does not start. |
| `REACTORCIDE_FIRST_ADMIN` | optional | An identity selector (`handle@domain`, `uuid@domain`, `email@domain`, or a bare `domain` to match any identity at it) that is granted global admin the *first time* it completes a real login, as long as no global admin exists yet. Safe to leave set permanently — it's a no-op once any admin exists. This gate is deliberate: a standing grant would let whoever edits the deployment configuration take over an instance that already has administrators. |
| `REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN` | optional | Enables a one-time bootstrap-admin session (see below) while zero global admins exist. Unset (default) disables the feature entirely. |
| `REACTORCIDE_TRUSTED_IDENTITIES` | `local-rp`/`rp` modes | Comma-separated `[handle@]domain` selectors seeded into the admission list at startup as `source=config` rows. A bare domain admits any handle at that domain. Global admins can add more from the UI (`source=admin`); config-seeded rows are re-applied on every restart. |
| `REACTORCIDE_UI_CALLBACK_URL` | `local-rp`/`rp` modes | **The web UI's public base URL** (the origin browsers reach the webapp on, e.g. `https://ci.example.com`) — *not* the coordinator's own URL. The LinkKeys login callback is built as `REACTORCIDE_UI_CALLBACK_URL + "/app/auth/callback"`. This is fixed, coordinator-side config; the CSIL `begin-login` op has no per-request callback-url field, so a malicious caller can't redirect a login token elsewhere. |
| `REACTORCIDE_CANCEL_GRACE_SECONDS` | optional (default `60`) | How long a graceful job cancel waits between sending the stop signal and the worker force-cleaning up. Not used for kill (force-kill skips the grace period). |
| `REACTORCIDE_WEB_COOKIE_INSECURE` | optional, **webapp**-side | Disables the `Secure` flag on the session cookie. Only set this for local plaintext-HTTP development; leave unset (default) for any real deployment, where the cookie must stay `Secure`. |

For both LinkKeys modes, Reactorcide reads the `_linkkeys_apis` TXT record for
the identity domain. It uses the published `https` endpoint for the browser
login route. If the record does not contain an HTTPS endpoint, Reactorcide uses
the identity domain. The LinkKeys local-RP SDK does this discovery, so both
modes resolve the endpoint the same way.

If the selector holds a handle (`alice@example.com`), Reactorcide adds
`username=alice` to the browser login URL. The LinkKeys IDP uses this value
only to fill the Username field. It is not identity or authentication
evidence, and the user must still authenticate. This parameter replaced the
older `user_hint`.

## Login troubleshooting

The coordinator logs the cause of every login failure it cannot classify, at
`error` level, with the message `login failed while beginning login` or
`login failed while completing login`. The browser only ever sees a generic
message, so read the coordinator log first. Common causes:

- `dial RP: ... connection refused` — `REACTORCIDE_LINKKEYS_RP_ADDR` is not
  reachable from the coordinator. See the note on that variable above.
- `certificate fingerprint ... does not match any pinned RP fingerprint` —
  `REACTORCIDE_LINKKEYS_RP_FINGERPRINTS` is stale. Re-read the RP domain's
  `_linkkeys` TXT record.
- `Rp/sign-request: ...` with an auth status — the stored RP API key is wrong
  or lacks the `api_access` relation on the RP server.

## First admin and bootstrap-admin

You need *some* way to get your first global admin, since global admin is what grants
every other management capability (creating projects, managing secrets, assigning roles,
editing trusted identities...).

**First admin** (`REACTORCIDE_FIRST_ADMIN`): set this to the login identity of whoever
should become the first admin, then have them log in normally through LinkKeys. The instant
their login completes — and only if no global admin exists yet — they're granted the
`global/admin` role. That person then grants further admins from the Roles page. Once any
global admin exists the variable is inert, by design: administrators are managed inside the
system by the administrators it has, not by the deployment configuration. This is the
normal path for `local-rp`/`rp` deployments.

The selector is matched against the verified identity's handle, its subject and its domain,
and a `local@domain` selector also matches the identity's `email` claim exactly. Every
comparison ignores case. In `rp` mode the subject is a uuid, so a `handle@domain` selector
depends on the handle claim arriving. The coordinator therefore asks for the `handle` claim
(required) plus `display_name` and `email` (optional) on every `rp` sign-request, the same
set the local-RP SDK requests by default. Before it did, the RP server's own default applied,
which can be no claims at all; every login then verified with an empty handle and the
selector could never match. The first login for the Reactorcide audience shows a consent
screen for those claims; later logins are silent.

On every completed login while the selector is set, one Info log line says what happened:
did not match (with the handle, domain, subject and email that arrived), already an admin,
or granted. A failed userinfo fetch is logged at Warn. If `/app/auth/config` reports
`has_global_admin: false` after the intended person has signed in, read those lines.

If the handle still does not arrive, the selector accepts `uuid@domain`: the uuid is the
`verified_subject` in that log line.

**Bootstrap admin** (`REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN`): for initial setup *before* login
is fully wired up (or in `mode=none` deployments that still want one admin session to do
initial configuration), set this token and visit `/app/bootstrap` in the webapp. Submitting
the correct token mints a session for a synthetic `bootstrap-admin` user and grants it
global admin, but **only while zero global admins exist**. Once any admin exists, the token
stops working — every failure mode (wrong token, feature not configured, admin already
exists) returns the same generic error, so there's no oracle for probing whether bootstrap
is even enabled. Treat the token as a secret: pass it via your secrets manager /
`REACTORCIDE_SECRETS_PASSWORD`-gated tooling, never paste it into chat, logs, or a checked-in
file. Consider unsetting `REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN` after your first real admin is
established.

## Trusted identities and domain patterns

Once a login mode is enabled, only *admitted* identities are allowed to complete a login —
everyone else's login attempt fails with "not admitted," even if they otherwise authenticate
correctly against LinkKeys. Admission has two independent mechanisms, and a login is
admitted if it matches *either*:

- **Exact trusted identities** (`auth_trusted_identities`): a `(domain, handle)` pair, or a
  bare-domain wildcard row (`handle=''`) that admits any handle at that domain. Seed these
  from `REACTORCIDE_TRUSTED_IDENTITIES` at startup (`source=config`), or manage them from
  `/app/admin` as a global admin (`source=admin`).
- **Trusted domain patterns** (`auth_trusted_domain_patterns`): RE2 regular expressions
  (validated at write time — an invalid pattern is rejected outright, not silently ignored)
  matched against the login's domain. Manage these from `/app/admin` as well. **Matching is
  always against the FULL domain string** — every pattern is implicitly wrapped as
  `^(?:pattern)$` before it's used, whether or not you wrote your own `^`/`$` anchors. This
  matters for a security boundary like login admission: an unanchored pattern such as
  `example\.com` admits *only* the domain `example.com`, never `example.com.attacker.example`
  (a suffix match) or `notexample.com` (a prefix match) — Go's default `regexp.MatchString`
  substring semantics do not apply here. Write patterns as if they must match the entire
  domain end to end (e.g. `.*\.example\.com` to admit any subdomain of `example.com`).

Both checks happen twice per login: once as a cheap pre-check on the identity selector the
browser requested, and again — authoritatively — on the *verified* identity LinkKeys
actually returned, since those aren't guaranteed to match until the protocol says so.

## Roles and the permission matrix

Reactorcide has first-class organizations. An organization owns its projects,
jobs, workflows, secrets, worker classes, queues, and policy records. Users are
principals. They are not organizations. Roles are assigned through
`role_assignments(principal_type, principal_id, scope_type, scope_id, role)`,
where:

- `principal_type` is `user`, `group` (groups are `org_id`-scoped collections of users), or
  `principal` (any principal, for example a service principal; see [Principals](./principals.md)).
- `scope_type` is `global`, `org`, or `project`.
- `role` is `admin`, `owner`, or `member`.

Effective permission is the union of a user's own direct role assignments and every group
they belong to. Global admin implies org admin everywhere; org admin of a project's owning
org implies project ownership of every project in that org.

| Capability | anon (`mode=none`) | anon (login mode) | member | project owner | org admin | global admin |
|---|---|---|---|---|---|---|
| view public orgs/projects/jobs | yes | yes | yes | yes | yes | yes |
| view private (scoped) | no | no | if assigned | yes | org-wide | yes |
| cancel job/workflow (graceful, cleanup runs) | **yes** | no | no | yes (proj) | yes (org) | yes |
| kill job (force) | no | no | no | no | yes (org) | yes |
| retry job/workflow/unsuccessful jobs | **yes** | no | no | yes (proj) | yes (org) | yes |
| create project | no | no | no | no | yes (in org) | yes (global) |
| delete project | no | no | no | no | yes (org) | yes |
| delete organization with an administered replacement | no | no | no | no | yes (both orgs) | yes |
| webhook secret add/rotate/deactivate | no | no | no | no | yes (org) | yes |
| vcs credential add/rotate | no | no | no | no | yes (org) | yes |
| set secrets (write-only) + secret grants | no | no | no | no | yes (org) | yes |
| manage groups / assign roles | no | no | no | no | yes (org) | yes |
| project settings (visibility, defaults) | no | no | no | yes | yes | yes |
| trusted users/domain-regexes, global settings | no | no | no | no | no | yes |

A few notes that trip people up:

- **Cancel and retry are special-cased for anonymous callers in `mode=none` only.** This is
  a trusted-LAN convenience (stop or re-run a job from the shared jobs list without needing
  an account) — it doesn't apply once any login mode is enabled, and it never applies to
  kill. It also does not apply when the login history guard refused mode `none` (see
  [Login history guard](#login-history-guard)).
- **Retry is the same permission tier as cancel, not kill.** `authz.Caps.Retry` is computed
  identically to `authz.Caps.Cancel` (see `internal/authz/capabilities.go`) and exposed as
  its own `retry_job` capability bit so the UI can gate the retry button independently, but
  the underlying authorization rule — project owner of the job's project, org admin of the
  owning org, global admin, or anonymous in `mode=none` — is identical to cancel's.
- **Secrets are write-only through the UI.** Set, delete, and list-*paths*-and-keys are the
  only operations; there's no "get secret value" op anywhere in the management surface, by
  design.
- The coordinator enforces every row of this table; the webapp only *mirrors* it (via
  `get-capabilities`) to decide what to render. Don't rely on hidden buttons for security —
  they're a UX affordance.
- **Capabilities are scoped, and the UI asks at the right scope.** `GET /app/auth/session`
  carries the GLOBAL-scope set, which is all-false for anyone but a global admin, plus the
  caller's `roles`. Job, workflow and project pages call `get-capabilities` with
  `project_id`; the coordinator resolves the project's owning organization and answers for
  the caller's roles there. Nav links for Workers, Access and Secrets show when the caller
  is a global admin or holds `admin` on some organization; each of those pages then has an
  organization picker. A user's own id is not an organization.
- **Finding a user id.** `list-users` (org admin of the named `org_id`, or global admin)
  returns the accounts the coordinator knows, with username, display name and the sign-in
  subject. An account appears after its first login. The role and group forms in the UI use
  it as a picker, so nobody has to read a UUID out of the database.
- **Cancel, kill and retry answer `not_found` for a job or workflow the caller cannot see**,
  the same as `get-job`. This includes the anonymous `mode=none` grant: it reaches public
  jobs only. `get-job-logs` and `get-job-metrics` behave the same way.

## Sessions and API tokens

The UI service (CSIL-RPC) accepts a browser session or an API token for each
management operation. A browser is not a special client. The API enforces
each permission. The webapp only shows or hides controls.

For an API token, the coordinator applies three checks:

1. The owner of the token (a user, or a service principal) must have the
   role that the table above gives. An instance token with no owner uses its
   capabilities only.
2. The token must cover the organization of the resource. An operation
   without an organization (a global pool, a queue, a global setting) needs
   a token for all organizations.
3. The token must have the capability of the operation:

| Operations | Token capability |
| --- | --- |
| `list-workers`, `list-pools`, `list-queues`, `list-worker-classes`, `get-worker-image-state` (and the pool detail of `get-image-rollout-status`) | `workers:read` or `workers:manage`. A read with no organization (`list-queues`, global pools and their workers) also accepts `organizations:manage`, with all organizations. |
| `set-worker-status`, `drain-worker`, pool, queue, and worker class changes, enrollment tokens (`create`, `list`, `deactivate`) | `workers:manage`. A change of a global pool or a queue also accepts `organizations:manage`, with all organizations. `set-worker-class-pool` with a global pool needs `workers:manage` and `organizations:manage`, with all organizations. |
| `get-shed-state` | `organizations:read` |
| Reads of groups, group members, group VCS links, role assignments, and principals; `list-users` | `organizations:read` or `organizations:manage` (also in the REST group, workflow role, and principal routes) |
| Changes of groups, group members, group VCS links, role assignments, and principals; `delete-org`, `set-shed-level`, `set-shed-actions` | `organizations:manage` |
| `issue-principal-token`, `revoke-principal-token` | `organizations:manage` and `tokens:manage`. The requested capabilities must be in the token; a request with no capability list needs a token with all capabilities. On a no-login instance, a request with no capability list also needs a global caller (`organizations:manage`, with all organizations). |
| `link-principal-identity` for a human principal | `organizations:manage`, with all organizations, and a global admin |
| `set-group-priority-shift` | `policies:manage` or `organizations:manage` |
| `create-project` | `projects:create` |
| `update-project`, `delete-project` | `projects:manage`. A change of `default_job_command` also needs a global admin and a token with `projects:manage` for all organizations. |
| Webhook secrets, VCS credentials, secrets, secret grants | `secrets:manage` |
| Execution profiles, `put-image-policy` | `policies:manage` |
| `get-image-policy` | `organizations:read` or `policies:manage` |
| Environment defaults | the read or manage capability of the scope: `organizations:*`, `workers:*` (worker class), or `projects:*` (project) |
| Image aliases: `list-image-aliases`, `get-image-alias`, `get-image-rollout-status` | `organizations:read` or `policies:manage` |
| Image aliases: `set-image-alias`, `delete-image-alias` | `policies:manage` |
| Schedules: `list-schedules`, `list-schedule-fires` | `projects:read` or `projects:manage` |
| Schedules: `create-schedule`, `update-schedule`, `delete-schedule` | `projects:manage` |
| Ref class rules and priority lanes: read and explain | `projects:read` or `policies:manage` |
| Ref class rules and priority lanes: change | `policies:manage` |
| Outbound event subscriptions | `organizations:manage` (organization scope), `projects:manage` (project scope) |
| OIDC settings | `organizations:read` or `organizations:manage` to read, `organizations:manage` to change |
| Alert rules | `organizations:manage` (organization rule), `projects:manage` (project rule); `projects:read` to see organization rules |
| CI trust settings | `policies:manage` (and the role) |
| CI policy: `get-ci-policy`, `put-ci-policy`, `delete-ci-policy` | `policies:manage` |
| `pr-command` | `workflows:control` for the organization of the project. The person is the user of a session or a user token, or the service principal of a service principal token. Also the admin token and wide tokens, in every auth mode (see [No-login instances](#no-login-instances-single-admin)). The subject rules of the command then decide. |
| `cancel-job`, `cancel-workflow` | `jobs:cancel` or `workflows:control` |
| `kill-job` | `jobs:cancel` |
| `retry-job`, `retry-workflow`, `retry-unsuccessful-jobs`, `rerun-workflow` | `jobs:retry` or `workflows:control` |
| `start-run` | `jobs:retry` or `workflows:control`, and `jobs:submit` |
| Required set: `get-required-set` | `projects:read` |
| Required set: `set-required-set-enabled`, `set-required-set-approvers`, `decide-required-set-proposal`, `quarantine-required-workflow`, `unquarantine-required-workflow` | `projects:read` and `policies:manage` |
| Required set: `propose-required-set-change` | `projects:read` and `policies:manage`. An owner of the workflow needs no capability. |
| Reporting: `get-reporting-settings`, `get-agreement-report`, `list-path-acceptances`, `get-org-reporting-settings` | `projects:read` |
| Reporting: `update-reporting-settings` | Report fields: `policies:manage` (organization admin) or `projects:manage` (project owner). Ownership fields: `policies:manage`. |
| Reporting: `put-org-reporting-settings`, `decide-path-acceptance` | `policies:manage` |
| Reporting: `request-path-acceptance` | `policies:manage`. An owner of the workflow needs no capability. |
| Tests: `list-flaky-tests`, `list-test-quarantines` | `projects:read` |
| Tests: `quarantine-test`, `unquarantine-test` | `projects:manage` (for a service principal token, its roles only) |
| `put-diagnosis` | `diagnosis:write` (a job token cannot) |
| Federation issuers: `list-federation-issuers`, `save-federation-issuer`, `delete-federation-issuer` | `organizations:manage` (also to list) |
| Federation mappings: `save-federation-mapping`, `delete-federation-mapping` | `organizations:manage` and `tokens:manage`. The mapping cannot give a capability that the token does not have. |
| `list-orgs` | None for the organizations of the caller. The list of all organizations needs `organizations:read` or `organizations:manage`, with all organizations. |
| Required set settings of an organization | `projects:read` or `policies:manage` to read, `policies:manage` to change |
| `list-jobs`, `list-workflows`, job and workflow reads | A private resource needs `projects:read`, `jobs:read`, or `workflows:read` for its organization (`list-jobs` needs `jobs:read`, `list-workflows` needs `workflows:read`), and the lists show only the organizations of the token. Without it, a token sees only public resources. The member jobs of `get-workflow` need `jobs:read`. |
| Global admin reads (global settings, trusted identities and domain patterns, GitHub Apps, VCS owner allowlist, OIDC keys) | `organizations:read` or `organizations:manage`, with all organizations |
| Global admin changes (the same resources, and organizations) | `organizations:manage`, with all organizations |

These operations check no token capability. The role and the visibility of
the resource decide. A private resource needs `projects:read`, `jobs:read`,
or `workflows:read`: `get-pr-approvals`, `list-projects`, `get-project`,
`get-event`, `get-job-logs`, `get-job-metrics`, `get-job-resource-history`,
`list-job-links`, `list-job-artifacts`, `read-job-artifact`,
`get-job-test-results`, `get-job-provenance`, `list-diagnoses`,
`delete-diagnosis` (the author only), the dashboards
(`get-pipeline-dashboard`, `get-infra-dashboard`, `get-usage-dashboard`), and
`get-capabilities`. `describe-form-metadata` needs no identity.

A token authenticates as its principal (a user or a service principal).
Its authority is the permissions of that principal. When the token was
issued with a capability list or an organization list, the token is
narrowed to them. A token issued with all capabilities and all
organizations has the full permissions of its principal.

A read operation accepts the read capability (for example
`organizations:read`). A change needs the manage capability. The role that
the operation needs is the same for both.

A token never exceeds its own capabilities. The role checks of the
coordinator (organization admin, project owner, global admin) apply to the
owner of a token. Each operation also checks the capability of the token.
The role flags of `get-capabilities` (`is_org_admin` and the other flags)
are also limited by the token.

To see workers gives no control of them. `workers:read` lists workers,
pools, and queues. It cannot change a worker.

The enrollment token list gives metadata only (ID, name, state, times, and
worker count). It never gives a token value or a hash.

### The event stream

The UI event stream (`GET /api/v1/ui/stream`, a WebSocket) accepts:

- No `Authorization` header: an anonymous caller. It gets the events of
  public resources only.
- `Authorization: Bearer <session>`: a browser session. The webapp proxy
  (`/app/ws`) sends the session of the browser from its HttpOnly
  `SameSite=Lax` cookie.
- `Authorization: Bearer <API token>`: the same token resolution as REST.

A bearer token that is not a valid session and not a valid API token gets
HTTP 401 at the handshake.

The webapp proxy (`/app/ws`) accepts the browser upgrade only when the
`Origin` header names the host that serves the request (the rule of the
CSIL-RPC bridge). Another site, and a sibling subdomain, get HTTP 403. A
request without `Origin` is not a browser: the proxy accepts it only when it
has no session cookie. Reason: a page of another site must not open a socket
with the session cookie of the user.

When the coordinator refuses the session of the browser (an expired or
revoked session), the proxy connects again without a credential. The browser
then gets the anonymous stream (public events), as a logged-out browser. The
UI finds the expired session through its normal calls.

The coordinator filters each event for the caller. A token gets an event
only when the token covers the organization of the event and has the read
capability of the event type: `jobs:read` (job events, metrics),
`logs:read` (log events), `workflows:read` (workflow events),
`projects:read` (project events). The role of the token owner then applies,
as for a session. Thus a token never gets an event that it cannot read by
API.

The login, logout, and bootstrap operations of the auth service work on a
browser session only. They make or remove the session itself.

## Public/private visibility

- `projects.is_private` and `organizations.is_private` both default to
  `false`. Reactorcide is public by default.
- A project is *effectively* private if `project.is_private` **or** its owning org
  (`organizations.is_private`) is private.
- Jobs and workflows that belong to a project inherit that project's visibility. Jobs and
  workflows with no project (raw API-submitted jobs) fall back to their owning org's
  visibility.
- The global setting `new_projects_private` (default `false`) controls what a *newly
  created* project's `is_private` defaults to when the create-project request doesn't
  specify one explicitly. Global admins change it from `/app/admin` (or the CSIL
  `update-global-settings` op) — it doesn't retroactively change any existing project.
- List/get/logs endpoints — both the legacy REST API and the CSIL UI service — apply this
  filtering consistently for the caller's resolved identity (including anonymous).
- The UI's project page has an **Access** card for a project's own role assignments
  (`member`, `owner`, `admin` at project scope) and the org pages manage org-scoped roles.
  Both are visible to org admins of the owning organization and to global admins.

## Credential rotation: webhook secrets and VCS credentials

Both project webhook secrets and VCS credentials support N-per-`(project, provider)` rows
so you can add a new credential, confirm it's actually being used, and only then retire the
old one — no "flag day" cutover, no window where a webhook provider's requests bounce.

**Rotation workflow** (identical shape for both — from `/app/projects/{id}` → Security tab,
or the equivalent `add-webhook-secret`/`add-vcs-credential` CSIL ops):

1. **Add** a new credential with a fresh, unique `name`. It becomes active immediately
   alongside any existing active credential(s) for that `(project, provider)`.
2. **Watch `last_used_at`.** Incoming webhooks try every active secret, newest first, until
   one validates the signature, and stamp `last_used_at` on whichever row actually matched.
   VCS credential resolution for outbound calls (status updates, etc.) prefers the
   highest-precedence active row the same way. Once you see the new row's `last_used_at`
   move (and, for webhooks, once you've updated the value configured on the VCS
   provider's side to match), you know the new credential is live.
3. **Deactivate** the old row once you're confident the new one is doing the job.
   Deactivated rows are excluded from every future verification attempt — a replayed
   signature computed with a deactivated secret can never validate again — but the row (and
   its `last_used_at` history) sticks around for audit until you explicitly **delete** it.
   Deactivating is idempotent: retrying (a double-click, a client retry after a dropped
   response) on a row that's already inactive succeeds as a no-op and leaves its original
   `deactivated_at` untouched — it doesn't surface as an error. Only a row that's been
   deleted (or never existed) reports not-found.

Legacy single-value `webhook_secret`/`vcs_token_secret` project columns (and the org/env
fallbacks below them) remain as a fallback of last resort in the resolution order, so
existing deployments that predate rotation keep working unchanged.

**Tiers are mutually exclusive.** Webhook signature verification resolves candidates in three
tiers — project (all active rotation rows plus the legacy project-level ref), org (the
project owner's org-level ref), then global/env — and only the *first non-empty tier* is
ever tried. Within that tier, every candidate is tried (that's what makes rotation work), but
once a project has its own dedicated secret(s) configured, the org-level and global/env
secrets are never consulted for that project's webhooks, even if they're also configured.
This is deliberate: it prevents a project that opted into a dedicated secret from also
silently accepting a signature computed with a broadly-shared org or global secret, which
would otherwise let anyone who knows that shared secret forge webhooks for the project. The
same tiering applies to outbound VCS credential resolution.

## Cancel vs kill

| | Cancel | Kill |
|---|---|---|
| Who can | project owner+ (or anonymous, `mode=none` only) | org admin+ only |
| Signal | graceful stop (SIGTERM equivalent), grace period `REACTORCIDE_CANCEL_GRACE_SECONDS` | immediate forced removal, no grace period |
| Cleanup hooks | run (runnerlib's `CLEANUP`/`ON_ERROR` plugin phases, including `cleanup_vcs_auth`) | **not** guaranteed |
| Job status flow | `running` → `cancelling` → `cancelled` (worker drives this via its heartbeat/cancel-poll) | `running` → `cancelling` (kill-marked) → `cancelled` immediately on the next poll |
| Not-yet-started jobs (`submitted`/`queued`) | cancelled immediately either way — there's no container to stop |

A workflow cancel cascades: every non-terminal node's job gets the same graceful-cancel
treatment, and any node that hadn't submitted a job yet (still pending/waiting) is marked
cancelled directly. There's no workflow-level kill — kill is a per-job admin action.

## Retry semantics

Only a job that is `failed`, `cancelled`, or `timeout` (`models.Job.IsRetryable`), or a
workflow instance that is `failed` or `cancelled` (`models.WorkflowInstance.IsRetryable` — a
workflow instance's own status is never `timeout`; a timed-out node folds into the workflow's
aggregate `failed` status instead), can be retried; retrying anything else (including
`cancelling`) is refused with a `conflict` error. REST refuses it with HTTP 400 and the error
code `not_retryable`. The message gives the states that can be retried and the current status,
for example `only a failed, cancelled, or timed-out job can be retried; job ID is completed`.
A re-run (`rerun-workflow`) needs a finished workflow; its refusal message is similar. There are three retry shapes, each its own
CSIL op (`retry-job`, `retry-workflow`, `retry-unsuccessful-jobs` on `ReactorcideUi`) and its
own button in the webapp:

- **Job retry stays in the workflow.** `retry-job` clones the failed/cancelled/timeout job's
  full spec into a brand-new job row (fresh id, `submitted` status, every execution field —
  timestamps, exit code, logs, etc. — zeroed) and resubmits it. If the original job belonged
  to a workflow node, the *same* node is rebound to the new job and the workflow instance's
  status is forced back to `running` so the UI stops showing it as failed/cancelled/timeout
  while the retry is in flight. The job detail page's **Retry** button drives this.
- **Workflow retry is a fresh instance.** `retry-workflow` creates an entirely new
  `WorkflowInstance` (new id, new nodes, new jobs) copying the old instance's definition
  (name, VCS repo/PR/commit, queue, etc.) and evaluates it from scratch, exactly like a brand
  new trigger-created workflow. The old instance, its nodes, and its jobs are left completely
  untouched for history/audit — this is a new run, not a mutation. The workflow detail page's
  **Retry workflow** button redirects to the *new* workflow's detail page, since the button's
  own page still describes the old (untouched) instance.
- **Retry-all = failed + cancelled + timeout members, and the nodes they
  skipped.** `retry-unsuccessful-jobs` retries each failed, cancelled, or
  timed-out member job in place. It does not make a new workflow instance.
  It also sets these nodes to pending: each node that is skipped because of a
  dependency that is not successful (or that an earlier retry made pass), and
  each finished node downstream. A node that is skipped by its own `if:` stays
  skipped. The workflow then evaluates the pending nodes again. A failed
  individual retry does not stop the batch. A workflow with no node to retry
  gets the error `conflict`. The **Retry unsuccessful** button on the workflow
  detail page shows when `get-workflow` sets `retry_unsuccessful_available`.
  This can also be true for a `success` workflow.

Retried jobs update their PR comment/commit-status row in place rather than posting a new
one. See "Retry and VCS report updates" below.

## Retry and VCS report updates

The database stores one report target for a pull request. Workflow and policy
updates write structured report entries. A reconciler merges those entries
into one Reactorcide pull-request comment. The root marker is
`<!-- reactorcide:report:v1 -->`.

A job retry creates a new job and rebinds the same workflow node to it. The
report entry keeps the stable workflow ID and node identity. The next worker
start or completion update replaces the displayed state. It does not add a
second workflow section.

A workflow retry creates a new workflow instance. The old instance remains for
history. The new instance writes current state to the report for its target.

There can be a short delay after a retry submission. The report changes when
the worker starts or completes the retried work. The in-process reconciler
checks dirty report targets every five seconds. It uses a database lock so two
coordinators do not write the same comment at the same time.

The `Reactorcide Checks` commit status is separate from the shared comment. A
CI file that the policy refuses fails it. Safe trusted-base workflows continue
after a policy refusal. Required reviews post their own status, `Reactorcide
Reviews`.

## API Credential Subjects

An instance token can have global authority. A service token has an owner or an
explicit organization set. A user token delegates to one active user. A job
token has one parent job and the `jobs:submit` capability.

Only a job token can submit triggers (`POST /api/v1/jobs/{id}/triggers`), and
only for its own job while that job is `scheduling` or `running`. A child job
gets the event, the ref classes, and the trusted CI authority of its parent.
Thus no other credential can add jobs to a parent: not a user session, not a
service token, and not the admin token. A finished job cannot add jobs.

The coordinator checks organization scope before it checks a project or job
capability. For a user token, it intersects the token scope with current user
roles on each request. A disabled user cannot use a delegated token.

A token issued for a service principal (`reactorcide principals tokens
create`) works like a user token: its scope is its capabilities intersected
with the current roles and groups of the principal. A disabled service
principal cannot use its tokens. Role assignments accept the principal type
`principal` for any principal. See [Principals](./principals.md).

## See also

- `docs/vcs-credentials-and-secret-grants.md` — the underlying secret-grant model that
  authorizes which secrets a job can resolve at runtime (a different, job-execution-time
  concern from the management-UI secrets surface described above).
- `docs/security-model.md` — the broader untrusted-code / secret-isolation security model.
- `docs/organizations-and-ci-policy.md` — organization scope, token
  capabilities, CI policy, approvals, and VCS reports.
- `helm_chart/DEPLOYMENT.md` — how these env vars map to Helm values for a Kubernetes
  deployment.
