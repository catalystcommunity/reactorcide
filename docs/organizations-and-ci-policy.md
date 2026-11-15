# Organizations and Coordinator CI Policy

This guide explains organization ownership and pull-request CI admission. Use
it when you operate a shared coordinator or when a repository must run changed
CI definitions from a pull-request branch.

## Compatibility Defaults

You do not need a coordinator policy to run existing trusted-base CI.

- Existing workflow definitions continue to run from the trusted base commit.
- A pull request cannot run changed CI definitions without a coordinator
  policy rule.
- With a stored policy, a refused CI-file change fails `Reactorcide Checks`.
  Safe trusted-base workflows continue.
- Bare `.reactorcide/jobs/*.yaml` definitions continue to use trusted-base CI.
  Head-CI policy applies to named workflow definitions.
- The `standard` execution profile preserves the previous execution limits.
- The default worker class is `default`.

Create the first policy with the coordinator API or CLI. A repository change
cannot create or replace it.

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

## Coordinator CI Policy

Store one CI policy for each project in the coordinator. Do not put this
policy in the source repository. Reactorcide does not read
`.reactorcide/policy.yaml` or `.reactorcide/policies/` during evaluation.
It treats these legacy paths as ordinary CI content when they change.

The coordinator validates the policy and calculates its revision. It puts an
exact policy snapshot, revision, and active approval snapshot on each
evaluation job. Triggered jobs inherit these values. Runnerlib does not
evaluate the policy. It returns inactive base and head workflow candidates.
The coordinator verifies the policy snapshot, loads current approvals,
selects the allowed candidate, and applies the execution authority. A later
policy update does not change this decision.

Keep an operator copy, such as `ci-policy.yaml`, outside the source repository.
Use the CLI to validate and store it:

```bash
reactorcide policy validate --file ci-policy.yaml
reactorcide policy set \
  --api-url https://reactorcide.example.com \
  --token "$REACTORCIDE_API_TOKEN" \
  --project application \
  --file ci-policy.yaml
```

The token needs `policies:manage` for the project organization. Use the project
ID when this is the token's only capability. Project name and repository URL
resolution also need `projects:read`. A project owner, an organization
administrator, or a global administrator can also manage the policy with an
authenticated session.

### Migrate an Old Repository Policy

Complete this migration after the coordinator database migration is active:

1. Copy the old `.reactorcide/policy.yaml` content to an operator file outside
   the repository.
2. Add each rule from `.reactorcide/policies/*.yaml` to the `head_ci` list in
   the operator file.
3. Remove `policy_maintainers`. Coordinator authorization now controls policy
   changes.
4. Run `reactorcide policy validate --file OPERATOR_FILE`.
5. Run `reactorcide policy set --project PROJECT --file OPERATOR_FILE` with the
   coordinator URL and an authorized session or API token.
6. Run `reactorcide policy get --project PROJECT` and record the revision.
7. Delete `.reactorcide/policy.yaml` and `.reactorcide/policies/` from the
   repository.

Set the coordinator policy before you delete the old files. The old files have
no authority after this release, but this order prevents an interval with no
head-CI policy.

Example:

