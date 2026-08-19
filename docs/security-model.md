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
- A BuildKit service with registry credentials
- Broad Kubernetes service-account permissions

Capabilities do not automatically select `root`. Request the minimum
capability and user for each job.

A job with a host container-runtime socket can usually control that runtime.
Treat it as host-level access.

## Webhook Security

Use HTTPS for public webhook endpoints. Configure a project webhook secret.
Reactorcide validates:

- GitHub `X-Hub-Signature-256` with HMAC-SHA256.
- GitLab `X-Gitlab-Token` with the configured token.

Do not depend only on source IP filtering. Rotate a webhook secret with the
credential rotation procedure in [UI Authentication and
Authorization](./ui-auth.md).

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
content cannot create or change it. The coordinator puts an exact policy copy
on the evaluation job and checks the result against that copy. Head files are
data until one complete policy rule permits a workflow security ID and all of
its executable CI paths. A violation does not stop safe base workflows.

An execution profile limits secrets, runtime capabilities, root use, worker
classes, time, resources, caches, and artifacts. Child work must use the same
or a weaker profile. Secret grants can restrict the project, profile, and CI
origin. An approval binds to the project, pull request, head repository, head
SHA, base SHA, policy revision, workflow, and profile. A new head SHA makes the
approval invalid.

A head-CI policy rule can name trusted base nodes with `base_nodes`. Only
coordinator policy can give a node more authority than the workflow head-CI
default. The trusted node and all of its executable CI content resolve from
the exact base CI SHA, while the tested source stays at the head SHA. A
pull-request head cannot add, change, rename, or replace a trusted node. The
coordinator verifies each node authority claim against its policy copy,
stores the effective authority on the node, and fails closed on any mismatch.
A retry replays the recorded node authority. An untrusted node never receives
a resolved secret from a trusted node; only normal workflow variables cross
the boundary. Treat a signed URL in a workflow variable as a temporary
credential and do not write it to a log or summary.

A GitHub approval comment names the workflow, profile, and coordinator-policy
revision. The coordinator gets the pull request SHAs from GitHub. It records
only provider-verified or identity-linked approver subjects. The admission
check requires an exact match before it uses the approval.

Audit records contain identifiers and authorization facts. They do not
contain raw tokens, secret values, or provider credentials. Use
`REACTORCIDE_AUDIT_RETENTION_DAYS` to set the retention age.
