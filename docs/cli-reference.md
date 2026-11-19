# CLI Reference

The `reactorcide` binary runs the coordinator, runs jobs, and operates the
coordinator API. Build it first:

```bash
cd coordinator_api && go build -o reactorcide .
```

## Connect to a Coordinator

Commands that use the API read the coordinator URL and the API token from
these environment variables:

- `REACTORCIDE_API_URL`
- `REACTORCIDE_API_TOKEN`

Use `--api-url` and `--token` to override them. If you give no token, the
command prompts for one. The prompt hides the value.

The URL must use HTTPS because these commands send credentials and can send
secrets. Use `--allow-insecure-transport` only on an isolated development
network. You must set this option on the command line. There is no environment
variable for it. The check also rejects a redirect from HTTPS to HTTP.

You can put a flag before or after a positional argument. These two commands
are the same:

```bash
reactorcide jobs get <job-id> --format json
reactorcide jobs --format json get <job-id>
```

## Output Formats

Commands that show records accept `--format`:

- `table` (default): columns for a terminal
- `json`: the full record
- `yaml`: the full record

## Server Commands

| Command | Purpose |
| --- | --- |
| `serve` | Run the coordinator API. `--allow-insecure-transport` permits HTTP for federation issuer fetches and the OIDC issuer URL, on an isolated development network only. |
| `migrate` | Run database migrations. |
| `worker` | Run a job worker. |
| `healthcheck` | Check the API health endpoint. Use it for container health checks. |
| `windows-service install\|start\|stop\|restart\|status\|uninstall` | Manage the native Windows worker service. `install` takes `--config` (the service JSON file) and `--executable`. |

