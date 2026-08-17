# Organizations and Trusted CI Policy

This guide explains organization ownership and pull-request CI admission. Use
it when you operate a shared coordinator or when a repository must run changed
CI definitions from a pull-request branch.

## Compatibility Defaults

You do not need a policy file to run existing trusted-base CI.

- Existing workflow definitions continue to run from the trusted base commit.
- A pull request cannot run changed CI definitions without a trusted-base
  policy rule.
- A CI-file change without a policy produces a failed `Reactorcide CI Policy`
  status. Safe trusted-base workflows continue.
- Bare `.reactorcide/jobs/*.yaml` definitions continue to use trusted-base CI.
  Head-CI policy applies to named workflow definitions.
- The `standard` execution profile preserves the previous execution limits.
- The default worker class is `default`.

The first policy file cannot authorize itself. Add the first policy through a
trusted base-branch change.

## Organizations

An organization owns projects, jobs, workflows, secrets, worker classes,
queues, profiles, approvals, and audit events. External commands use the
organization name. The database uses an organization ID.

An organization name is immutable. It must match this expression:

```text
[a-z0-9][a-z0-9-]{0,62}
```

Create and select a default organization:

```bash
reactorcide orgs create example --display-name "Example"
reactorcide orgs set-default example
reactorcide orgs list
```

Delete a bootstrap organization only after you create its replacement:

```bash
reactorcide orgs create example --display-name "Example"
reactorcide orgs delete default --replacement example --yes
```

This command makes `example` the default organization. It then deletes the old
organization and all resources and credentials that the old organization owns.
The command refuses to run while the old organization has an active job or
workflow. The caller must be an administrator of both organizations. An API
token must have both organization scopes and the `organizations:manage`
capability. A global instance token meets these requirements.

The command does not delete user records. A token-only organization does not
need a user record. When you add LinkKeys later, grant the new user the
organization administrator role before you delete the bootstrap organization.

The coordinator uses the default organization when a project or direct job
request does not name an organization. `REACTORCIDE_DEFAULT_ORG` supplies the
name only during initial bootstrap. The database setting is authoritative
after bootstrap.

Organization status has these values:

- `active`: permit new projects and jobs.
- `suspended`: refuse new projects and jobs.
- `disabled`: refuse new projects and jobs.

Other operations remain subject to token capabilities and roles. Use
`suspended` for a temporary stop and `disabled` for an administrative state.

Update a status:

```bash
reactorcide orgs update example \
  --display-name "Example" \
  --status suspended
```

Create a project in an organization:

```bash
reactorcide projects create \
  --org example \
  --name application \
  --repo-url github.com/example/application
```

Do not put an organization name or ID in job or workflow YAML. A child job
always inherits its parent organization.

## API Tokens

An API token has one subject type:

- An instance token can have global authority.
- A service token has an owner organization or an explicit organization set.
- A user token delegates to one active user.
- A job token is short-lived and is bound to one parent job.

The first token can be a global instance token:

```bash
reactorcide token create --name bootstrap-admin
```

Create a limited service token:

```bash
reactorcide token create \
  --name example-ci \
  --org example \
  --capability projects:read \
  --capability jobs:submit \
  --capability jobs:read \
  --capability logs:read
```

Create a delegated user token:

```bash
reactorcide token create \
  --name alice-cli \
  --as-user alice \
  --org example \
  --capability projects:read \
  --capability jobs:read
```

If you omit all `--org` flags, the token has all-organization scope. If you
omit all `--capability` flags, the token has all capabilities. Token creation
through the API cannot give more authority than the caller has. The
`reactorcide token create` command writes directly to PostgreSQL. Treat it as
an instance-operator bootstrap command because it does not use caller token
scope.

The capability names are:

```text
organizations:read    organizations:manage
projects:read         projects:create         projects:manage
jobs:submit           jobs:read               jobs:cancel
jobs:retry            logs:read
workflows:read        workflows:control
secrets:manage        workers:manage
policies:manage       tokens:manage           audit:read
```

## Worker Classes

A worker class connects organization work to one or more worker pools. Queue
identity includes the organization, worker class, and canonical worker
characteristics.

- An organization pool can run work only for its organization.
- A hosted pool has no organization owner.
- A hosted pool can run an organization class only after an administrator
  grants that class to the pool.
- Worker-reported characteristics cannot bypass a class-to-pool mapping.

