# UI Auth, RBAC, and Management Suite

This document covers the management UI's login system, role-based access control,
public/private visibility, and credential-rotation workflow. It's the operator-facing
companion to `UI_AUTH_PLAN.md` (the implementation plan/architecture record); this doc
describes the shipped behavior, not the build history.

The webapp (`webapp/`) is a thin, server-rendered client: it renders pages and forms, but
the **coordinator is the sole authorizer**. Every management action the webapp's forms
trigger is re-validated and re-authorized on the coordinator side over CSIL-RPC
(`POST /csil/v1/rpc`, mounted by `coordinator_api/internal/uiapi`). Buttons and sections the
caller lacks capability for are simply not rendered, but that's a UX nicety, not a security
boundary.

## Auth modes

Set with `REACTORCIDE_UI_AUTH_MODE` on the **coordinator**. Default: `none`.

| Mode | Behavior |
|------|----------|
| `none` | No login. Everyone is an anonymous viewer. Trusted-LAN/single-tenant posture: anonymous callers may view public projects/jobs and gracefully **cancel** jobs/workflows, but can't kill jobs, manage secrets, or touch any admin/management surface. No session machinery runs. |
| `local-rp` | DNS-less LinkKeys login. The coordinator generates (or loads) its own local-RP Ed25519 identity bundle via the LinkKeys Go SDK and acts as its own minimal relying party — no separate LinkKeys RP deployment needed. Good for small/self-hosted setups. |
| `rp` | The coordinator is an app registered with a full LinkKeys RP/IDP deployment reachable over TCP CSIL-RPC. Use this when you already run (or federate with) a LinkKeys RP. |

Anonymous browsing (public projects/jobs) works the same in every mode; what changes is
whether a login flow exists at all and what an anonymous caller can do beyond viewing.

## Environment variables

All of these are read by the **coordinator** (`coordinator_api/internal/config/config.go`,
`config_auth.go`) unless noted. Values below are placeholders — never put real secrets in
job YAML, env files checked into a repo, or command output.

| Variable | Required when | Purpose |
|----------|----------------|---------|
| `REACTORCIDE_UI_AUTH_MODE` | always (has a default) | `none` (default) \| `local-rp` \| `rp`. Misconfiguration fails fast at startup (`ValidateUIAuthMode`) rather than silently falling back to `none`. |
| `REACTORCIDE_LOCAL_RP_NAME` | `mode=local-rp` | Display name (audit/UI metadata only, never an identity input) used when generating this coordinator's local-RP identity bundle. |
| `REACTORCIDE_LINKKEYS_RP_ADDR` | `mode=rp` | `host:port` TCP CSIL-RPC address of your LinkKeys RP server. |
| `REACTORCIDE_LINKKEYS_RP_FINGERPRINTS` | `mode=rp` | Comma-separated pinned SPKI SHA-256 fingerprints for the RP server's TLS certificate (mirrors the RP's own `_linkkeys` DNS TXT record). |
| `REACTORCIDE_LINKKEYS_RP_API_KEY` | `mode=rp`, first boot only | The RP server's API key. Presented once on first boot; the coordinator persists it encrypted (Fernet, under the active master key) in `auth_credentials` (`name='rp_api_key'`) and you can drop the env var afterward — same env-or-DB convention as `REACTORCIDE_MASTER_KEYS`. |
| `REACTORCIDE_FIRST_ADMIN` | optional | An identity selector (`handle@domain` or `uuid@domain`, or a bare `domain` to match any handle at it) that is granted global admin the *first time* it completes a real login, as long as no global admin exists yet. Safe to leave set permanently — it's a no-op once an admin exists. |
| `REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN` | optional | Enables a one-time bootstrap-admin session (see below) while zero global admins exist. Unset (default) disables the feature entirely. |
| `REACTORCIDE_TRUSTED_IDENTITIES` | `local-rp`/`rp` modes | Comma-separated `[handle@]domain` selectors seeded into the admission list at startup as `source=config` rows. A bare domain admits any handle at that domain. Global admins can add more from the UI (`source=admin`); config-seeded rows are re-applied on every restart. |
| `REACTORCIDE_UI_CALLBACK_URL` | `local-rp`/`rp` modes | **The web UI's public base URL** (the origin browsers reach the webapp on, e.g. `https://ci.example.com`) — *not* the coordinator's own URL. The LinkKeys login callback is built as `REACTORCIDE_UI_CALLBACK_URL + "/app/auth/callback"`. This is fixed, coordinator-side config; the CSIL `begin-login` op has no per-request callback-url field, so a malicious caller can't redirect a login token elsewhere. |
| `REACTORCIDE_CANCEL_GRACE_SECONDS` | optional (default `60`) | How long a graceful job cancel waits between sending the stop signal and the worker force-cleaning up. Not used for kill (force-kill skips the grace period). |
| `REACTORCIDE_WEB_COOKIE_INSECURE` | optional, **webapp**-side | Disables the `Secure` flag on the session cookie. Only set this for local plaintext-HTTP development; leave unset (default) for any real deployment, where the cookie must stay `Secure`. |

