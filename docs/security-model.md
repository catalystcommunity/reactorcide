# Reactorcide Security Model

Reactorcide separates trusted CI definitions from application source. It also
uses container isolation, worker authentication, and explicit secret grants.
These controls reduce risk. They do not make untrusted code safe by
themselves.

## Trust Boundaries

The coordinator is trusted. It has access to:

- PostgreSQL
- Corndogs
- The object store
- Secret encryption keys
- VCS system credentials

A worker is trusted to run jobs and to protect values that the coordinator
gives to it. A worker connects only to the coordinator.

A job container is trusted only for the permissions that its job needs. Treat
application source from a pull request as untrusted.

## Trusted CI Source

A job can use two source locations:

```text
/job/src   application source
/job/ci    trusted CI source
```

For a pull request from a fork:

- `SourceURL` points to the fork.
- `SourceRef` points to the pull-request head.
- `CISourceURL` points to the trusted upstream repository.
- For an open or updated pull request, `CISourceRef` points to the trusted base
  commit.
- For a merged pull request, `CISourceRef` points to the trusted revision on the
  target branch.

This design prevents the pull request from changing the CI definition that
starts the job.

Runnerlib plugins (`.reactorcide/plugins`) are CI code. Runnerlib loads them
only from the trusted CI tree when the run has untrusted application source:
a run with a separate CI tree, or a fork pull request. When the trusted CI
tree has no plugins, runnerlib loads no repository plugins. It never uses
the plugins of the application source as a fallback. It also refuses a
`--plugin-dir` or `REACTORCIDE_PLUGIN_DIR` directory that is in the
application source. See
[Plugin Development](../runnerlib/docs/plugin_development.md#plugin-loading).

The coordinator treats a pull request as a fork unless it knows that the
head repository is the base repository (fail closed). A GitHub pull request
with a null `head.repo` (a deleted fork) is a fork. A GitLab merge request
is a fork when its `source_project_id` is not its `target_project_id`, or
when the IDs are not known. The eval job of a fork with no known repository
fetches the head commit from the base repository.

Only the job token of a running job can submit triggers for that job
(`POST /api/v1/jobs/{id}/triggers`). A child job inherits the event, the ref
classes, and the trusted CI authority of its parent, so no other credential
can add jobs to a parent, and a finished job cannot add jobs.

## Important Limit

Trusted CI source does not isolate secrets from untrusted application code.
The CI script and application source can run in the same container. A build
tool, test, compiler plugin, package script, or application test can read the
job environment and files.

Do not give production secrets to a job that executes untrusted source unless
you accept that source as a secret consumer.

Use separate jobs:

1. Run untrusted tests without secrets.
2. Store only non-secret results or artifacts.
3. Run a trusted release or deployment job after the required approval or
   trusted event.
4. Give the deployment secret only to that trusted job.

## Secret Authorization

Job YAML uses references:

```yaml
environment:
  DEPLOY_TOKEN: "${secret:deploy/production:token}"
```

Do not replace a reference with a value.

The coordinator authorizes remote secret access through built-in job scope or
an explicit secret grant. Make each grant narrow:

- Select one project.
- Select an exact path when possible.
- Select one job name or a narrow prefix.
- Remove grants that are no longer necessary.

Log masking reduces accidental disclosure. It is not an access control. A
process that receives a secret can use or disclose it.

To keep a secret out of the job, use a proxy-delivered grant
(`delivery: proxy`, see [Egress Proxy](./egress-proxy.md)). The job gets a
placeholder. The egress proxy puts the value only in the named header and
only for the hosts in the grant. A job that sends the placeholder to another
host gets 403, and the value does not leave the proxy.

A job can also get a short-lived OIDC token for a consumer, in place of a
static secret ([OIDC Tokens for Jobs](./oidc.md)). Any job can ask for a
token, including a fork pull request job. The consumer decides what a token
gets. Configure each consumer with the trust conditions in that document:
match the full subject, `event_type`, `ci_origin`, the execution profile, and
the ref classes. Never give write access to a token with a `refs/pull/` ref.

## Egress Control

Egress control sends all network traffic of a job through the egress proxy.
Turn it on for an organization, a project, or an execution profile
(`egress_control: true`). It is off by default. With it on:

- The job can reach only the destinations in the proxy policy, the job's
  own destinations (the coordinator API), and the hosts of its proxy grants.
- The proxy scans requests and responses for secret values (raw, base64,
  hex, percent encoding) and removes them.
- The job has no network capability. In Kubernetes an init container with
  only `NET_ADMIN` installs the redirect; in Docker and containerd a setup
  container does this in a helper namespace. A guard rule drops all other
  traffic, for example UDP.
- The worker refuses the `docker` capability (a privileged job could remove
  the redirect) and the `builder` capability with egress control.
- A job with egress control runs only on a worker with the characteristic
  `egress_proxy=true`.

The proxy ends TLS with its own CA. Keep the CA key in the proxy Secret only.
The worker gets the CA certificate, the job key, and the control token. The
job gets only its own credential, which stops working when the job ends.

See [VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md).
See [Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md)
for organization scope, execution profiles, policy rules, and approvals.

## VCS Credentials

Webhook secrets and VCS API tokens are system credentials. Keep them in the
coordinator secret store. Project configuration contains only `path:key`
references.

Use a different webhook secret for each repository when possible. Give VCS API
tokens only these necessary permissions:

- Read the repository when private checkout needs it.
- Write commit status.
- Read pull-request data.
- Write a pull-request comment when that feature is in use.

Do not give a VCS token to the job environment. The worker uses temporary Git
credential files for approved private checkout.

## Runtime Risk

These job features increase access:

- `run_as.user: root`
- A Docker or containerd host socket
- `capabilities: [docker]`
- A privileged container
- A BuildKit service with registry credentials. The default sidecar is
  privileged. The rootless mode removes the privilege
  ([Builds, Caches, and Job Settings](./builds-and-caches.md#rootless-builder)).
- Broad Kubernetes service-account permissions
- Open egress (egress control off): the job can send any data to any host

Capabilities do not automatically select `root`. Request the minimum
capability and user for each job.

A job with a host container-runtime socket can usually control that runtime.
Treat it as host-level access.

## Cache and Image Boundaries

The coordinator selects the cache namespace and the write mode of each job
from the execution profile and the ref classes of the job. The worker cannot
select another namespace. A job with no cache writes gets no persistent
BuildKit state, so it cannot change a cache that another job reads. Two
organizations never share a namespace volume, except the shared namespace
(`standard` by default), which keeps the behavior before namespaces. See
[Builds, Caches, and Job Settings](./builds-and-caches.md#cache-namespaces).

Do not give `cache_namespace` of a trusted profile to an untrusted profile
with writes. Keep `trusted_cache_writes: false` (or `cache_write_classes`)
for profiles of pull request code.

An organization image policy limits job images to allowed registry prefixes
and can require digest pinning. The coordinator checks it at submit and at
claim.

Environment defaults are not secret. The coordinator refuses a secret
reference in a default, and refuses reserved keys.

## Webhook Security

Use HTTPS for public webhook endpoints. Configure a project webhook secret.
Reactorcide validates:

- GitHub `X-Hub-Signature-256` with HMAC-SHA256.
- GitLab `X-Gitlab-Token` with the configured token.

Do not depend only on source IP filtering. Rotate a webhook secret with the
credential rotation procedure in [UI Authentication and
Authorization](./ui-auth.md).

## Management Credentials

- The coordinator authorizes every management operation. The UI service
  (CSIL-RPC) and REST apply the same checks to a browser session and to an
  API token: the role of the owner, the organization scope of the token, and
  the token capability. See
  [Sessions and API tokens](./ui-auth.md#sessions-and-api-tokens).
- The webapp holds no coordinator API token. It sends the session of the
  browser to the coordinator, and it removes each credential that the client
  supplies.
- The webapp accepts a CSIL-RPC call and an event stream upgrade (`/app/ws`)
  only when the `Origin` header names the host that serves the request.
  Another site and a sibling subdomain get HTTP 403.
- A token never gets more than its capabilities. A token of a service
  principal (`reactorcide principals tokens create`) also gets no more than
  the roles of the principal. See [Principals](./principals.md).
- WARNING: on a no-login instance (`REACTORCIDE_UI_AUTH_MODE=none`), a
  principal token with no capability list gets all capabilities and all
  organizations. It keeps them after you enable logins. Revoke such tokens
  before you enable logins. See
  [No-login instances](./ui-auth.md#no-login-instances-single-admin).

## Deployment Security

For production:

- Use managed PostgreSQL or a protected PostgreSQL service.
- Use durable S3-compatible object storage.
- Store encryption master keys in a Kubernetes Secret or an external secret
  system.
- Put TLS in front of the API and web application.
- Restrict coordinator and database network access.
- Persist worker identity when stable worker records are required.
- Review worker queue characteristics and Kubernetes RBAC.
- Back up PostgreSQL and the object store.

## Operator Checklist

- Protect the trusted CI branch.
- Configure project-specific VCS secret references.
- Test a fork pull request with no job secrets.
- Review every secret grant.
- Review every `root`, `docker`, and `builder` job.
- Keep coordinator and worker images current.
- Monitor failed webhook validation and denied secret access.

## Organization and CI Admission Boundaries

Every remote job belongs to one organization. A child job inherits the
organization of its parent. A trigger payload cannot select an organization.
A worker receives work only through a pool mapping for the job organization
and worker class. Worker characteristics do not grant access to another pool.

Pull-request CI policy comes only from the coordinator database. Repository
content cannot create or change it. The coordinator puts an exact policy
snapshot and revision on each evaluation job. Runnerlib does not evaluate the
snapshot. The job also receives an active approval snapshot. Triggered jobs
inherit both snapshots. Runnerlib returns inactive base and head candidates.
The coordinator verifies the policy snapshot and loads current approvals. It
selects the allowed candidate and sets its
authority. Head files are data until one complete policy rule permits a
workflow security ID and all of its executable CI paths. A violation does not
stop safe base workflows.

An execution profile limits secrets, runtime capabilities, root use, worker
classes, time, resources, caches, and artifacts. Child work must use the same
or a weaker profile. Secret grants can restrict the project, profile, and CI
origin. An approval binds to the project, pull request, head repository, and
head SHA. A new head SHA makes the approval invalid. The profile of a run comes
from the settings or a policy rule, never from the approval.

A head-CI policy rule can name trusted base nodes with `base_nodes`. Only
coordinator policy can give a node more authority than the workflow head-CI
default. The trusted node and all of its executable CI content resolve from
the exact base CI SHA, while the tested source stays at the head SHA. A
pull-request head cannot add, change, rename, or replace a trusted node. The
coordinator sets node authority from its policy, stores the effective authority
on the node, and fails closed on any mismatch.
A retry replays the recorded node authority. An untrusted node never receives
a resolved secret from a trusted node; only normal workflow variables cross
the boundary. Treat a signed URL in a workflow variable as a temporary
credential and do not write it to a log or summary.

A pull request command (`approve-ci`, `override-ci`, `accept`, `revoke`,
`retry`, `rerun`) has the same rules in a comment, a review, the CLI, the API,
and the UI. `promote` and `demote` change the
[required set](./required-set.md) and are comments only. The coordinator gets the pull request head from the VCS. It records
only provider-verified or identity-linked approver subjects, and the settings
decide which subjects can give each command. A pull request review never
grants execution.

The run gate holds a pull request until a run approver gives `approve-ci`. A
new organization has the gate `outside_contributors` (the `secure` defaults
profile). An organization from before migration 000055 has no gate. An
approved gated run uses trusted base CI only, and by default it gets the
profile `pr-untrusted`, with no secrets. Without a gate, a fork pull request
runs trusted base CI with the normal profile. Its job can get secrets while it
builds the fork's code. Use a gate, or keep secrets out of jobs that build
untrusted source. See
[Run Gate, Approvals, and Required Reviews](./organizations-and-ci-policy.md#run-gate-approvals-and-required-reviews).

Audit records contain identifiers and authorization facts. They do not
contain raw tokens, secret values, or provider credentials. Use
`REACTORCIDE_AUDIT_RETENTION_DAYS` to set the retention age.