```yaml
version: 1

defaults:
  ci_source: base
  profile: standard

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

A changed `.reactorcide/` file that no workflow claims as its own YAML or
job file — a plugin, script, test, or helper file — is a shared CI path.
The evaluator attributes a shared CI path to every candidate workflow,
because plugin and script code loads globally. A rule whose `paths` cover
the shared path authorizes the change; a rule with narrower `paths` refuses
it.

The remaining checks are:
- The event and base branch, when configured.
- Whether the head repository is the same repository or a fork.
- The verified actor, when configured.
- An `override-ci` approval from a subject in `approval:`, when configured.
- The selected profile and worker class.

The supported actor and approval subjects are:

- `repository_write`
- `vcs_user:PROVIDER/LOGIN`
- `vcs_team:OWNER/TEAM`
- `reactorcide_group:NAME`
- `project_owner` for approvals only

Use a lowercase provider and login in `vcs_user:`. For example, use
`vcs_user:github/alice`. Reactorcide gets this identity from the signed VCS
event. This subject does not need LinkKeys or a Reactorcide user record.

`reactorcide_group:` needs a verified VCS identity link. The link connects a
VCS login to a Reactorcide user. The user then gets the subject
`reactorcide_group:NAME` for each group of the project organization. Make one
link for each VCS provider that the user approves from. The same group then
applies from each provider.

To make a group and add a member, use a token with `organizations:manage` for
the organization (an org admin for a user token):

```bash
reactorcide groups create --org example linkkeys-trust-maintainers
reactorcide groups add-member --org example linkkeys-trust-maintainers alice
reactorcide groups members --org example linkkeys-trust-maintainers
reactorcide groups remove-member --org example linkkeys-trust-maintainers alice
```

A group name is lowercase letters, digits, and hyphens, and starts with a
letter or a digit. The UI also manages groups.

To link a VCS login to the user, use a token with `organizations:manage`:

```bash
reactorcide identities link --org example --provider github --login alice --username alice
```

An admin link has `verified_by=admin`. The coordinator compares the provider
and the login without regard to case. To list or remove links, use a token for
all organizations:

```bash
reactorcide identities list
reactorcide identities unlink LINK_ID
```

The REST paths are:

```text
GET|POST  /api/v1/organizations/ORG/groups
GET|POST  /api/v1/organizations/ORG/groups/GROUP/members
DELETE    /api/v1/organizations/ORG/groups/GROUP/members/USERNAME
GET|POST  /api/v1/vcs-identities
DELETE    /api/v1/vcs-identities/LINK_ID
```

## Policy-Controlled Node Authority

A head-CI rule can give selected workflow nodes trusted base authority while
the other nodes run head CI with the untrusted profile. Use this for a
pull-request workflow that needs trusted control nodes, for example a node
that reads an asset-cache secret before untrusted build nodes run.

Add a `base_nodes` list to the rule `use` block:

```yaml
head_ci:
  - id: csilgen
    actors:
      any: [repository_write]
    workflows: [csilgen-pr]
    paths: ['.reactorcide/**']
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
      base_nodes:
        - nodes: [asset-prepare, asset-seal]
          ci_source: base
          profile: standard
          workers: default
```

Each entry names exact workflow node names. Each entry must set
`ci_source: base`, one profile, and one worker class. A node name can appear
in only one entry for a rule.

The rules for a policy-controlled base node are:

- Runnerlib resolves the node and all of its executable CI content from the
  exact base CI SHA. This includes the workflow definition, the job file,
  plugins, scripts, and helper files. It returns this content as an inactive
  candidate.
- The tested source stays at the pull-request head SHA.
- The base specification wins. A head change to the node command, image,
  environment, `job_file`, dependencies, `for_each`, capabilities, or
  condition has no effect on the node that runs. A head node with the same
  name is ignored. A trusted node that head removed still runs.
- Repository YAML cannot set or replace node authority. Runnerlib rejects a
  workflow job entry or job file that contains a reserved authority field.
- The coordinator sets node authority from the policy snapshot. The node
  profile must be the same or weaker than the evaluation job profile. The
  node worker class must be allowed by the node profile.
- The coordinator stores the effective CI origin, CI repository, CI SHA,
  profile, worker class, policy revision, rule, and approval on each node
  that differs from the workflow default.
- A retry replays the recorded node authority. A retry does not evaluate a
  newer policy.
- Admission fails closed when the base workflow does not contain a named
  node, when the node profile does not exist, when the policy revision does
  not match, or when a required approval is missing. The workflow then runs
  trusted base CI, and the policy status reports the violation.

Secret authorization uses the effective node authority. A grant with
`--execution-profile standard --ci-origin base` and an exact job name matches
only the trusted node. An untrusted node never receives a secret when its
profile denies secrets, and normal workflow variables still flow between the
two authority levels.

## Get, Validate, and Explain Policy

The `get`, `set`, and `delete` commands contact the coordinator. The `validate`
and `explain` commands read one local file and do not contact the coordinator.

```bash
reactorcide policy get \
  --api-url https://reactorcide.example.com \
  --token "$REACTORCIDE_API_TOKEN" \
  --project application