`serve --port` sets the listen port. `migrate --db-uri` and `serve --db-uri`
set the database. Each `worker` flag also has an environment variable, for
example `--concurrency` and `REACTORCIDE_WORKER_CONCURRENCY`. Run
`reactorcide worker --help` for the full list. The flags set the coordinator
URL, the enrollment token file, the characteristics (`--os`, `--arch`,
`--custom`, `--region`, `--zone`, `--cloud`), the runtime
(`--container-runtime`), the admission mode (`--admission`,
`--admission-cpu`, `--admission-memory`), the telemetry intervals, and the VM
image cache (`--vm-image-prefetch`, `--vm-image-max-unused`,
`--vm-image-prune-interval`, `--vm-image-registry-plain-http`). See
[Worker environment reference](./workers.md#worker-environment-reference),
[Headless Windows Worker](./windows-headless-worker.md), and
[VM Image Operations](./vm-images.md#worker-prefetch-and-retention).

## Job Commands

| Command | Purpose |
| --- | --- |
| `run-local <file>` | Run one job or one workflow on this machine. |
| `submit <job-file>` | Submit a job to a coordinator. |
| `run --project P --ref REF` | Start a new evaluated run on a branch, a tag, or a pull request head (`--pr N`). |
| `jobs list` | List jobs. |
| `jobs get <job-id>` | Show one job. |
| `jobs cancel <job-id>` | Cancel a job. Cleanup hooks run. |
| `jobs kill <job-id>` | Stop a job immediately. Cleanup hooks do not run. Admin only. |
| `jobs retry <job-id>` | Run the job again as a new job. Optional shifts. |
| `jobs delete <job-id>` | Delete a job and its telemetry. |
| `jobs metrics <job-id>` | Show CPU, memory, and storage metrics for a job. |
| `jobs resource-suggestion <job-id> [--runs N]` | Suggest `cpu_request` and `memory_limit` from the p95 of the measured runs with the same organization, project, and job name (default: the newest 50 runs). |
| `backlog [--org ORG]` | Show the waiting jobs of each queue (`GET /api/v1/backlog`). Without `--org` the token needs `workers:read` or `workers:manage` for all organizations. |
| `logs <job-id> [--stream stdout\|stderr\|combined] [-o FILE]` | Get the logs of a job. The default stream is `combined`. |

Filter `jobs list` with `--status`, `--queue-name`, `--source-type`,
`--project-id`, `--workflow-id`, `--user-id`, and `--failure-class` (`job`,
`timeout`, `oom`, `infra.image_pull`, `infra.scheduling`,
`infra.worker_lost`, `infra.platform`, `cancelled`; REST
`?failure_class=`). Page with `--limit` and `--offset`.

`jobs metrics` accepts `--metric NAME` (repeat), `--from` and `--to`
(RFC 3339), and `--max-points`.

`submit` accepts `--overlay FILE` (`-o`, repeat), `--allow-secret-overrides`,
`--wait` (`-w`), and `--poll-interval` (seconds, default 5).

```bash
reactorcide jobs list --status failed --limit 20
reactorcide jobs get 019fc939-397e-7974-a099-6e8d29c760b8 --format yaml
reactorcide jobs cancel 019fc939-397e-7974-a099-6e8d29c760b8
```

When the coordinator refuses a job, `submit` shows the error code and the
message: `API error (400 image_alias): <message>`. An image alias refusal
names the alias, the key, the value of the job, the value of the alias, and
the fix. See
[Image Aliases: Job creation refusals](./image-aliases.md#job-creation-refusals).

`jobs retry` of a job with an image alias uses the current pin of the alias.
The new job shows `image_retry_of` when the image changed. `jobs get` shows
`image_display` and, for such a retry, `image_retry_of` in the table output.
See [Image Aliases: Retry and rerun](./image-aliases.md#retry-and-rerun).

For a workflow file, use `--max-parallel` to set the local concurrency. Use
`--event` to select the workflow trigger. Use `--changed-file` to supply paths
for path filters. A selected workflow is skipped when its `on` filter does not
match the event.
The command writes `reactorcide-workflow-summary.json` in the local workspace.
This file contains the node states and the final workflow variable names. It
does not contain variable values.

`run-local` has these other flags:

| Flag | Purpose |
| --- | --- |
| `--dry-run` | Show the run plan. Do not run the job. |
| `--backend docker\|containerd\|kubernetes\|vm` | Select the runtime. The default is `docker`. `vm` works only on macOS and Windows. |
| `--input FILE` (`-i`, repeat) | Merge overlay files with the job. The first file has the highest priority. |
| `--allow-secret-overrides` | Let an overlay replace a secret reference with a plain value. |
| `--code-url URL`, `--code-ref REF` | Clone a repository into `/job/src`. Do not mount `--source-dir`. |
| `--as-runner` | Run as the runner uid `1001:1001`. |
| `--user uid[:gid]\|runner\|root\|host` | Run as this user. You cannot use it with `--as-runner`. |
| `--eval-image IMAGE` | Use this image to evaluate a workflow. |

The repository that contains the current directory is both the application
source and the trusted CI tree by default. Use `--source-dir` and `--ci-dir`
to select different local trees. Reactorcide mounts the CI tree read-only.

Use `local-context sync` to copy non-secret project defaults from a
coordinator. The context file does not contain the API token or secret values.

```bash
reactorcide local-context sync --project <project-id> --name dev
reactorcide run-local --context dev .reactorcide/workflows/test.yaml
reactorcide local-context show --name dev
reactorcide local-context remove --name dev
```

An offline run uses the last synchronized context. The CLI shows the age of
that context at the start of the run.

`run-local` sets the reserved cache values (`REACTORCIDE_CACHE_NAMESPACE`,
`REACTORCIDE_CACHE_WRITABLE`, `REACTORCIDE_ARTIFACT_NAMESPACE`) from the
context. The cache is read-only unless you give `--cache-writable`.
`run-local` stops a job at its `timeout_seconds` with the exit code 124.
`--ignore-timeout` turns this off. See
[Runtime Behavior](./runtime-behavior.md#job-facts-and-defaults).

To copy only the secrets that one workflow references, add
`--include-workflow-secrets`. The CLI writes the values to the encrypted local
secret store. It does not print the values. Add `--replace-secrets` to replace
an existing local value. The caller must own the project or administer its
organization. A non-user token must also have secret-management access. The
coordinator checks the secret grant for each selected workflow job name. The
coordinator records the reference names in the audit log. It does not record
the values.

The context file has an optional `overrides` block. A later synchronization
keeps this block.

```yaml
overrides:
  runner_image: registry.example.com/runner:test
  eval_image: registry.example.com/runner:test
  timeout_seconds: 1800
  checkout_mode: shared
```

Runtime values use this order, from highest priority to lowest priority:

1. Command options and overlay files.
2. Values in the context `overrides` block.
3. Values in the workflow or job.
4. Synchronized project values.
5. Built-in defaults.

The coordinator sends one effective, non-secret context response for the
project. The response includes the runner defaults, the execution profile
limits, the worker class, and secret references. A user must have a role in
the project. The context file does not store the API token. The endpoint is
`GET /api/v1/projects/<project-id>/local-context`.

## Worker Administration Commands

| Command | Purpose |
| --- | --- |
| `workers list [--pool ID] [--enrollment-token ID] [--on-deactivated-token]` | List enrolled workers. Columns: `ID`, `HOST`, `OS/ARCH`, `STATUS`, `IMAGES`, `POOL`, `TOKEN`, `LEASES`, `LAST SEEN`. |
| `workers set-status <worker-id> active\|quarantined\|disabled` | Activate, quarantine, or disable a worker. |
| `workers drain <worker-id>` | Stop new work on a worker. |
| `workers pools list [--org ORG]` | List pools. |
| `workers pools create <name> [--org ORG] [--description TEXT]` | Create a pool. Without `--org`, the pool is global. |
| `workers pools update <pool-id> [--name N] [--description TEXT]` | Change a pool. |
| `workers pools delete <pool-id>` | Delete a pool. |
| `workers tokens create <pool-id> --output-file FILE [--name N]` | Create an enrollment token. |
| `workers tokens list <pool-id>` | List the enrollment tokens of a pool. |
| `workers tokens deactivate <token-id> --confirm-permanent` | Deactivate an enrollment token. |
| `workers queues list` | List queues. |
| `workers queues create --characteristic KEY=VALUE ... [--display-name N]` | Create a queue for a set of characteristics (`-c`, repeat). |
| `workers queues rename <queue-id> <display-name>` | Rename a queue. |
| `workers queues delete <queue-id>` | Delete a queue. |
| `workers classes list --org ORG` | List worker classes. |
| `workers classes put <name> --org ORG [--protected] [--pool ID ...]` | Create or replace a worker class. |
| `workers classes delete <name> --org ORG` | Delete a worker class. |
| `workers classes set-pool <class-name> <pool-id> --org ORG [--revoke]` | Grant a pool to a class. `--revoke` removes the grant. |

The token create command requires `--output-file`. It creates a new file with
mode `0600`. It never prints the raw token. `workers tokens list` shows the ID,
the name, and the active worker count of each token.

`workers tokens deactivate <token-id> --confirm-permanent` deactivates a token
permanently. The command refuses to run without `--confirm-permanent`. It
prints a warning when workers still run on the token.

`workers list` shows the enrollment token (ID and name) of each worker.
`--enrollment-token <token-id>` shows only the workers of one token, and
`--on-deactivated-token` shows only the workers that run on a deactivated
token. See [Workers](./workers.md#enrollment-token-rotation).

```bash
reactorcide workers pools create windows-hyperv
reactorcide workers tokens create <pool-id> \
  --name windows-host-1 \
  --output-file ./enrollment-token
reactorcide workers list --on-deactivated-token
reactorcide workers tokens deactivate <token-id> --confirm-permanent
```

The `IMAGES` column shows the image status of each worker that uses image
sets: `healthy`, `updating images`, or `image update
failed` with the reason. See
[Image Aliases](./image-aliases.md#worker-image-states).

Each `workers` command accepts a browser session or an API token. The API
does the same permission check for both. A token gets the role of its owner,
limited to the organizations and capabilities of the token.

- `workers list`, `workers pools list`, and `workers queues list` need
  `workers:read` or `workers:manage` for the organization of the pool. To
  see workers gives no control of them.
- `workers set-status`, `workers drain`, `workers tokens list`, and the
  other pool, queue, and token commands need `workers:manage`.
- Without `--pool` (all pools), and for a global pool or a queue, the owner
  of the token must be a global admin, and the token must be for all
  organizations.
- `workers tokens list` gives metadata only. It never shows a token value.

The other management commands that use the UI service (groups, roles,
projects, secrets, CI policy) also accept a token. See
[UI Authentication: Sessions and API tokens](./ui-auth.md#sessions-and-api-tokens)
for the capability of each operation.

```bash
reactorcide workers list --token "$(cat ~/.reactorcide-token)" --pool <pool-id>
```

## Image Alias Commands

| Command | Purpose |
| --- | --- |
| `images alias list` | List the image aliases of an organization or a project. |
| `images alias get` | Show one image alias. |
| `images alias set` | Create an alias, or give it a new pinned reference (a rollout). `--force` uses the new reference at once. |
| `images alias delete` | Delete an image alias. The coordinator refuses while a job that is not terminal uses the alias. |
| `images rollout status` | Show the workers that serve each alias and their image state. |

Each command needs `--org`. Add `--project <project-id>` for the project
scope. `images alias set NAME REF` needs a pinned `REF` (`@sha256:<digest>`).
For a new alias, give `--os` and `--arch`. You can also give `--kind vm\|container`
and `--characteristic KEY=VALUE` (repeat). These four values cannot change
after the alias exists. `images alias list` shows `NAME`, `SCOPE`, `KIND`,
`PLATFORM`, `ACTIVE`, and `PENDING`. `images rollout status [NAME]` shows
`ALIAS`, `WORKER`, `HOST`, `ONLINE`, `IMAGE STATUS`, `VERSION`, `ACTIVE`, and
`PENDING`. See [Image Aliases and Worker Image Sets](./image-aliases.md).

```bash
reactorcide images alias set --org acme --os windows --arch amd64 \
  windows-vs2022 oci://registry.example.com/vm/windows-vs2022:2026.11@sha256:<digest>
reactorcide images rollout status --org acme windows-vs2022
```

## Workflow Commands

| Command | Purpose |
| --- | --- |
| `workflows list` | List workflows. |
| `workflows get <workflow-id>` | Show one workflow and its job counts. |
| `workflows cancel <workflow-id>` | Cancel a workflow and its unfinished jobs. |
| `workflows retry <workflow-id>` | Run a failed or cancelled workflow again as a new instance. Optional shifts. |
| `workflows rerun <workflow-id>` | Run a finished workflow (also a completed one) again as a new instance. Optional shifts. |
| `workflows retry-unsuccessful <workflow-id>` | Run the failed and cancelled jobs again in the same instance. Optional shifts. |

Filter `workflows list` with `--status`, `--project-id`, and `--user-id`.

`workflows retry` keeps the original instance for history. `workflows
retry-unsuccessful` changes the original instance. Both retry forms replay
the recorded workflow and node authority, including the exact CI SHA and
execution profile. A retry does not evaluate a newer coordinator policy.

### Shifts

`jobs retry`, `workflows retry`, `workflows rerun`, `workflows
retry-unsuccessful`, `approvals retry`, and `approvals rerun` accept shifts.
A shift is the only change that a retry or a re-run can make. The new run
keeps the authority of the original run.

| Flag | Changes |
| --- | --- |
| `--cpu` | CPU limit, for example `4` or `2500m` |
| `--cpu-request` | CPU request |
| `--memory` | Memory limit, for example `8Gi` |
| `--storage` | Ephemeral storage limit, for example `20Gi` |
| `--storage-request` | Ephemeral storage request |
| `--timeout` | Timeout, for example `2h` or `7200` (seconds) |

The CLI checks the values before it sends the request. The coordinator
refuses a shift over the `rerun_shift_limits` setting or over an execution
profile ceiling. See
[Retries, Re-runs, and Shifts](./runtime-behavior.md#retries-re-runs-and-shifts).

```bash
reactorcide workflows list --status failed
reactorcide workflows retry-unsuccessful 019fc939-397e-7974-a099-6e8d29c760b8
reactorcide jobs retry 019fc939-397e-7974-a099-6e8d29c760b9 --memory 8Gi --timeout 2h
reactorcide workflows rerun 019fc939-397e-7974-a099-6e8d29c760b8 --cpu 4
```

### Manual Runs

`reactorcide run` starts a new evaluated run on a ref. The coordinator
resolves the commit and applies the normal policy of that ref. Give `--ref`
(a branch name, `refs/heads/NAME`, or `refs/tags/NAME`) or `--pr`. Add
`--workflow` to run only one workflow. The token needs `jobs:submit` and the
retry permission of the project. See
[Manual Runs on a Ref](./events-and-schedules.md#manual-runs-on-a-ref).

```bash
reactorcide run --project my-app --ref main
reactorcide run --project my-app --ref refs/tags/v1.2.0 --workflow release
reactorcide run --project my-app --pr 42
```

## Project Commands

A project connects a repository to Reactorcide.

| Command | Purpose |
| --- | --- |
| `projects list` | List projects. |
| `projects get <project-id>` | Show one project. |
| `projects create` | Create a project. |
| `projects update <project-id>` (or `projects set`) | Change the given fields of a project. |
| `projects delete <project-id>` | Delete a project. |

Give the fields with flags, or with `--file` and a YAML or JSON document.
The document uses the same field names as the API. Flags win over the file.
`--vcs-token-secret` and `--webhook-secret` set one secret reference
(`path:key`). Use `--file` for the secret reference maps `vcs_token_secrets`
and `webhook_secrets`, which have no flags. Other flags are `--description`,
`--allowed-event-type` (repeat), `--ci-source-type`, `--ci-source-url`,
`--ci-source-ref`, `--runner-image`, `--timeout-seconds`, and
`--queue-name`.

```bash
reactorcide projects create --file example-repo.yaml
reactorcide projects update <project-id> --enabled --target-branch main
reactorcide projects update <project-id> --checkout-mode shared
reactorcide projects set <project-id> --infra-retries 2
reactorcide projects create --name app --repo-url github.com/acme/app --org acme \
  --dynamic-jobs deny --draft-prs skip --infra-retries inherit --report-mode statuses_only
```

The event settings `--draft-prs`, `--dynamic-jobs`, `--duplicate-events`,
and `--infra-retries` (0-5, or `inherit`) work with `create` and `update`.
`--report-mode` works with `create` only; it sets the reporting setting of
the new project (use `reporting set` later). The coordinator writes the
audit event `project.event_settings` for each change of an event setting,
with the old and the new value.

`--checkout-mode` accepts `isolated` or `shared`. The shared mode uses one
non-shallow, blob-filtered Git object store during pull-request evaluation. It
stages small base and head CI views from that store. Set an empty value on an
update to remove the project override. If a project does not set this field,
the coordinator uses `REACTORCIDE_CHECKOUT_MODE`. The default is `isolated`.

See [Connect a VCS Repository](./vcs-setup.md) for a full example.

`update` changes only the fields you give. Most text fields cannot be set back
to empty with a flag. `--checkout-mode ""` is an explicit exception. It removes
the project checkout override.

`update` also sets the project event settings. Each one accepts `inherit` to
use the organization value:

| Flag | Values |
| --- | --- |
| `--draft-prs` | `inherit`, `run`, `skip`, `skip_when_shedding` |
| `--dynamic-jobs` | `inherit`, `allow`, `deny` |
| `--duplicate-events` | `inherit`, `run`, `skip` |

`--job-command` replaces the eval command of the project. Only a global
administrator (for example the admin API token) can set it. See
[Declarative Pipelines](./organizations-and-ci-policy.md#declarative-pipelines).

## Required Set Commands

The required set names the workflows that block a merge. See
[Required Set](./required-set.md).

| Command | Purpose |
| --- | --- |
| `required-set enable --project P` | Turn the set on. Every known workflow goes into the set. An org admin. |
| `required-set disable --project P` | Turn the set off. Every workflow blocks again. An org admin. |
| `required-set list --project P` | Show each workflow and its state: required, advisory, or quarantined (with the expiry). |
| `required-set proposals --project P` | List the proposals in all states. |
| `required-set propose --project P [--branch GLOB] [--reason T] [--approve] <workflow-id>` | Propose a promotion. `--approve` approves at once (an approver only). |
| `required-set remove --project P [--branch GLOB] [--reason T] [--approve] <workflow-id>` | Propose a demotion. The same approvers decide. |
| `required-set approve\|reject\|withdraw --project P [--reason T] <proposal-id>` | Decide a pending proposal. |
| `required-set quarantine --project P (--for 7d \| --until TIME) --reason T [--branch GLOB] <workflow-id>` | Remove an entry until the expiry. |
| `required-set unquarantine --project P [--branch GLOB] <workflow-id>` | End a quarantine early. |
| `required-set approvers --project P (--approver S ... [--rule 'GLOB [GLOB...]=SUBJECT [SUBJECT...]'] \| --inherit)` | Set the project approvers. An org admin. |
| `required-set org-settings --org O [--approver S] [--rule 'GLOB [GLOB...]=SUBJECT [SUBJECT...]'] [--max-quarantine-days N]` | Set the organization defaults. An org admin. |

The admin API token can run every command, so a deployment with no logins
can use the feature.

## Reporting and Ownership Commands

Report modes, the agreement report, the code owners import, and path
acceptances. See [Reporting and Ownership](./reporting-and-ownership.md).

| Command | Purpose |
| --- | --- |
| `reporting show --project P` | Show the effective settings and where each value comes from. |
| `reporting set --project P [--report-mode M] [--agreement-context C ...] [--agreement-contexts-file F] [--codeowners-import off\|on] [--codeowners-ttl S] [--path-acceptance off\|warn\|enforce] [--path-owner 'PATTERN=SUBJECT ...' ...] [--inherit NAME ...] [--clear-agreement-contexts] [--clear-path-owners]` | Change project values. A value that you do not give stays. `report_mode` and the agreement contexts: a project owner or an org admin. The other values: an org admin. |
| `reporting org-show --org O` | Show the organization values. |
| `reporting org-set --org O [same value flags]` | Replace the organization values. An org admin. |
| `reporting agreement --project P [--since 7d] [--limit N] [--all]` | Show the agreement with the other CI system: the rate and the disagreements. |
| `path-acceptances list --project P [--state S]` | List requests and acceptances. |
| `path-acceptances request --project P --pattern PATTERN --owner SUBJECT [--reason T] [--accept] <workflow-id>` | Ask the owner of an area to accept a path dependency. `--accept` accepts at one time (the owner or an org admin). |
| `path-acceptances accept\|reject --project P [--reason T] <acceptance-id>` | Decide a request (the owner or an org admin). |
| `path-acceptances revoke --project P [--reason T] <acceptance-id>` | End a request or an acceptance. |

A comma splits a flag value. Put an agreement context with a comma in the
file of `--agreement-contexts-file`. The admin API token can run every
command.

## Workflow Role Commands

A workflow role makes a group, a user, or a principal an owner of one
workflow of a project. See [Workflow Owners and Workflow Roles](./organizations-and-ci-policy.md#workflow-owners-and-workflow-roles).

| Command | Purpose |
| --- | --- |
| `roles assign --org O --project P --workflow W (--group G \| --user U \| --principal ID)` | Give the workflow `owner` role. |
| `roles list --org O --project P [--workflow W]` | List the workflow roles of a project. |
| `roles revoke --org O ASSIGNMENT` | Remove a workflow role. |
| `roles owners --org O --project P` | List the trusted owner and contact of every workflow. |

## Schedule Commands

A schedule runs a workflow definition on the head commit of a branch. See
[Schedules](./events-and-schedules.md#schedules).

| Command | Purpose |
| --- | --- |
| `schedules list --project P` | List the schedules of a project. |
| `schedules create --project P --name N --branch B --workflow W --cron EXPR [--timezone TZ] [--disabled]` | Create a schedule. `--cron` is a five-field expression or a descriptor such as `@daily`. The default time zone is `UTC`. `--workflow` is a workflow ID or a file path under `.reactorcide/workflows/`. |
| `schedules update <schedule-id> [--name N] [--branch B] [--workflow W] [--cron EXPR] [--timezone TZ]` | Change a schedule. Only the flags that you give change. |
| `schedules enable\|disable <schedule-id>` | Enable or disable a schedule. After `enable`, the coordinator plans the next fire from now. It does not run the missed times. |
| `schedules delete <schedule-id>` | Delete a schedule and its fire history. |
| `schedules fires <schedule-id> [--limit N]` | List the newest fires (1-200, default 20). |

`--project` accepts a project ID, a project name, or a repository URL.

## Subscription Commands

| Command | Purpose |
| --- | --- |
| `subscriptions create --org O --name N --url URL [--scope org\|project\|workflow\|owner_group] [--project P] [--workflow W] [--owner-group G] [--event-type T ...] [--result R ...] [--ref-class C ...] [--actor-kind K ...] [--max-attempts N] [--owner PRINCIPAL] [--secret-file F] [--disabled]` | Create a subscription. The signing secret shows one time. |
| `subscriptions list --org O [--project P]` | List the subscriptions. |
| `subscriptions show --org O <subscription-id>` | Show a subscription. |
| `subscriptions update --org O <subscription-id> [--url URL] [--event-type T ...] [--clear FILTER] [--rotate-secret] [--secret-file F] ...` | Change a subscription. Only the flags you give change. |
| `subscriptions enable\|disable --org O <subscription-id>` | Enable or disable. Disable sends the open deliveries to dead letter. |
| `subscriptions delete --org O <subscription-id>` | Delete a subscription and its delivery log. |
| `subscriptions deliveries --org O <subscription-id> [delivery-id] [--status S] [--limit N]` | The delivery log, or one delivery with its attempts. |
| `subscriptions replay --org O <subscription-id> (<delivery-id> \| --all-dead-letters)` | Send deliveries again. |
| `subscriptions test --org O <subscription-id>` | Send one `test.ping` event now. Exits 1 when it is not delivered. |

`subscriptions create` and `subscriptions update` also accept
`--failure-class C` (repeat), for example `job`, `oom`, `timeout`. Then only
events with one of these failure classes are sent. `--clear failure-class`
removes the filter.

An org admin manages org subscriptions. A project owner manages project and
workflow subscriptions. The admin API token can run every command. See
[Outbound Events](./outbound-events.md).

## Alert Commands

Pipeline owner alert rules. Only the failure classes `job`, `timeout`, and
`oom` count. See [Observability](./observability.md#alerts).

| Command | Purpose |
| --- | --- |
| `alerts create --org O <name> --scope workflow\|project\|owner_group --condition pass_rate_below\|duration_regression [--project P] [--workflow W] [--owner-group G] [--threshold X] [--factor F] [--window-hours N] [--baseline-days N] [--min-runs N] [--ref-class B\|all] [--resolve-margin M] [--fire-after N] [--resolve-after N] [--disabled]` | Create a rule. |
| `alerts list --org O` | List the rules with their state, last value, and runs. |
| `alerts show --org O <name>` | Show a rule and its last transitions. |
| `alerts update --org O <name> [--condition C] [--enabled true\|false] [rule flags]` | Change a rule. The scope cannot change. A change of the condition or the ref class resets the state. |
| `alerts delete --org O <name>` | Delete a rule. |

A project owner manages a rule with a project. An org admin manages an
`owner_group` rule with no project. Subscribe to `alert.firing` and
`alert.resolved` to get notifications.

## SLO Commands

| Command | Purpose |
| --- | --- |
| `slo list --org O` | List the targets, stored and built-in. |
| `slo set --org O <sli> --objective X [--lane L] [--threshold-seconds S] [--window-days N]` | Set a target. `sli` is `eval_latency`, `queue_wait`, `infra_failure_rate`, or `availability`. Only `queue_wait` and `infra_failure_rate` have lane targets. |
| `slo delete --org O <sli> [--lane L]` | Delete a stored target. The built-in target applies again. |
| `slo status --org O` | Show each SLI: compliance, p95, total, the error budget that remains, and its state. |

An org admin sets targets. The admin API token can run every command.

## Rollup Commands

| Command | Purpose |
| --- | --- |
| `rollups show --org O [--granularity hour\|day] [--days N] [--project P] [--workflow W] [--owner-group G] [--lane L] [--ref-class B]` | The job rollup rows that match, and their totals (JSON or YAML). |
| `rollups dashboard --org O <pipeline\|infrastructure\|usage\|status> [--days 7\|30\|90] [--project P] [--attribution owner\|actor]` | A dashboard as JSON or YAML. `status` shows the watermarks of the rollup loop. |

## GitHub App Commands

| Command | Purpose |
| --- | --- |
| `github-apps add --app-id N --private-key-file F --webhook-secret-file F [--slug S] [--name N] [--client-id C] [--default-org ORG] [--open-registration] [--no-verify] [--replace]` | Register an App. The key and the secret are read from files and never printed. Without `--open-registration`, a new project of an account with no mapping is created disabled and waits for approval. |
| `github-apps list` | List the Apps with headroom. The recommended App has the most headroom. |
| `github-apps show <app-id>` | Show an App and its install URL. |
| `github-apps recommended` | Show the App that new installations must use. |
| `github-apps enable\|disable <app-id>` | Enable or disable an App. |
| `github-apps installations <app-id>` | List the installations and their repositories. |
| `github-apps sync <app-id>` | Read the installations from GitHub and register projects. |
| `github-apps map-account --org ORG <account-login>` | Register new projects of an account in an organization. |
| `github-apps unmap-account <account-login>` | Remove an account mapping. |
| `github-apps accounts` | List the account mappings. |

Each command needs a global administrator. The admin API token can run every
command. See [GitHub App](./github-app.md).

## Diagnosis Commands

| Command | Purpose |
| --- | --- |
| `diagnosis setup --org O --principal NAME --url URL [--scope org\|project] [--project P] [--failure-class C ...] [--name N] [--secret-file F]` | Create the subscription of a diagnosis tool: `node.post_complete` events with the failure class `job`, `oom`, or `timeout`. The principal must be a service principal. It owns the subscription. The signing secret shows one time. |
| `diagnosis post (--job J \| --workflow W [--node N]) --title T [--body B \| --body-file F] [--confidence low\|medium\|high] [--category C] [--link NAME=URL ...] [--shift KEY=VALUE ...]` | Post the diagnosis entry of the caller's principal. A new post replaces it. `--body-file -` reads standard input. |
| `diagnosis list (--job J \| --workflow W)` | List the entries of a job, or of a workflow and the jobs of its nodes. |
| `diagnosis delete (--job J \| --workflow W [--node N])` | Remove the entry of the caller's principal and its pull request section. |

A post needs `diagnosis:write` and a role in the scope of the job. `--shift`
uses the keys of a retry (`cpu`, `cpu_request`, `memory`, `storage`,
`storage_request`, `timeout`). A suggested shift is not applied until
somebody retries with it. See [Failure Diagnosis](./diagnosis.md).

## Token Commands

| Command | Purpose |
| --- | --- |
| `token create` | Create a token. Writes to the database. |
| `token list` | List your tokens. |
| `token delete <token-id>` | Delete a token. |

`token create` needs database access, not an API token, so you can use it to
make the first token. Give it `--db-uri` or set `REACTORCIDE_DB_URI`. The
token value prints one time. This is an instance-operator command. It does not
apply caller-token authority checks because it writes directly to PostgreSQL.

Use repeated `--org` and `--capability` flags to create a limited service
token. [API Tokens](./organizations-and-ci-policy.md#api-tokens) lists the
capability names. Use `--as-user USERNAME` to create a delegated user token. An omitted
organization list means all organizations. An omitted capability list means
all capabilities. This is the same in all auth modes. On a no-login instance
(`REACTORCIDE_UI_AUTH_MODE=none`), service principal tokens also get all
permissions by default. See
[No-login instances](./ui-auth.md#no-login-instances-single-admin). This is
not for production.

## Secret Commands

| Command | Purpose |
| --- | --- |
| `secrets init` | Prepare secret storage. |
| `secrets set <path> <key>` | Set a secret value. |
| `secrets get <path> <key>` | Print a secret value. |
| `secrets delete <path> <key>` | Delete a secret. |
| `secrets list <path>` | List the keys at a path. |
| `secrets list-paths` | List all paths. |
| `secrets get-multi <path:key>...` | Get more than one secret in one operation. |
| `secrets master-keys list\|create\|rotate\|decommission\|sync-primary` | Administer master keys. Admin only. See [Master Key Administration](./secrets.md#master-key-administration). |

`secrets set` reads the value from `--value` or, with `--stdin`, from
standard input. Use `--stdin` so that the value is not in the shell history.

`secrets` uses the coordinator API when `REACTORCIDE_API_URL` is set.
Otherwise it uses local file storage and asks for
`REACTORCIDE_SECRETS_PASSWORD`.

See [Secrets](./secrets.md).

## Secret Grant Commands

| Command | Purpose |
| --- | --- |
| `secret-grants list` | List all grants that the token can manage. |
| `secret-grants get <name-or-id>` | Show one grant. |
| `secret-grants set <name>` | Create or change a grant. |
| `secret-grants delete <name-or-id>` | Delete a grant. |
| `secret-grants apply --file <file>` | Apply a set of grants from a file. |

`secret-grants set <name>` takes `--secret-path` and `--secret-match`
(`exact`, `prefix` (default), `glob`, `regex`). It also takes `--job-name`
and `--job-match` (`any` (default), `exact`, `prefix`, `glob`, `regex`), and
`--description`.

Use repeated `--execution-profile` and `--ci-origin` options with
`secret-grants set` to limit a grant. Valid CI origins are `base` and `head`.
Use `--clear-execution-profiles` or `--clear-ci-origins` to remove a limit.

Use repeated `--ref-class-any` and `--ref-class-none` options to limit a
grant by ref class. Use `--clear-ref-classes` to remove these conditions. See
[Ref Classes](./ref-classes.md).

`--delivery env` puts the value in the job environment. `--delivery proxy`
gives the job a placeholder. The egress proxy adds the value only for the
`--proxy-host` patterns (repeat). `--proxy-header` sets the request header
(default `Authorization`). `--proxy-header-format` sets the full header
value, with `{}` for the secret, for example `"Bearer {}"`. See
[Egress Proxy](./egress-proxy.md).

`secret-grants apply --file FILE` accepts `--dry-run` to show the changes
without a write. `--prune` deletes the grants that the file does not have,
in each scope that the file names.

By default, `secret-grants list` includes all project and organization grants
that the token can manage. Use `--project` to select one project. Use
`--global-only` to list only organization-wide grants.

`--project`, `--api-url`, and `--token` are options of `secret-grants`. Put
them before the subcommand:

```bash
reactorcide secret-grants --project <project-id> set deploy-key \
  --secret-path deploy/ --ref-class-any protected
```

See [VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md).

## VM Image Commands

| Command | Purpose |
| --- | --- |
| `vm-image build macos` | Build a derived macOS VM image. |
| `vm-image build windows` | Build a derived Windows VM image (Hyper-V). |
| `vm-image publish` | Push an image to an OCI registry or S3. |
| `vm-image pull` | Pull an image from an OCI registry or S3 to the local cache. |
| `vm-image cache prune` | Remove unused cached images. |
| `vm-image registry login --username U --password-stdin REGISTRY` | Store registry credentials. The password comes from standard input. |
| `vm-image registry logout` | Remove registry credentials. |

`vm-image publish`, `pull`, `cache prune`, and both `build` commands accept
`--auth-file` (a Docker-compatible credential file), `--plain-http HOST`
(development only), `--s3-endpoint`, and `--s3-region` for `s3://`
references. Run `reactorcide vm-image build windows --help` or
`reactorcide vm-image build macos --help` for the build flags.

See [VM Image Operations](./vm-images.md) and
[VM Images Quick Start](./vm-images-quickstart.md).

## Organization and Policy Commands

Use `reactorcide orgs create`, `list`, `get`, `update`, `set-default`, and
`delete` to manage organizations. Use `reactorcide projects create --org NAME` to select an
organization. If you omit `--org`, the coordinator uses the default
organization.

| Command | Action |
| --- | --- |
| `orgs create NAME [--display-name TEXT] [--private] [--defaults-profile secure\|legacy]` | Create an organization. The default profile is `secure`. |
| `orgs update NAME [--display-name TEXT] [--status STATUS] [--private] [--draft-prs VALUE] [--dynamic-jobs VALUE] [--duplicate-events VALUE] [--infra-retries N]` (or `orgs set`) | Change the given fields. A flag that you do not give keeps the current value. `--infra-retries` is 0-5. |
| `orgs set-defaults NAME --profile secure\|legacy` | Write the settings of a defaults profile: `run_gate`, `dynamic_jobs`, the `cache_write_classes` of the `standard` profile, and the built-in CI policy. See [Defaults Profiles](./organizations-and-ci-policy.md#defaults-profiles). |
| `orgs vcs-owners get\|set NAME [--owner LOGIN ...]` | The VCS owners whose teams the group sync may read with the global VCS credential. Global admin only. See [Principals](./principals.md#credentials). |
| `orgs get NAME` | Show the organization, with its defaults profile, `draft_prs`, `dynamic_jobs`, and shed level. |
| `orgs list` | List the organizations. |
| `orgs set-default NAME` | Make the organization the default organization. |
| `orgs delete NAME --replacement ORG --yes` | Delete the organization, its role assignments, and the resources that it owns. `--replacement` is necessary. It becomes the default organization. The coordinator refuses when the organization has a job or a workflow that is not finished. The token needs `organizations:manage` for both organizations. |

Use `reactorcide shed` to set shed levels (overload protection). See
[Shed Levels](./events-and-schedules.md#shed-levels).

| Command | Action |
| --- | --- |
| `shed get [--org ORG]` | Show the global level, or the level in force for an organization and its actions |
| `shed set --level normal\|degraded\|critical [--org ORG] [--reason TEXT]` | Set the global level (global administrator) or the level of an organization (organization administrator) |
| `shed set-actions --org ORG --file FILE` | Replace the actions of each level for an organization |
| `shed set-actions --org ORG --reset` | Use the built-in actions |

Use `reactorcide env-defaults`, `reactorcide image-policy`, and
`reactorcide job-links` for environment defaults, the organization image
policy, and job links. See [Builds, Caches, and Job
Settings](./builds-and-caches.md).

| Command | Action |
| --- | --- |
| `env-defaults list --org ORG [--worker-class CLASS] \| --project ID` | List the environment defaults of one scope |
| `env-defaults set (scope flags) KEY=VALUE` | Set one default. `REACTORCIDE_*` and `RC_WF*` keys are refused. |
| `env-defaults unset (scope flags) KEY` | Remove one default |
| `image-policy get --org ORG` | Show the image policy |
| `image-policy set --org ORG [--enabled] [--allow PREFIX ...] [--require-digest]` | Replace the image policy (`policies:manage`) |
| `job-links JOB_ID` | List the named links that a job published |

Use `reactorcide artifacts`, `reactorcide tests`, and `reactorcide
provenance` for job artifacts, test results, test quarantine, and signed
provenance. See [Artifacts and Test Results](./artifacts-and-tests.md) and
[Provenance](./provenance.md).

| Command | Action |
| --- | --- |
| `artifacts list JOB_ID` | List the artifacts of a job with their sizes, SHA-256 digests, and states |
| `artifacts download JOB_ID PATH_OR_ID [-o FILE]` | Download one artifact (`-o -` for stdout) and compare its digest with the recorded digest |
| `tests results JOB_ID [--failed] [--limit N]` | Show the test summary and the results of a job, failures first |
| `tests flaky --project ID [--days N]` | List the flaky tests of a project |
| `tests quarantine list --project ID [--all]` | List the test quarantines of a project |
| `tests quarantine add --project ID (--test-id ID \| --suite S --class C --name N) --reason TEXT (--for 7d \| --until TIME) [--owner PRINCIPAL_ID] [--workflow SECURITY_ID]` | Quarantine one test. The admin token must give `--owner`. |
| `tests quarantine remove --project ID QUARANTINE_ID` | End a quarantine before its expiry |
| `provenance get JOB_ID [-o FILE]` | Write the DSSE provenance envelope of a job |
| `provenance verify FILE \| --job JOB_ID [--keys FILE] [--require-ref-class CLASS ...] [--require-ci-origin base] [--require-builder ID] [--require-repository R] [--require-project ID] [--require-org ORG] [--require-workflow SECURITY_ID] [--require-coordinator-hashed] [--subject sha256:HEX] [--allow-any-repository]` | Verify the signature, print the facts, and fail when a requirement is not met. `--subject` needs `--require-repository` or `--require-project`, or `--allow-any-repository` |

Use repeated `reactorcide token create --org NAME` and `--capability VALUE`
flags to limit a token. Use `--as-user USERNAME` for a delegated user token.
The CLI shows the raw token only when it creates the token.

Use `reactorcide profiles list --org NAME` to list execution profiles. Use
`profiles get`, `apply`, and `delete` to manage them. The `apply` command reads
a YAML profile file. The token needs `policies:manage` for the organization.

Use `reactorcide policy get`, `set`, and `delete` to manage the coordinator CI
policy for a project. When a project stores no policy and its organization
has the secure defaults profile, `policy get` shows the built-in policy,
marked `built-in`. The token needs `policies:manage` for the project
organization. Use a project ID when the token does not also have
`projects:read`. Use `--expected-revision` with `set` or `delete` when
concurrent updates are possible.

Use `reactorcide policy validate --file POLICY_FILE` to validate a local policy
file. Use `reactorcide policy explain --file POLICY_FILE --workflow ID` with
`--event`, `--base-branch`, `--head-repository`, `--actor`, `--approval`, and
`--ref-class` to inspect a decision without starting a job. Add one `--changed-path` option for each changed CI file.

Use `reactorcide approvals COMMAND --project PROJECT --pr N` to control a
pull request head. The commands are `approve-ci`, `override-ci`, `accept`
(with an optional `--label`), `revoke`, `retry`, and `rerun`. Use
`reactorcide approvals show` to see the approval actions and the required
reviews of the current head. Add `--head-sha SHA` to refuse a command when the
head changed. The CLI needs a user API token, or a service principal token
from `principals tokens create` (or a federation exchange) with
`workflows:control`. The coordinator refuses a token from `token create`
(also the admin token), a job token, and the wide token of a no-login
instance (HTTP 403).

A pull request comment gives the same commands:

```text
/reactorcide approve-ci
/reactorcide override-ci
/reactorcide accept [LABEL]
/reactorcide revoke
/reactorcide retry [SHIFTS]
/reactorcide rerun [SHIFTS]
/reactorcide promote WORKFLOW
/reactorcide demote WORKFLOW
```

`promote` and `demote` propose a required set change for every target
branch. WORKFLOW is the workflow security ID. See
[Required Set](./required-set.md).

SHIFTS are `key=value` words: `cpu`, `cpu_request`, `memory`, `storage`,
`storage_request`, and `timeout`. For example
`/reactorcide retry memory=8Gi timeout=2h`. Any other word refuses the
command. `rerun` with no shifts evaluates the head again. `rerun` with shifts
runs the finished workflows of the newest evaluation again with their
recorded authority.

An approval binds to the head SHA. The settings decide who can give each
command. Use `reactorcide trust get --org ORG [--project PROJECT]` and
`reactorcide trust set --org ORG [--project PROJECT] --file FILE` to manage the
run gate, the approvers, the override profile, and the required reviews. See
[Run Gate, Approvals, and Required Reviews](./organizations-and-ci-policy.md#run-gate-approvals-and-required-reviews).

Use `reactorcide groups create|list|members|add-member|remove-member --org ORG`
to manage the groups behind `reactorcide_group:NAME`. Use
`reactorcide groups set-priority-shift --org ORG GROUP SHIFT` (-50 to 50) to
move the priority of the work of the group members
([Priority and Lanes](./priority.md#group-shifts)). Use
`reactorcide identities link --org ORG --provider github --login LOGIN
--username USER`, `identities list`, and `identities unlink <link-id>` to
manage verified VCS identity links. `list` and `unlink` need an
all-organizations token.

Groups linked to VCS teams (see
[Principals](./principals.md#groups-linked-to-vcs-teams)):

| Command | Action |
| --- | --- |
| `groups link-team --org ORG GROUP OWNER/SLUG` | Link the group to a GitHub team. The next sync adds the team members. |
| `groups unlink-team --org ORG GROUP OWNER/SLUG` | Remove the link. The members that only this team gives leave the group. A link ID also works. |
| `groups teams --org ORG GROUP` | List the linked teams: member count, last sync, next sync, last error |
| `groups sync --org ORG GROUP` | Sync the linked teams now and show the changes |
| `groups members --org ORG GROUP` | List the members with their source (`manual`, `vcs_sync`) and teams |
| `groups history --org ORG GROUP [--principal UUID] [--since TIME] [--limit N]` | Show the membership changes, newest first |

Use `reactorcide principals` to manage principals. The commands need an org
admin, or the admin token:

| Command | Action |
| --- | --- |
| `principals list --org ORG [--kind KIND]` | List the principals of the organization |
| `principals create --org ORG NAME [--display-name TEXT]` | Create a service principal |
| `principals show --org ORG PRINCIPAL` | Show identities, groups, tokens, and subjects |
| `principals disable` and `enable --org ORG PRINCIPAL` | Change the status of a service principal |
| `principals link --org ORG --type TYPE --value VALUE PRINCIPAL` | Link an identity (`vcs`, `linkkeys`, `vcsstar`) |
| `principals unlink --org ORG --type TYPE --value VALUE PRINCIPAL` | Remove an identity |
| `principals add-group` and `remove-group --org ORG PRINCIPAL GROUP` | Change group membership |
| `principals grant-role --org ORG --role ROLE [--project P [--workflow W]] PRINCIPAL` | Give a direct role. With `--workflow`, the role is `owner` of one workflow (the same as `roles assign --principal`). |
| `principals roles --org ORG PRINCIPAL` and `revoke-role ... ASSIGNMENT` | List or remove direct roles |
| `principals tokens create --org ORG PRINCIPAL [--name N] [--capability C]... [--expires-at RFC3339]` | Issue a token; it intersects with the roles. With no `--capability` on a no-login instance, the token gets all capabilities and all organizations (not for production, see [No-login instances](./ui-auth.md#no-login-instances-single-admin)) |
| `principals tokens list` and `revoke --org ORG PRINCIPAL [TOKEN]` | List or revoke tokens |

Use `reactorcide federation` to set up inbound federation. An org admin, or
the admin token, runs it. See [Inbound Federation](./federation.md).

| Command | Action |
| --- | --- |
| `federation issuers list\|show --org ORG [NAME]` | List or show trusted OIDC issuers |
| `federation issuers create --org ORG --issuer-url URL --audience AUD [--jwks-url URL] [--algorithm ALG]... [--clock-skew S] NAME` | Register an issuer |
| `federation issuers update\|delete --org ORG NAME` | Change or remove an issuer |
| `federation mappings list --org ORG --issuer ISSUER` | List claim mappings |
| `federation mappings create --org ORG --issuer ISSUER --principal P --condition CLAIM=VALUE\|CLAIM~GLOB... --capability C... [--ttl S] NAME` | Map claims to a service principal |
| `federation mappings show\|update\|delete --org ORG --issuer ISSUER NAME` | Show, change, or remove a mapping |
| `federation exchange --api-url URL --org ORG [--token-file FILE] [--principal P] [--capability C]... [--json]` | Exchange an external token. It needs no Reactorcide token. It prints only the issued token. `--json` prints the full response. |

Use `reactorcide oidc` to manage the OIDC issuer for jobs. See
[OIDC Tokens for Jobs](./oidc.md).

| Command | Action |
| --- | --- |
| `oidc keys list` | List the signing keys (global authority with `secrets:manage`) |
| `oidc keys rotate [--immediate]` | Add a signing key |
| `oidc keys retire KID` | Remove a key from the JWKS now |
| `oidc settings show --org ORG [--project P]` | Show the issuer setting and extra claims |
| `oidc settings set --org ORG [--project P] [--issuer allow\|deny\|inherit] [--claims-file FILE]` | Change them (org admin) |

A job gets a token with `runnerlib oidc-token --audience AUD`.

Use `reactorcide ref-classes` to manage ref class rules. Use `--org ORG` for
organization rules (an org admin) or `--project PROJECT` for project rules (a
project owner). See [Ref Classes](./ref-classes.md).

| Command | Action |
| --- | --- |
| `ref-classes rules list (--org ORG\|--project P)` | List the built-in, organization, and project rules |
| `ref-classes rules set (--org ORG\|--project P) --class C... [--event E]... [--branch G] [--tag G] [--base-branch G] [--pr B] [--fork B] [--default-branch B] [--position N] [--disabled] NAME` | Create or replace a rule |
| `ref-classes rules delete (--org ORG\|--project P) NAME` | Delete a rule |
| `ref-classes explain (--org ORG\|--project P) [--event-id ID \| --source S --event E --branch B --head-branch B --tag T --base-branch B --default-branch B --fork]` | Show the classes and the matched rules. `ref-classes rules explain` is the same command. |

Use `reactorcide lanes` to manage priority lanes. Use `--org ORG` for
organization lanes (an org admin) or `--project PROJECT` for project lanes (a
project owner). See [Priority and Lanes](./priority.md).

| Command | Action |
| --- | --- |
| `lanes list (--org ORG\|--project P)` | List the effective lanes in evaluation order |
| `lanes set (--org ORG\|--project P) --base N [--position N] [--class C]... [--not-class C]... [--event E]... [--draft B] [--kind eval\|work] [--retry] [--match-all] [--disabled] NAME` | Create or replace a lane. With a built-in name and no match flags, only the base changes. |
| `lanes delete (--org ORG\|--project P) NAME` | Delete a lane; the replaced lane applies again |
| `lanes explain (--org ORG\|--project P) [--job-id ID \| --class C... --event E --source S --draft --kind K --retry --actor P --requested N]` | Show the lane, the shifts, and the priority |

`PRINCIPAL` is a service principal name or a principal UUID. Use
`reactorcide resolve-principal --type TYPE --value VALUE [--org ORG]` to show
the principal of an identity and all of its linked identities. See
[Principals](./principals.md).

Use `GET /api/v1/audit?org=NAME` to export organization audit events. The
token needs `audit:read` for that organization. Set
`REACTORCIDE_AUDIT_RETENTION_DAYS` to control age-based audit retention.

See [Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md)
for complete examples, the policy schema, worker classes, execution profiles,
checkout precedence, GitHub status reporting, and upgrade checks.