Every organization starts with the `default` class. Manage classes and pool
grants on `/app/workers/classes`. The token needs `workers:manage`.

The REST paths are:

```text
GET|POST  /api/v1/organizations/NAME/worker-classes
GET|PUT|PATCH|DELETE
          /api/v1/organizations/NAME/worker-classes/CLASS
PUT|DELETE
          /api/v1/organizations/NAME/worker-classes/CLASS/pools/POOL_ID
```

Mark a class as protected when untrusted profiles must not use its pools.

## Execution Profiles

An execution profile limits the authority of a workflow and its jobs. Every
organization starts with these profiles:

- `standard` preserves the normal execution limits.
- `pr-untrusted` denies secrets, root, trusted cache writes, and protected
  worker classes. It also limits runtime capabilities.

List the profiles:

```bash
reactorcide profiles list --org example
reactorcide profiles get --org example pr-untrusted --format yaml
```

Apply a custom profile from a file:

```yaml
name: integration-pr
deny_secrets: true
secret_path_allowlist: []
runtime_capabilities:
  - gpu
may_run_as_root: false
allowed_worker_classes:
  - integration
timeout_ceiling_seconds: 1800
resource_ceilings:
  cpu_limit: "4"
  memory_limit: 8Gi
cache_namespace: integration-pr
artifact_namespace: integration-pr
trusted_cache_writes: false
```

```bash
reactorcide profiles apply --org example integration-pr.yaml
```

The token needs `policies:manage`. A child profile must have the same or less
authority than its parent profile.

## Stable Workflow IDs

Give each policy-controlled workflow an explicit ID:

```yaml
id: backend-tests
name: Backend tests
on:
  events:
    - pull_request_opened
    - pull_request_updated
  branches:
    - main
jobs:
  test:
    job_file: backend-test.yaml
```

The ID must match the organization-name grammar. The ID is the security
identity. The name is only a display label. A policy rule cannot authorize a
workflow that has only a generated compatibility ID.

## Policy Files

Put the main policy at `.reactorcide/policy.yaml`. You can put additional rule
sets in `.reactorcide/policies/*.yaml`. Reactorcide sorts fragments by path and
calculates one revision for the complete policy set.

The evaluator always loads policy from the exact trusted base SHA. A policy
change in a pull request does not apply to that pull request.

Example:

```yaml
version: 1

defaults:
  ci_source: base
  profile: standard

policy_maintainers:
  any:
    - reactorcide_group:ci-admins
    - vcs_team:example/ci-admins

head_ci:
  - id: backend-team
    actors:
      any:
        - vcs_user:github/alice
        - repository_write
        - reactorcide_group:backend
        - vcs_team:example/backend
    workflows:
      - backend-tests
    paths:
      - .reactorcide/workflows/backend.yaml
      - .reactorcide/jobs/backend/**
    events:
      - pull_request_opened
      - pull_request_updated
    base_branches:
      - main
    head_repository: same
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default

  - id: approved-integration
    workflows:
      - integration-tests
    paths:
      - .reactorcide/workflows/integration.yaml
      - .reactorcide/jobs/integration/**
    base_branches:
      - main
    head_repository: any
    approval:
      any:
        - project_owner
        - reactorcide_group:integration-maintainers
    use:
      ci_source: head
      profile: integration-pr
      workers: integration
```

One complete rule must authorize all applicable facts. The rule checks:

- The stable workflow ID.
- Every executable workflow and job path for that workflow.
- Every applicable changed CI path.
- The event and base branch, when configured.
- Whether the head repository is the same repository or a fork.
- The verified actor, when configured.
- A valid approval, when configured.
- The selected profile and worker class.

The supported actor and approval subjects are:

- `repository_write`
- `vcs_user:PROVIDER/LOGIN`
- `vcs_team:OWNER/TEAM`
- `reactorcide_group:NAME`
- `project_owner` for approvals and policy maintainers only

Use a lowercase provider and login in `vcs_user:`. For example, use
`vcs_user:github/alice`. Reactorcide gets this identity from the signed VCS
event. This subject does not need LinkKeys or a Reactorcide user record.

`reactorcide_group:` needs a verified VCS identity link. An administrator can
create and remove identity links with these REST paths:

```text
GET|POST  /api/v1/vcs-identities
DELETE    /api/v1/vcs-identities/LINK_ID
```

## Validate and Explain Policy

The policy commands read local repository files. They do not contact GitHub or
the coordinator.