reactorcide policy validate --file ci-policy.yaml

reactorcide policy explain \
  --file ci-policy.yaml \
  --workflow backend-tests \
  --changed-path .reactorcide/workflows/backend.yaml \
  --changed-path .reactorcide/jobs/backend/test.yaml \
  --event pull_request_updated \
  --base-branch main \
  --head-repository same \
  --actor repository_write
```

Supply verified actor and approval subjects when you reproduce a coordinator
decision. Use `--expected-revision` with `set` or `delete` to prevent an update
from replacing a newer policy.

## Run Gate, Approvals, and Required Reviews

A person controls a pull request head with a command. Give the command as a
comment on the pull request, with the CLI, with the REST API, or in the UI.
Each channel uses the same rules and has the same effects.

| Command | Effect |
| --- | --- |
| `/reactorcide approve-ci` | The run gate releases the head. The head runs on trusted base CI. The head's changes to CI do not run. |
| `/reactorcide override-ci` | The head's own CI runs. |
| `/reactorcide accept [LABEL]` | You accept shared responsibility for the change. This counts toward required reviews. It is not related to CI. |
| `/reactorcide revoke` | You withdraw your approvals for the head (see [Revoke](#revoke)). |
| `/reactorcide retry` | The failed and cancelled jobs of the head's workflows run again. |
| `/reactorcide rerun` | Every workflow of the head runs again. A push of an empty commit does the same. |

The comment must contain only the command. You do not give a workflow, a
profile, or a policy revision. The coordinator evaluates the current head with
the current settings and the current policy.

### Approvals bind to the head

An approval binds to the organization, the project, the pull request, the head
repository, and the head SHA. A new head SHA cancels every approval of the old
heads. A new base SHA or a new policy revision does not cancel an approval. A
policy change applies to the approvals that exist.

Each command records one action. The coordinator never changes or deletes an
action. It calculates the state from the actions. A redelivery of the same
comment or review makes no new action.

### Who can give a command

The coordinator finds the approver subjects of the person:

- `vcs_user:PROVIDER/LOGIN` for the VCS account.
- `repository_write` and `vcs_team:OWNER/TEAM` from the VCS.
- `reactorcide_group:NAME` and `project_owner` for a Reactorcide user. A VCS
  account gets them through a verified identity link.

A Reactorcide user in the UI or the CLI acts as their linked VCS account, if
the user has a verified identity link for the provider. Then the user has the
same subjects as in a comment.

| Command | The person needs a subject from |
| --- | --- |
| `approve-ci`, `retry`, `rerun` | `run_approvers` |
| `override-ci` | `override_approvers`, or the `approval:` list of a `head_ci` policy rule |
| `accept`, `revoke` | any person; a required review decides which subjects count |

The coordinator records a refused command in the audit log. A refused comment
does nothing else.

### Run gate

The run gate holds a pull request until a run approver gives `approve-ci`. Set
one or more conditions:

- `forks`: the head is in a fork.
- `first_time_contributors`: the provider does not know the author as an
  owner, member, collaborator, or contributor of the repository.
- `outside_contributors`: the author or the person who pushed has no
  repository write permission.

A held event has the status `awaiting_approval` and no eval job. "Reactorcide
Checks" is pending with the text `Waiting for a run approver: /reactorcide
approve-ci`. When a fact cannot be verified, the gate holds the pull request.

An approved gated run uses trusted base CI only. A `head_ci` policy rule does
not apply to it. Set `gated_run_authority` for the profile of the run:

- `no_secrets` (default): every node uses the profile `pr-untrusted`. The run
  gets no secrets.
- `normal`: the run uses its normal profile, with its secrets.

The default run gate has no condition. Thus a deploy does not change which pull
requests run. Without a gate, a fork pull request runs trusted base CI with
the normal profile. That run can get secrets, and it builds the fork's code.
Use a gate when this is not acceptable.

### Override

`override-ci` lets the head's own CI run in its entirety. Each workflow whose CI
files the head changed runs from the head, with `override_profile` (default
`pr-untrusted`) and the default worker class. A workflow that the head did not
change runs from the base. `override-ci` also releases the run gate.

A `head_ci` policy rule that matches the change wins over an override. A rule
can be narrower: it can have its own profile and trusted base nodes. A rule
with `approval:` grants head CI when a person with a listed subject gives
`override-ci`.

### Required reviews

A required review must be satisfied before the pull request can merge. The
coordinator evaluates the reviews of the head and posts the commit status
`Reactorcide Reviews`. To block a merge, require this status in the branch
rules. A review is not related to CI, so "Reactorcide Checks" does not include
it.

```yaml
required_reviews:
  - id: trust-anchors
    description: Trust anchors and their verifiers
    paths: ["crates/liblinkkeys/src/crypto.rs", "crates/linkkeys/src/services/pins.rs"]
    accept: [reactorcide_group:linkkeys-trust-maintainers]
    count: 1
  - id: compliance
    accept: [non_author]
    count: 1