## First admin and bootstrap-admin

You need *some* way to get your first global admin, since global admin is what grants
every other management capability (creating projects, managing secrets, assigning roles,
editing trusted identities...).

**First admin** (`REACTORCIDE_FIRST_ADMIN`): set this to the identity selector of whoever
should become the first admin, then have them log in normally through LinkKeys. The instant
their login completes — and only if no global admin exists yet — they're granted the
`global/admin` role. This is the normal path for `local-rp`/`rp` deployments.

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

Reactorcide doesn't have a first-class "orgs" table yet — every `users` row doubles as its
own org (`user_id` **is** the org id everywhere). Roles are assigned via
`role_assignments(principal_type, principal_id, scope_type, scope_id, role)`, where:

- `principal_type` is `user` or `group` (groups are `org_id`-scoped collections of users).
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
  kill.
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

## Public/private visibility

- `projects.is_private` and `users.is_private` (an org's own privacy flag) both default to
  `false` — Reactorcide is public/open-source-friendly by default.
- A project is *effectively* private if `project.is_private` **or** its owning org
  (`users.is_private`) is private.
- Jobs and workflows that belong to a project inherit that project's visibility. Jobs and
  workflows with no project (raw API-submitted jobs) fall back to their owning org's
  visibility.
- The global setting `new_projects_private` (default `false`) controls what a *newly
  created* project's `is_private` defaults to when the create-project request doesn't
  specify one explicitly. Global admins change it from `/app/admin` (or the CSIL
  `update-global-settings` op) — it doesn't retroactively change any existing project.
- List/get/logs endpoints — both the legacy REST API and the CSIL UI service — apply this
  filtering consistently for the caller's resolved identity (including anonymous).

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

Only a job or workflow instance that is `failed` or `cancelled` can be retried
(`models.Job.IsRetryable`/`models.WorkflowInstance.IsRetryable`); retrying anything else
(including `cancelling`) is refused with a `conflict` error. There are three retry shapes,
each its own CSIL op (`retry-job`, `retry-workflow`, `retry-unsuccessful-jobs` on
`ReactorcideUi`) and its own button in the webapp:

- **Job retry stays in the workflow.** `retry-job` clones the failed/cancelled job's full
  spec into a brand-new job row (fresh id, `submitted` status, every execution field —
  timestamps, exit code, logs, etc. — zeroed) and resubmits it. If the original job belonged
  to a workflow node, the *same* node is rebound to the new job and the workflow instance's
  status is forced back to `running` so the UI stops showing it as failed/cancelled while the
  retry is in flight. The job detail page's **Retry** button drives this.
- **Workflow retry is a fresh instance.** `retry-workflow` creates an entirely new
  `WorkflowInstance` (new id, new nodes, new jobs) copying the old instance's definition
  (name, VCS repo/PR/commit, queue, etc.) and evaluates it from scratch, exactly like a brand
  new trigger-created workflow. The old instance, its nodes, and its jobs are left completely
  untouched for history/audit — this is a new run, not a mutation. The workflow detail page's
  **Retry workflow** button redirects to the *new* workflow's detail page, since the button's
  own page still describes the old (untouched) instance.
- **Retry-all = failed + cancelled members.** `retry-unsuccessful-jobs` applies the same
  in-place job-retry to every failed/cancelled member job of a workflow, without creating a
  new workflow instance — the workflow-scoped analog of "retry all the red jobs" in one
  click. A node with no job, or whose job is already successful, is skipped rather than
  erroring; an individual retry failure doesn't abort the batch, so the response always
  reports how many jobs were actually retried vs. skipped. The workflow detail page's
  **Retry all unsuccessful jobs** button (shown whenever at least one member job is
  failed/cancelled, independent of the workflow instance's own overall status) drives this
  and redirects back to the same workflow page.

## See also

- `UI_AUTH_PLAN.md` — the architecture/implementation plan this feature was built from
  (schema, CSIL service definitions, task breakdown). Useful if you're extending this
  system, not just operating it.
- `docs/vcs-credentials-and-secret-grants.md` — the underlying secret-grant model that
  authorizes which secrets a job can resolve at runtime (a different, job-execution-time
  concern from the management-UI secrets surface described above).
- `docs/security-model.md` — the broader untrusted-code / secret-isolation security model.
- `helm_chart/DEPLOYMENT.md` — how these env vars map to Helm values for a Kubernetes
  deployment.