```bash
reactorcide policy validate --path .

reactorcide policy explain \
  --path . \
  --project application \
  --pr 42 \
  --workflow backend-tests \
  --changed-path .reactorcide/workflows/backend.yaml \
  --changed-path .reactorcide/jobs/backend/test.yaml \
  --event pull_request_updated \
  --base-branch main \
  --head-repository same \
  --actor repository_write
```

The `--project` and `--pr` values label the output. Supply verified actor and
approval subjects when you reproduce a coordinator decision.

## Approvals

An approval binds to the organization, project, pull request, head repository,
head SHA, base SHA, policy revision, workflow, execution profile, and approver
subject. A new head SHA, base SHA, or policy revision makes the approval
inapplicable.

A verified GitHub user can add this exact command to a pull request:

```text
/reactorcide approve WORKFLOW PROFILE POLICY-REVISION
```

The coordinator reads the current SHAs from GitHub. The trusted-base policy
must permit the approver subject.

An operator can create the same record through the API:

```bash
reactorcide approvals create \
  --org example \
  --project application \
  --pr 42 \
  --head-repository github.com/contributor/application \
  --head-sha HEAD_SHA \
  --base-sha BASE_SHA \
  --policy-revision POLICY_REVISION \
  --workflow integration-tests \
  --profile integration-pr \
  --subject project_owner
```

The token needs `policies:manage`. The authenticated user must hold the named
approval subject.

## GitHub Status and Report

Runnerlib returns workflow decisions and policy violations in one evaluation
result. The coordinator then writes a commit status on the exact head SHA:

```text
Context: Reactorcide CI Policy
Success: no unauthorized CI changes
Failure: one or more unauthorized CI changes
```

The status target URL points to the evaluator job. A failed policy status does
not stop safe trusted-base workflows.

The coordinator also stores structured workflow and policy report entries. A
reconciler updates one shared Reactorcide pull-request comment. The database is
the report source of truth. A VCS error does not change the workflow result.

The coordinator selects a VCS credential in this order:

1. Active project credential rotation entry.
2. Project credential reference.
3. Organization or project-owner credential reference.
4. Global provider credential.

For GitHub, the credential needs permission to write commit statuses and pull-
request comments. See [Connect a VCS Repository](./vcs-setup.md).

## Checkout Modes

Runnerlib supports these pull-request evaluation modes:

- `isolated` uses separate source, base CI, and head CI clones.
- `shared` uses one non-shallow, blob-filtered Git object store. It stages only
  the base and head `.reactorcide` views.

The current precedence is:

```text
job or local overlay -> project -> coordinator
```

Set the coordinator default with `REACTORCIDE_CHECKOUT_MODE`. Set the Helm
value with `defaults.checkoutMode`. Set a project override with:

```bash
reactorcide projects update PROJECT_ID --checkout-mode shared
```

Set an empty project value to inherit the coordinator default. Organization-
level checkout defaults are not implemented.

Set the mode in a local evaluator job specification or overlay with:

```yaml
checkout:
  mode: shared
```

A repository workflow or job definition cannot select this mode. Evaluation
has already started before runnerlib reads those files.

Shared mode applies only to pull-request evaluation. It does not create an
application source worktree in the evaluator. Child jobs still receive the
source revision and the approved CI revision that they need.

## Audit Export

The coordinator records organization, token, policy, approval, worker, report,
and security events. Export recent organization events:

```bash
curl --fail --silent --show-error \
  --header "Authorization: Bearer $REACTORCIDE_API_TOKEN" \
  "$REACTORCIDE_API_URL/api/v1/audit?org=example&limit=200"
```

The token needs `audit:read` for the organization. Set Helm value
`audit.retentionDays` or environment variable
`REACTORCIDE_AUDIT_RETENTION_DAYS` to control age-based retention. The default
is 365 days. A value of `0` disables age-based deletion.

## Upgrade Checks

Before you deploy migrations 000025 through 000033:

1. Back up PostgreSQL.
2. Confirm that each resource-owning user has a unique username that can become
   an organization name.
3. Confirm that each loose job and workflow has an owner or a project.
4. Review global worker pools. Migration grants the oldest global pool to the
   default organization.
5. Confirm that existing projects map to the expected organizations.
6. Confirm that the default organization is correct after migration.
7. Test one application-only pull request and one CI-file pull request.

Existing API tokens migrate with their previous broad authority. Create
narrower tokens after the upgrade when required.