```

- `paths` limits the review to a change that touches a pattern. With no
  `paths`, the review applies to each pull request. When the coordinator cannot
  list the changed files, every review applies.
- `accept` lists the subjects that can satisfy the review. `non_author` is any
  person who did not write the change and who has a role: repository write, a
  team, a group, or project owner. The authors are the pull request author,
  each person who pushed to the head, and each commit author and committer that
  the provider links to an account. The person who made the current head
  cannot accept as a non-author. When the coordinator cannot list the authors,
  no accept counts through `non_author`.
- `count` is the number of different people (default 1).
- The review ID is also a label. `/reactorcide accept trust-anchors` counts only
  for that review. `/reactorcide accept` with no label counts for every review
  that you can satisfy.

An approved pull request review is an accept. A line
`/reactorcide accept LABEL` in the review body gives it a label. A review never
grants execution: a "changes requested" review revokes only the reviewer's
accepts, and a dismissed review cancels only that reviewer's accept.

Configure the GitHub webhook to send the "Pull request reviews" event.

### Revoke

For each role (approver subject), the last action that names the role wins:

- A revoke cancels the approval of each role that the revoker has. It also
  cancels the approval of another person with the same role.
- An approval through a role that the revoker does not have stays.
- For `non_author`, the role is the person. A revoke cancels only the
  revoker's own accept.

A revoke of `approve-ci` or `override-ci` does not stop work that runs. It
applies to the next evaluation of the head.

### Re-evaluate, rerun, and retry

- **Re-evaluate**: after `approve-ci` or `override-ci`, the coordinator
  evaluates the head again. The new event has the source `approval`. A
  workflow whose decision (CI origin and profile) did not change does not run
  again: the event carries the workflow of the earlier event, and its result
  counts. `approve-ci` re-evaluates only a head that the run gate held.
  `accept` and `revoke` start no evaluation. They only update `Reactorcide
  Reviews`.
- **Rerun**: every workflow of the head runs again. The new event has the
  source `rerun`.
- **Retry**: the failed and cancelled jobs of the newest workflows of the head
  run again.

The re-evaluation and the rerun use the actor of the last webhook event for
the head, not the approver. Thus actor policy sees the same facts as before.
Only the newest event of a commit posts "Reactorcide Checks".

### Settings

The run gate, approver, override, and required-review settings are
organization and project settings. They are not in the policy document.

| Setting | Default |
| --- | --- |
| `run_gate` | empty (no gate) |
| `gated_run_authority` | `no_secrets` |
| `run_approvers` | `project_owner`, `repository_write` |
| `override_approvers` | `project_owner`, `repository_write` |
| `override_profile` | `pr-untrusted` |
| `required_reviews` | none |

A project setting that is absent inherits the organization value. An empty
list is a value: `run_gate: []` removes the organization gate for the project.
Organization required reviews apply to each project, and a project adds its
own.

A project owner can make a project more strict than the organization. Only an
organization administrator can make it less strict. An API token with
`policies:manage` can also change the settings.

```bash
reactorcide trust get --org example --project application
reactorcide trust set --org example --file org-trust.yaml
reactorcide trust set --org example --project application --file project-trust.yaml
```

The UI has the same settings on the organization and project pages.

### Commands through the CLI and the API

```bash
reactorcide approvals approve-ci --project application --pr 42
reactorcide approvals accept --project application --pr 42 --label trust-anchors
reactorcide approvals show --project application --pr 42
```

Add `--head-sha SHA` to refuse the command when the head changed. The CLI uses
your session or your user API token: a command needs a person.

The REST path is `POST /api/v1/approvals` with `organization`, `project`,
`pr_number`, `command`, and optional `label` and `head_sha`. The token must
belong to a user and have `workflows:control`.

### Approval facts in jobs

Each evaluation job, and each job it starts, gets `REACTORCIDE_CI_APPROVALS`:
a JSON array of the approval actions of the head when the event arrived.
Admission does not read it; it reads the current actions when it decides.

`REACTORCIDE_RUN_AUTHORITY` carries the run gate and override decision of the
eval job.

Repository YAML cannot set or replace these values. See
[Reserved event facts](#reserved-event-facts).

### Reserved event facts

The coordinator sets event facts in the job environment. Repository YAML
(base or head) cannot set or replace them. Runnerlib refuses a workflow or job
file whose `environment:` names one of them, and the coordinator ignores them
in trigger output. The reserved names are:

- `REACTORCIDE_ACTOR_SUBJECTS`, `REACTORCIDE_CI_APPROVALS`,
  `REACTORCIDE_RUN_AUTHORITY`, `REACTORCIDE_CI_POLICY`,
  `REACTORCIDE_CI_POLICY_REVISION`
- `REACTORCIDE_SHA`, `REACTORCIDE_DIFF_BASE`, `REACTORCIDE_SOURCE_URL`,
  `REACTORCIDE_BRANCH`, `REACTORCIDE_REPO`, `REACTORCIDE_PROVIDER`
- `REACTORCIDE_EVENT_ID`, `REACTORCIDE_EVENT_TYPE`, `REACTORCIDE_IS_FORK_PR`,
  `REACTORCIDE_JOB_KIND`, `REACTORCIDE_EVAL_DUAL_CI`,
  `REACTORCIDE_REPORT_GENERATION`, `REACTORCIDE_REPORT_TARGET_ID`
- each name that starts with `REACTORCIDE_HEAD_`, `REACTORCIDE_BASE_`, or
  `REACTORCIDE_PR_`

The names are not case-sensitive. Other names, for example
`REACTORCIDE_CI_JOB`, are not reserved.

A job that you submit through the API has no CI event. It posts no commit
status. A trigger from such a job can give its child event context, for
example `REACTORCIDE_EVENT_TYPE`, but not the first five names in the list.

## GitHub Status and Report

Runnerlib returns inactive base and head workflow candidates in one evaluation
result. It does not return an authority decision. The coordinator verifies the
policy snapshot, loads the current approvals, and makes the authoritative
decision.

A CI event posts one commit status on the exact head SHA, `Reactorcide
Checks`. It includes the eval job, each workflow, each workflow that the
coordinator did not admit, and each CI change that the policy refused. A
refused CI change fails the status, but it does not stop safe trusted-base
workflows. Required reviews post a second status, `Reactorcide Reviews` (see
[Required reviews](#required-reviews)).

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
