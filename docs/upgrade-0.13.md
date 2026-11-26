# Upgrade to Reactorcide 0.13

This guide is for an operator who upgrades a Reactorcide installation from
0.12.x (for example v0.12.13) to 0.13. Read all of it before you start.

The upgrade adds database migrations `000047` to `000074`, changes some
behavior at the default settings, and adds optional features. Most new
features are off until you turn them on.

## 1. Before the upgrade

1. Read [section 3](#3-behavior-changes-at-the-default-settings). Decide for
   each change if you keep the new default or set the old behavior.
2. Make sure that the `timeout_seconds` of each job is large enough. Docker,
   containerd, and VM workers now stop a job at its timeout.
3. Look for jobs that are stuck in `cancelling` or `submitted`. Finish or
   cancel them before the upgrade:

   ```bash
   reactorcide jobs list --status cancelling
   reactorcide jobs list --status submitted
   ```

4. Back up the coordinator database. A schema dump and a data dump are
   sufficient. A dump from a Spilo (Zalando operator) server restores into a
   plain PostgreSQL server. Ignore the errors for the extensions
   `pg_stat_kcache` and `set_user`: they are Spilo extensions and Reactorcide
   does not use them.
5. Back up the Corndogs file store (see [section 2.1](#21-corndogs-081)).
6. If you run macOS or Windows VM jobs, rebuild each VM image with the 0.13
   tools. The 0.13 worker cannot use an image without the guest daemon (see
   [section 3](#3-behavior-changes-at-the-default-settings)).
7. If you run macOS or Windows VM jobs, add an image alias for each VM
   image that a job uses, and change the job definitions to the alias
   name. Do this with the 0.13 coordinator before you upgrade the VM
   workers, and before new VM jobs are created. After the upgrade the
   coordinator refuses a VM job with a plain tag reference (no digest), and
   a pinned reference that no alias pins. See
   [Image Aliases and Worker Image Sets](./image-aliases.md).

## 2. Order of the upgrade

Do the steps in this order. Each step needs the step before it.

| Step | Part | Why this position |
| --- | --- | --- |
| 1 | Corndogs 0.8.1 | The 0.13 coordinator uses keyed submits and guarded tasks. Corndogs 0.7.x does not have these operations. |
| 2 | Coordinator 0.13 and web app 0.13, together | The coordinator applies migrations `000047` to `000074` when it starts. The web app and the coordinator use the same CSIL UI schema (`coordinator_api/csil/reactorcide-ui.csil`). The 0.13 UI pages use operations and fields that only a 0.13 coordinator has. Deploy both from the same release tag in the same rollout. |
| 3 | Workers 0.13 | A 0.13 worker uses worker operations that only a 0.13 coordinator has (for example `upload-artifact`). Upgrade the workers directly after the coordinator. Each VM job of a 0.13 coordinator has the characteristic `image_digest`, and a VM worker older than 0.13 does not send `image_digest`. Thus an old VM worker cannot claim VM jobs: VM jobs wait until a 0.13 VM worker has the image. |
| 4 | Runner image (`runnerbase`) 0.13 | The new runnerlib commands (artifacts, test results, `oidc-token`) need the 0.13 coordinator. |

The CSIL changes of the worker schema
(`coordinator_api/csil/reactorcide-worker.csil`) add only optional fields
and new operations. A 0.12 worker can still register with a 0.13
coordinator. It runs container jobs that name a plain image reference. It
gets no image set, so it cannot claim a job whose image is an alias (that
job has the characteristic `image_digest`). It gets no egress proxy lease. A 0.13 worker that starts before a 0.13
coordinator is ready tries the registration again with backoff.

The Helm chart changes the coordinator, the web app, and the worker
Deployments in one `helm upgrade`. Kubernetes rolls them at the same time.
This is permitted: the workers try again until the coordinator is ready.

### 2.1 Corndogs 0.8.1

The Helm chart includes the Corndogs chart 0.6.1 (server 0.8.1). The compose
files use the image `corndogs:0.8.1`.

Corndogs 0.8.1 sends a service error as a `ServiceError` reply variant. A
coordinator before 0.13.2 cannot read this reply. Upgrade Corndogs and the
coordinator together. The Helm chart and the compose files do this in one
step.

The resilience policies stay at `compatibility`
(`resilience.submissionKeyPolicy` and `resilience.taskGuardPolicy` of the
Corndogs chart). In this mode Corndogs accepts the requests of 0.12 clients.
Do not set `required` until no 0.12 client is in use and the metric
`corndogs_legacy_requests_total` stops increasing.

**Known limit: Reactorcide 0.13 does not support
`CORNDOGS_SUBMISSION_KEY_POLICY=required`.** The coordinator submits jobs
without a submission key (`internal/corndogs/client.go`). Only the tasks of
the internal queue (outbound events) use keyed submits. A Corndogs server
with the policy `required` refuses each job submit. This also applies to the
compose files and to an external Corndogs server. Keep
`resilience.submissionKeyPolicy: compatibility` (the chart default).

The file backend (bbolt) needs a stop before the replacement. Do not run a
0.7.x server and a 0.8.0 server on the same file store.

Back up the file store before the upgrade. Stop Corndogs first, because a copy
of an open bbolt file can be damaged.

Compose (VM) installation:

```bash
cd ~/reactorcide
docker compose -f docker-compose.prod.yml stop corndogs
mkdir -p backups
docker run --rm \
  -v reactorcide_corndogs_data:/data:ro \
  -v "$PWD/backups:/backup" \
  busybox:1.37 tar czf "/backup/corndogs-$(date +%Y%m%d%H%M).tgz" -C /data .
```

Use `nerdctl` instead of `docker` on a containerd host. The volume name is
`<compose project>_corndogs_data`. Make sure of the name with
`docker volume ls`.

Kubernetes installation (Corndogs release `corndogs`, claim `corndogs-data`):

```bash
kubectl -n reactorcide scale deployment corndogs --replicas=0
kubectl -n reactorcide run corndogs-backup --restart=Never --image=busybox:1.37 \
  --overrides='{"spec":{"containers":[{"name":"b","image":"busybox:1.37","command":["sleep","3600"],"volumeMounts":[{"name":"d","mountPath":"/data","readOnly":true}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"corndogs-data"}}]}}'
kubectl -n reactorcide exec corndogs-backup -- tar czf /tmp/corndogs.tgz -C /data .
kubectl -n reactorcide cp corndogs-backup:/tmp/corndogs.tgz "./corndogs-$(date +%Y%m%d%H%M).tgz"
kubectl -n reactorcide delete pod corndogs-backup
```

Then deploy Corndogs 0.8.1. `jobs/deploy-to-k8s.yaml` deletes the old
Corndogs deployment before it installs the new chart, and
`jobs/deploy-to-vm.yaml` stops all services before it starts them again.

**A downgrade to Corndogs 0.7.x is not supported after guarded tasks
exist.** The 0.13 coordinator makes guarded tasks (for example, outbound
event deliveries). A 0.7.x file server does not read the guarded-task bucket,
so it does not see these tasks. A 0.7.x binary does not detect the newer
data, so it does not refuse to start. To go back to 0.7.x, restore the backup
from before the upgrade. Tasks that were made after the backup are lost.

### 2.2 Coordinator

The coordinator applies the migrations when `reactorcide serve` starts. You
do not need a separate migration step. The first 19 migrations (`000047` to
`000065`) took about 2 seconds on a copy of the production schema with two
times the production data. The migrations after them add nullable columns,
one table, and partial indexes. They
were also tested on PostgreSQL 18 (the compose installation) with Up, Down,
and Up again.

Set the new coordinator settings before the start, if you need them (see
[section 4](#4-new-optional-features) and
[section 5](#5-helm-chart-values)).

Make sure that `REACTORCIDE_UI_AUTH_MODE` (Helm: `uiAuth.mode`) has the
login mode of the instance. Migration `000073` records that an instance
with login data ran with logins. After it, the coordinator does not start
in mode `none` (the default when the setting is missing). See
[Login history guard](./ui-auth.md#login-history-guard). An instance in mode
`rp` or `local-rp` starts as before and records its mode at each start.

### 2.3 Workers and runner image

Deploy the workers, then the runner image. Set the project evaluator image
and the job images to the 0.13 `runnerbase` tag (or keep
`runnerbase:latest`).

If you use the egress proxy, deploy the egress image with the same tag (see
[section 4.3](#43-egress-proxy)).

### 2.4 After the upgrade

- Watch the first pull request run of the `test-egress` job (the Reactorcide
  repository CI). It sets a memory limit of 6Gi and no memory request, so
  Kubernetes sets the request to 6Gi. It also requests 2 CPU. On a small
  node it can stay `Pending`, or the kubelet can evict it. If this occurs,
  add a node with more memory or move the job to a larger worker class. Do
  the same check for the first `image-egress` release build.
- Look at the coordinator log for SQL errors (`SQLSTATE`) while old and new
  pods run at the same time.

## 3. Behavior changes at the default settings

These changes apply after the upgrade with no configuration change.

| Change | New default | How to keep the old behavior |
| --- | --- | --- |
| Priority lanes | Release and tag work runs first (base 100). Pull request work gets -100 (ready) or -200 (draft), pushes to other branches -100, schedules -300. The eval of a draft pull request gets 5, not 10. See the table below. | `reactorcide lanes set --org ORG LANE --base 0` for each lane that you want at 0. See [Priority and Lanes](./priority.md). |
| Automatic retry of infrastructure failures | `infra_retries` is 1 for each organization. A job that fails with `infra.platform` before its main container starts runs one more time. A lost job gets at least 2 retries, as before. | Set `infra_retries` to 0 on the organization or the project. Do this for deploy jobs that are not safe to run again. See [Runtime Behavior](./runtime-behavior.md). |
| Webhook rate limit | 600 deliveries a minute for each project, burst 200, for each coordinator replica. A delivery over the limit gets HTTP 429 and the event is stored as `rate_limited`. | `REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE=0` (Helm: `rateLimits.webhookPerMinute: 0`). |
| Observability loops | The coordinator writes hourly and daily rollups, evaluates alert rules every 300 seconds (only the rules that a subscription receives), and writes availability heartbeats. One replica does this work (lease `observability-rollups`). Each replica exports the SLO gauges `reactorcide_slo_*` (computed at most once a minute). | `REACTORCIDE_OBSERVABILITY_ENABLED=false` (Helm: `observability.enabled: false`). |
| Artifacts and test results | On. A job can upload artifacts and JUnit results to the object store. Limits: 256 MiB for each file, 1 GiB and 1000 files for each job. | `REACTORCIDE_ARTIFACTS_ENABLED=false` (Helm: `artifacts.enabled: false`). |
| Timeout enforcement | Docker, containerd, and VM workers stop a job at its `timeout_seconds` and report the status `timeout`. Before, only Kubernetes did this. | Increase `timeout_seconds` of the jobs that need more time. |
| Draft pull request check | When a draft pull request is skipped (`draft_prs: skip` or a shed level), "Reactorcide Checks" is `pending` with the text `Skipped: draft pull request`. It is not `success`. Existing organizations keep `draft_prs: run`, so a draft runs as before. | No action. `ready_for_review` replaces the pending status. |
| Shared builder cache | The shared cache namespace (`standard`) moves to a new location (`shared/` on a builder cache claim). The first builds after the upgrade start with a cold cache, one time. | No action. You can delete the old cache data after the upgrade. See [Builds and Caches](./builds-and-caches.md). |
| Trigger endpoint | `POST /api/v1/jobs/{id}/triggers` accepts only the job token of that running job. A finished job and other credentials cannot add jobs to it. | No action for `runnerlib trigger`. Change scripts that called the endpoint with an admin or user token. |
| Manual events | A manual submit has the event type `manual`. A ref class rule must name the event type `manual` to give it a protected class. | Add rules for `manual` events. See [Ref Classes](./ref-classes.md). |
| Outbound events | The dispatcher runs, but it sends nothing until a subscription exists. It uses Corndogs. | `REACTORCIDE_OUTBOUND_EVENTS_ENABLED=false` (Helm: `outboundEvents.enabled: false`). |
| Enrollment token deactivation | Permanent. `deactivate-enrollment-token` requires `confirm_permanent: true`, and `reactorcide workers tokens deactivate` requires `--confirm-permanent`. A database trigger refuses to activate a deactivated token. The worker list shows the token ID and name of each worker after its next registration. | No old behavior. Add `--confirm-permanent` to scripts that deactivate tokens. See [Workers](./workers.md#enrollment-token-rotation). |
| Job command shell | Each job command goes through the shell prefix, also a single-line command. The prefix is `REACTORCIDE_JOB_SHELLCMD` (or `command_prefix` of a job spec file), else `sh -c`, else `powershell.exe -NoProfile -NonInteractive -Command` for a job with `os: windows`. Thus `echo a; sleep 5` runs two commands, and `*`, `?`, and `~` expand in a single-line command. Before, a single-line command with no `$NAME` reference was split into words and run directly, and `;`, `|`, and `&&` were text. A command that starts with a shell (`sh -c`, `bash -c`, `powershell.exe`, `pwsh`, `cmd /c`, and similar) is not wrapped again. `submit` now sends `command_prefix` to the coordinator as `REACTORCIDE_JOB_SHELLCMD`. See [Command Execution](./job-definitions.md#command-execution). | An image without `/bin/sh` (for example distroless) must set `REACTORCIDE_JOB_SHELLCMD: none` (or `command_prefix: none`): the worker then runs the words directly, as before. A repository job command is also wrapped as `runnerlib run --job-command '...'`, so an image with no shell and no runnerlib also needs `raw_command: true`. A single-line Windows command now runs in PowerShell, not as a direct program: examine the quoting of arguments. |
| Service principal tokens on a no-login instance | With `REACTORCIDE_UI_AUTH_MODE=none` (the default), a service principal token issued with no capability list (`principals tokens create` without `--capability`, REST, or UI `issue-principal-token`) gets all capabilities and all organizations. The roles of the principal do not narrow it. Only a global caller can issue it. Not for production. See [No-login instances](./ui-auth.md#no-login-instances-single-admin). A login mode (`local-rp`, `rp`) does not change. | Give `--capability` values: the token is then org-scoped and role-narrowed, as before. Before you enable logins, revoke the tokens that you issued in no-login mode. |
| `run_as` merge | An overlay file (`run-local -i`, `submit -o`, and a trigger overlay) and a workflow entry over a `job_file` change only the `run_as` fields that they set. A `run_as` block without `user` keeps the `user` of the base job; an empty or null field also keeps the base value. Before, an overlay file reset `user` to the default (`runner`), and a workflow entry replaced the full `run_as` block of the job file. runnerlib eval and the coordinator (a trigger file with a `job_file`) use the same rule. | To get the default user, set `run_as.user: runner` in the overlay or the workflow entry. |
| Plugins of untrusted source | `runnerlib run` loads `.reactorcide/plugins` only from the trusted CI tree when the run has untrusted application source (a run with a separate CI tree, or a fork pull request). When the trusted CI tree has no plugins, no repository plugins load. Before, runnerlib then loaded the plugins of the application source, also of a fork. A `--plugin-dir` or `REACTORCIDE_PLUGIN_DIR` directory in the application source is refused for such a run. See [Security Model](./security-model.md#trusted-ci-source). | No old behavior. Put the plugins that pull request jobs need in the trusted CI tree (the base branch). |
| Eval command wrap | Eval quotes the command in `runnerlib run --job-command` as one shell argument: each `'` becomes `'"'"'` (or `''` for PowerShell). A command with single quotes now reaches runnerlib unchanged. Before, the quotes of the command ended the wrap quotes: `echo 'a b'` ran as `echo a b`, and an odd number of quotes broke the job. | Examine commands that had single quotes and worked by chance. |
| Trigger `job_file` that cannot load | A workflow node from a trigger file (not eval) whose `job_file` cannot be read or parsed fails as `not_admitted`, with the load error. Before, the coordinator dropped the node and wrote only a log line. | No old behavior. Fix the job file. |
| Default runner image | Migration `000074` changes the database default of `jobs.runner_image` and `projects.default_runner_image` to `containers.catalystsquad.com/public/reactorcide/runnerbase:dev`. Before, the default was `quay.io/catalystcommunity/reactorcide_runner`, which the project does not build. Existing rows do not change. | Set the runner image of each project, or `REACTORCIDE_DEFAULT_RUNNER_IMAGE`, to the image that you want. |
| runnerlib workflow context | `ctx.branch`, `ctx.commit`, and `ctx.ref` read the job environment (`REACTORCIDE_BRANCH`, `REACTORCIDE_SHA`, and for a pull request the head branch `REACTORCIDE_HEAD_REF`). Before, they were `None` unless `REACTORCIDE_GIT_*` was set. For a pull request, `ctx.branch` is the head branch, not the target branch. `is_job_running` and `get_job_result` are removed (they were stubs). The default `--runner-image` of runnerlib `--container` mode is `containers.catalystsquad.com/public/reactorcide/runnerbase:dev`, not `quay.io/catalystcommunity/reactorcide_runner`. See [Writing Pipelines](./writing-pipelines.md#context-properties). | `REACTORCIDE_GIT_BRANCH`, `REACTORCIDE_GIT_COMMIT`, and `REACTORCIDE_GIT_REF` still win when set. Remove calls to `is_job_running` and `get_job_result`. Use `depends_on` and `condition`. |
| Corndogs API key setting | Removed: Helm `corndogs.apiKey` (and the `CORNDOGS_API_KEY` Secret key and environment variable of the coordinator), and `REACTORCIDE_CORNDOGS_API_KEY`. Corndogs has no API key authentication, and Reactorcide never read these settings. | No action. Remove the value from your Helm values and environment. |
| Reserved job environment names | Eval refuses a job whose `environment` sets `REACTORCIDE_REF_CLASSES`, `REACTORCIDE_CACHE_NAMESPACE`, `REACTORCIDE_CACHE_WRITABLE`, or `REACTORCIDE_ARTIFACT_NAMESPACE`. The coordinator removes these names from trigger output. | No old behavior. Rename such job variables. |
| VM job images | The coordinator resolves the image of a job when it creates the job (see [Image Aliases](./job-definitions.md#image-aliases)). It refuses a macOS or Windows VM job whose image is not an image alias, and not a pinned reference that an alias of the same platform pins. A plain tag reference (no `@sha256:` digest) for a VM job is refused. Each VM job gets the characteristic `image_digest`, so a VM worker older than 0.13 cannot claim VM jobs. Only a worker with that digest in its custom characteristic `image_digest` claims a job. A pinned VM reference is stored as the reference of the alias that pins its digest. A job (VM or container) whose os, arch, characteristics, or worker class conflict with its alias is refused at creation. The error names the alias, the key, both values, and the fix. A refused workflow job fails its node (`not_admitted`); a refused eval job fails its CI event. A retry or a rerun gets the current pin of the alias (a direct pinned reference resolves through the alias that pinned it at creation); it is refused only when that alias does not exist any more or does not fit the job. Container jobs with a reference do not change. A 0.13 worker gets an image set from the coordinator, pulls each image that it can be asked to run before it claims a job, and sends the ready digests in `image_digest` itself. It pulls each image independently. A worker that has a transient failure of an image that it needs stays not ready (`image update failed`); a permanent failure (not found, denied, digest mismatch) does not stop the worker, and the rollout status shows it to the alias owner. | No old behavior. Deploy the coordinator first, then the workers. Before the upgrade, add an image alias for each VM image, and change VM jobs to the alias name. Do not set `REACTORCIDE_WORKER_CUSTOM_IMAGE_DIGEST`: the worker sends its digests. See [Image Aliases and Worker Image Sets](./image-aliases.md). |
| Management operations with an API token | Each management operation of the UI service accepts an API token, not only a browser session: workers, pools, queues, enrollment tokens, groups, roles, projects, secrets, CI policy, and global admin operations. The token gets the role of its owner, limited to its organizations and capabilities. `list-workers`, `list-pools`, and `list-queues` need `workers:read` or `workers:manage`; changes need `workers:manage`. A user-delegated token now gets no capability in an organization that the token does not cover. See [UI Authentication: Sessions and API tokens](./ui-auth.md#sessions-and-api-tokens). | Before, these operations refused a token (`unauthorized`). Check the capabilities and the organizations of tokens that automation uses. |
| Token capability checks | A token authenticates as its principal. Its authority is the permissions of that principal, narrowed to the capability list and the organization list that the issuer chose. A token issued with all capabilities (`all_capabilities`) and all organizations has the full permissions of its principal and loses nothing. Before, some operations ignored the capability list of a narrow token and gave the full role of its principal: image aliases, schedules, ref class rules and priority lanes, subscriptions, principals, group VCS links, OIDC settings, alert rules, CI trust settings, required set settings, and the private job and workflow lists (REST `GET /api/v1/jobs` and `GET /api/v1/workflows`, and the UI service). Now each operation uses the list, as the issuer asked. Reads accept the read capability (for example `organizations:read`); changes need the manage capability. See [UI Authentication](./ui-auth.md#sessions-and-api-tokens). | No action for a token with all capabilities. A token with a narrow capability list gets only what the list gives: add a capability to the list if an operation needs it. |
| UI event stream with an API token | `GET /api/v1/ui/stream` accepts an API token (bearer), filtered by the token. A bearer value that is not a valid session or API token gets HTTP 401; before, it was an anonymous caller. The webapp proxy (`/app/ws`) then connects again without the credential, so a browser with an expired session keeps the anonymous stream as before. The proxy now refuses a browser upgrade from another origin (HTTP 403). | No action. A client that opens `/app/ws` with the session cookie must send the `Origin` of the webapp (browsers do). |
| Image alias metadata | The kind, os, arch, and characteristics of an image alias cannot change after the create. `alias set` changes only the pin. | No old behavior (new feature). Create an alias with a new name for another platform or constraint. |
| Kubernetes image pre-seed | The pre-seed DaemonSet is named `<prefix>-<short hash>` for each image set. `REACTORCIDE_K8S_IMAGE_PRESEED_NAME` is now the prefix (52 characters or less). An empty set does not delete a DaemonSet; old DaemonSets are removed after 30 minutes without use. | No old behavior (new feature). |
| VM guest transport | A VM guest has no network adapter. The worker talks to the guest daemon (`vmguest`) over vsock (macOS) or Hyper-V sockets (Windows). Before, the worker found the guest IP in the DHCP leases and used SSH. A guest image without the guest daemon fails before boot ("no Reactorcide guest daemon"). | No old behavior. Rebuild each VM image with the guest daemon: `build-windows-image.ps1 -GuestDaemon` or `vm-image build windows` on Windows; `install-vmguest.sh` or `vm-image build macos --guest-daemon` on macOS. See [VM Images Quick Start](./vm-images-quickstart.md). |
| Windows base images | The image build does not generalize the image (no Sysprep), and the worker writes no per-clone `Unattend.xml`. All clones have the computer name `RCIDE-BASE` and the same SID. The build prevents BitLocker offline and decrypts the volume before capture. | No old behavior. Rebuild the base images with the 0.13 `build-windows-image.ps1`. Do not join job VMs to a domain. See [Windows VM Image Build](./windows-vm-image-build.md). |
| VM worker settings | The worker ignores `REACTORCIDE_VM_SSH_USER`, `REACTORCIDE_VM_SSH_PASSWORD`, `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`, `REACTORCIDE_VM_SSH_HOST_KEY_FILE`, and `REACTORCIDE_VM_HYPERV_SWITCH`. The new setting is `REACTORCIDE_VM_GUEST_USER` (default `reactorcide`). In `worker-service.example.json`, `REACTORCIDE_VM_SSH_USER` is now `REACTORCIDE_VM_GUEST_USER`. `vm-image build macos` has no `--ssh-user`, `--ssh-private-key-file`, or `--ssh-password-file` flag; it has `--guest-user` and `--guest-daemon`. | Set `REACTORCIDE_VM_GUEST_USER`. On Windows, run `install-reactorcide.ps1` again: it renames the key and removes the old keys. Change image pipelines that used the SSH flags. |
| VM image source of a worker | A coordinator VM job has a pinned reference of an alias. The `local` image source cannot resolve it. The worker needs `REACTORCIDE_VM_IMAGE_SOURCE=oci` or `s3`. `local` is for `run-local` only. The Windows installer (`install-reactorcide.ps1`) and `worker-service.example.json` now use `oci` by default; before, they used `local`. The worker setting default is still `local`. | Set the image source to `oci` or `s3` on each VM worker. On Windows, run the installer again (the default is `-ImageSource oci`). Use `-ImageSource local` only for local tests. |
| VM scratch directories | Each job VM runs from a scratch directory `reactorcide-vm-*` under `REACTORCIDE_VM_SCRATCH_DIR` (default: the temp directory). At start and before each boot, the worker deletes each directory that no live worker locks, and on Windows its job VM. A directory with no lock file is deleted after 10 minutes. | No old behavior. Do not keep other files with this prefix in the scratch directory. |
| Windows VM host | Job VMs need no virtual switch. Only the ISO builder uses one. At the first boot, the worker writes two Hyper-V socket service keys under `HKLM` (`GuestCommunicationServices`). | Run the worker elevated or as LocalSystem, or add the keys one time as an administrator. Smart App Control or another application control policy blocks unsigned binaries: turn it off or sign the binaries. See [Windows VM Runner](./vm-runners-windows.md). |
| Windows guest egress | Guest egress works only for programs that use the proxy settings (WinHTTP, WinINET, or the proxy environment variables). The guest daemon adds a loopback adapter (`10.255.255.1/30`). A tool that ignores the proxy cannot connect. | Examine jobs that used direct network access. Set the proxy for such tools. |
| macOS VM host | The worker must run as a user LaunchAgent with the Local Network approval. | See [macOS VM Runner](./vm-runners-macos.md). |
| Pull request commands through the API | `POST /api/v1/approvals` (and `reactorcide approvals`) accepts a user token or a service principal token from `principals tokens create` or a federation exchange, with `workflows:control`. Before, only a user token worked. A job token and other tokens from `token create` get HTTP 403. From 0.13.4, the admin token and wide service principal tokens act as the instance administrator in every auth mode: they pass each approver check, and their approvals record `project_owner`. See [No-login instances](./ui-auth.md#no-login-instances-single-admin). | To automate approvals with logins, issue a principal token with `--capability workflows:control`. |
| No-login mode after logins | The coordinator does not start with `REACTORCIDE_UI_AUTH_MODE=none` (also the default when the setting is missing) on an instance that ran with logins. Migration `000073` marks an existing instance that has login data (LinkKeys login identities, a stored RP API key or local-RP identity, trusted identities, or trusted domain patterns). Each start in `rp` or `local-rp` marks it. The no-login behaviours (wide principal tokens, anonymous cancel and retry) apply only when the guard accepts mode `none`. See [Login history guard](./ui-auth.md#login-history-guard). | Set the login mode of the instance. To change to mode `none` on purpose, start one time with `REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE=true`, then remove it. |
| UI pull request commands with an API token | The UI operation `pr-command` with an API token needs `workflows:control` for the organization of the project, as REST does. It now also accepts a service principal token (the principal is the person), as REST does. | Add `workflows:control` to user tokens that give pull request commands through the UI service. |
| Refused retry message | A retry of a job that is not `failed`, `cancelled`, or `timeout` (REST `POST /api/v1/jobs/{id}/retry`, `reactorcide jobs retry`) gets HTTP 400 with the error code `not_retryable` and a message with the current status, for example `only a failed, cancelled, or timed-out job can be retried; job ID is completed`. Before, it was `invalid_input` (`Invalid input data`). A workflow retry, `retry-unsuccessful`, and a re-run that is refused give the same code with their own message. The UI service keeps the code `conflict`, with the new message. | Change scripts that compare the error code `invalid_input` of a refused retry. |
| Shifts and new comment commands | `/reactorcide retry` and `/reactorcide rerun` accept `key=value` shifts. `/reactorcide promote` and `/reactorcide demote` are new. A word that is not a valid shift makes the comment not a command. Each shift has a bound by default: CPU 16, memory 64Gi, storage 200Gi, timeout 86400 seconds. | Set `rerun_shift_limits` to make the bounds smaller. |
| Replies to comment commands | Reactorcide replies on the pull request to a refused or not valid `/reactorcide` comment. The replies have a rate limit for each pull request (burst 3, then 1 a minute) and for each project (burst 20, then 10 a minute). | No setting turns the replies off. |
| New token capabilities | `workers:read` and `diagnosis:write` are new. A token with all capabilities gets them. | Add them to narrow tokens that need them. |
| Container image sets | A Docker, containerd, or Kubernetes worker that serves container image aliases pulls their images before its first job request (the startup gate). A transient pull failure keeps the gate closed. Docker and containerd workers remove the images that they pulled when the images leave the set. A Kubernetes worker uses the pre-seed DaemonSet. | Make sure that the workers can reach the registry of each alias. To keep the old behavior, use no container image aliases for the pools of those workers. See [Workers](./workers.md#image-sets-and-pre-seed). |
| Image prune interval | `--vm-image-prune-interval` (`REACTORCIDE_VM_IMAGE_PRUNE_INTERVAL`) also controls the removal of images that left the image set, on each backend. The value 0 turns off VM retention checks, but the set removal then uses 1 hour. | No action for the default (24h). |
| Execution profile update | A profile update writes each boolean field: `egress_control`, `deny_secrets`, `may_run_as_root`, and `trusted_cache_writes`. Before, a request without the field kept a stored `true`. Now a missing or `false` value clears it. | Send the full profile in each update. |
| Group sync from VCS teams | Every 15 minutes, only for groups that you link to a VCS team. | `REACTORCIDE_GROUP_VCS_SYNC_INTERVAL=0` (Helm: `groupVcsSync.interval: "0"`). |

Existing organizations keep the `legacy` defaults profile. New
organizations (also the default organization of a new installation) get the
`secure` profile.

### Priority lanes

| Work | Before | After |
| --- | --- | --- |
| Release, production, or tag work | 0 | 100 |
| Eval of a pull request (not a draft) | 10 | 10 |
| Eval of a draft pull request | 10 | 5 |
| Other eval jobs | 5 | 5 |
| Push to the default branch, manual work | 0 | 0 |
| Pull request work, ready | 0 | -100 |
| Pull request work, draft | 0 | -200 |
| Push to another branch | 0 | -100 |
| Schedules | 0 | -300 |
| Backfill (only with a ref class rule) | 0 | -400 |

The priority of a job specification (`job.priority`) is now a request inside
the lane (from -50 to 50). Jobs in the queue at the upgrade keep their stored
priority.

## 4. New optional features

Each feature is off until you turn it on.

### 4.1 OIDC issuer for jobs

Set the public HTTPS issuer URL on the coordinator. The coordinator needs a
master key.

```yaml
# Helm values
oidc:
  issuerUrl: "https://ci.example.com/api/v1/oidc"
```

Then allow the issuer for an organization or a project with
`reactorcide oidc settings set`. See [OIDC Tokens for Jobs](./oidc.md).

### 4.2 GitHub App

Create the App on GitHub, then register it:

```bash
reactorcide github-apps add ...
reactorcide github-apps sync APP_ID
```

See [GitHub App](./github-app.md). Remove the repository webhook when you
move a repository to its App.

### 4.3 Egress proxy

Make the key Secret, then enable the proxy:

```yaml
egressProxy:
  enabled: true
  secretName: reactorcide-egress-keys
```

`jobs/scripts/deploy_k8s.py` sets `egressProxy.image.tag` to the same release
tag as the coordinator, worker, and web images
(`REACTORCIDE_IMAGE_TAG`). Set `REACTORCIDE_EGRESS_IMAGE_REPOSITORY` to use a
different repository. If your deploy wrapper writes image repositories and
tags into `REACTORCIDE_HELM_VALUES`, also write `egressProxy.image`. See
[Egress Proxy](./egress-proxy.md).

### 4.4 Required set

```bash
reactorcide required-set enable --project PROJECT
```

The project needs a stored CI policy, or its organization must use the
`secure` defaults profile. See [Required Set](./required-set.md).

### 4.5 Secure defaults profile

```bash
reactorcide orgs set-defaults ORG --profile secure
```

This sets the run gate `outside_contributors` and `dynamic_jobs: deny`. To go
back, use `--profile legacy`. See
[Organizations and CI Policy](./organizations-and-ci-policy.md).

### 4.6 Event subscriptions (outbound webhooks)

```bash
reactorcide subscriptions create --org ORG --name NAME ...
```

A webhook can reach only public addresses. To allow private networks, set
`REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS` (Helm:
`outboundEvents.allowedNetworks`). See [Outbound Events](./outbound-events.md).

### 4.7 Prometheus alert rules

The chart can make a PrometheusRule with the CI owner alerts of
`deployment/prometheus/reactorcide-ci-owner-alerts.yaml`:

```yaml
metrics:
  prometheusRule:
    enabled: true
    labels:
      release: kube-prometheus-stack
```

This needs the PrometheusRule CRD (Prometheus Operator).

## 5. Helm chart values

The new values set coordinator variables. An empty value does not set the
variable, so the coordinator uses its code default. The default render of the
coordinator ConfigMap is the same as 0.12. The default render of the chart
has these changes:

- The Corndogs subchart is 0.6.0 when `corndogs.enabled` is true.
- The worker Deployment always sets `REACTORCIDE_K8S_IMAGE_PRESEED_NAME`.
- The worker Role can create, read, change, and delete DaemonSets (`apps`
  group). The worker uses them for the image pre-seed.

| Value | Variable | Code default |
| --- | --- | --- |
| `oidc.issuerUrl` | `REACTORCIDE_OIDC_ISSUER_URL` | empty (off) |
| `rateLimits.webhookPerMinute`, `rateLimits.webhookBurst` | `REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE`, `_BURST` | 600, 200 |
| `rateLimits.apiPerMinute`, `rateLimits.apiBurst` | `REACTORCIDE_API_RATE_LIMIT_PER_MINUTE`, `_BURST` | 0 (off), 300 |
| `rateLimits.trustedProxyCIDRs` | `REACTORCIDE_TRUSTED_PROXY_CIDRS` | empty (the federation source limit uses the TCP peer) |
| `outboundEvents.enabled` | `REACTORCIDE_OUTBOUND_EVENTS_ENABLED` | true |
| `outboundEvents.allowedNetworks` | `REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS` | empty |
| `observability.enabled` | `REACTORCIDE_OBSERVABILITY_ENABLED` | true |
| `observability.intervalSeconds`, `alertIntervalSeconds` | `REACTORCIDE_OBSERVABILITY_INTERVAL_SECONDS`, `_ALERT_INTERVAL_SECONDS` | 60, 300 |
| `observability.rollupHourlyRetentionDays`, `rollupDailyRetentionDays` | `REACTORCIDE_ROLLUP_HOURLY_RETENTION_DAYS`, `REACTORCIDE_ROLLUP_DAILY_RETENTION_DAYS` | 35, 400 |
| `artifacts.enabled` | `REACTORCIDE_ARTIFACTS_ENABLED` | true |
| `artifacts.maxFileBytes`, `maxJobBytes`, `maxFiles` | `REACTORCIDE_ARTIFACT_MAX_FILE_BYTES`, `_MAX_JOB_BYTES`, `_MAX_FILES` | 256 MiB, 1 GiB, 1000 |
| `artifacts.flakyWindowDays` | `REACTORCIDE_FLAKY_WINDOW_DAYS` | 30 |
| `provenance.enabled`, `provenance.builderId` | `REACTORCIDE_PROVENANCE_ENABLED`, `REACTORCIDE_PROVENANCE_BUILDER_ID` | on, the OIDC issuer URL |
| `cache.sharedNamespace` | `REACTORCIDE_CACHE_SHARED_NAMESPACE` | null: `standard`. `""` turns the shared volume off. |
| `groupVcsSync.interval` | `REACTORCIDE_GROUP_VCS_SYNC_INTERVAL` | 15m |
| `metrics.prometheusRule.enabled` | (PrometheusRule object) | false |

These new worker values are empty or off by default:

| Value | Variable | Default |
| --- | --- | --- |
| `worker.admission.mode` | `REACTORCIDE_WORKER_ADMISSION` (set only for `resources`) | `concurrency` |
| `worker.admission.maxLeases` | `REACTORCIDE_WORKER_CONCURRENCY` in `resources` mode | 64 |
| `worker.admission.cpu`, `worker.admission.memory` | `REACTORCIDE_WORKER_ADMISSION_CPU`, `_MEMORY` | empty (the measured capacity) |
| `worker.admission.capacityNodeSelector` | `REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR` | empty |
| `worker.region`, `worker.zone`, `worker.cloud` | `REACTORCIDE_WORKER_REGION`, `_ZONE`, `_CLOUD` | empty |
| `worker.detectTopology` | `REACTORCIDE_WORKER_DETECT_TOPOLOGY` (set only for `false`) | true |
| `worker.kubeconfig.secretName`, `key`, `context` | `REACTORCIDE_K8S_KUBECONFIG`, `REACTORCIDE_K8S_CONTEXT` | empty (in-cluster) |
| `worker.jobApiUrl` | `REACTORCIDE_JOB_API_URL` | the in-cluster coordinator Service |
| `worker.jobNodeSelector`, `jobTolerations`, `jobAffinity` | `REACTORCIDE_K8S_JOB_NODE_SELECTOR`, `_TOLERATIONS`, `_AFFINITY` | empty |
| `worker.imagePreseed.name`, `toolsImage`, `pauseImage`, `timeout` | `REACTORCIDE_K8S_IMAGE_PRESEED_NAME`, `_TOOLS_IMAGE`, `_PAUSE_IMAGE`, `_TIMEOUT` | name: `<worker name>-image-preseed` (52 characters or less) |
| `builder.rootless` | `REACTORCIDE_BUILDER_ROOTLESS` | false |
| `egressProxy.*` | the `REACTORCIDE_EGRESS_*` worker variables and the egress Deployment | `enabled: false` |

See [Kubernetes Deployment](../helm_chart/DEPLOYMENT.md) and
[Workers](./workers.md).

For other settings, use `app.extraEnv` (coordinator) and `worker.extraEnv`
(worker). Each item is a Kubernetes `EnvVar`:

```yaml
app:
  extraEnv:
    - name: REACTORCIDE_ADMISSION_RESERVE_AFTER
      value: "10m"
worker:
  extraEnv:
    - name: REACTORCIDE_WORKER_REGION
      value: us-west-2
    - name: SOME_SECRET_SETTING
      valueFrom:
        secretKeyRef: {name: worker-settings, key: value}
```

An item in `app.extraEnv` replaces the variable of the same name from the
coordinator ConfigMap.

The compose files need no new settings. Add a variable to the `environment`
of a service (or to `.env`) only when you turn on a feature.

## 6. Rollback

### 6.1 Old binary on the new schema

A v0.12.13 coordinator works on a database at version 74 (the 0.13
schema). It does not use the new tables and columns. The full verification
below was done at version 66. Migrations `000067` to `000074` add only
nullable columns, two tables, indexes, and new column defaults; the paragraphs below tell why
each one is safe for the old coordinator. This is the fast rollback: deploy the old images
and keep the database. Keep Corndogs at 0.8.1 (see
[section 2.1](#21-corndogs-081)). The same is true during the rolling
deploy, when old and new coordinator pods use the database at the same time.

Migration `000067` adds one nullable column, `jobs.run_as_administrator`,
with no default value. A v0.12.13 coordinator does not read or write it. A
job that the old coordinator creates has NULL, which means that the job did
not set `run_as.administrator`. The verification below was done at version
66, before `000067` was added.

Migration `000068` adds the table `image_aliases` and nullable columns:
`jobs.image_alias`, the `carried_image_*` columns of `workflow_nodes`, and
the image state columns of `workers`. `workers.image_set_version` is
`NOT NULL DEFAULT 0`. A v0.12.13 coordinator does not read or write them.
It does not resolve image aliases: a job that it creates keeps the image
of the job definition, with no `image_digest` characteristic, as before.
A worker row that it creates gets the default values. The automated test
`TestMigration068ImageAliases` applies `000068` on a version 67 database,
inserts a worker row without the new columns, and runs Down and Up again.
The v0.12.13 verification below was done at version 66.

Migration `000069` adds one nullable column, `workers.image_runtime`, with a
CHECK that permits NULL, and the partial index
`jobs_image_alias_active_idx`. Migration `000070` adds only the partial
index `jobs_image_digest_direct_active_idx`. A v0.12.13 coordinator does not
read or write `image_runtime`. A worker row that it creates has NULL, which
means that the worker gets no image set. The indexes change no query
result. The automated tests `TestMigration069WorkerImageRuntime` and
`TestMigration070DirectImageDrainIndex` apply each migration on the
version before it and run Down and Up again.

Migration `000071` adds one nullable column, `workers.image_failed` (the
per-image pull failures of the newest report). A v0.12.13 coordinator does
not read or write it. The automated test
`TestMigration071WorkerImageFailures` applies it on a version 70 database
and runs Down and Up again.

Migration `000072` adds three nullable columns: `jobs.image_pin_alias`,
`jobs.image_retry_of`, and `workflow_nodes.carried_image_pin_alias`. A
v0.12.13 coordinator does not read or write them. A job that it creates has
NULL in each column. A retry of such a job looks for an alias that pins the
digest at the time of the retry. The automated test
`TestMigration072JobImagePinAlias` applies it on a version 71 database and
runs Down and Up again.

Migration `000073` adds the table `auth_login_history` (one row or no row)
and writes the row when the database has login data. A v0.12.13
coordinator does not read or write it, and it has no login history guard:
a rollback to v0.12.13 in mode `none` gets the no-login behaviours of that
version. Keep the login mode set during a rollback. The automated test
`TestLoginHistoryMigrationBackfill` applies it on a version 72 database
with and without login data, and runs Down and Up again.

What was verified (PostgreSQL 17 and 18, a fresh database and a v46 database
with production-like data, migrated to 66 by the 0.13 binary):

- `reactorcide migrate` of v0.12.13 is a no-op ("no migrations to run,
  current version: 66").
- The v0.12.13 integration test package (258 tests, the v0.12.13 store code)
  passes on a clean v66 schema. On the database with data, the same 8 tests
  fail on v46 and on v66 (test fixtures that collide with the existing
  rows), and no test fails only on v66.
- The v0.12.13 `serve` on the v66 schema: token create, project create, job
  create, job list, and a signed GitHub `pull_request` webhook (CI event and
  eval job) work, with no SQL errors.
- The `ON CONFLICT` clause that v0.12.13 uses to create the root workflow of
  a CI event works, also when a 0.13 re-run of the same workflow exists.
  Migration `000051` does not change the unique index
  `workflow_instances_event_security_idx`; a 0.13 re-run keeps its event in
  the new column `rerun_event_id`.

Limits of the old coordinator on the new schema:

- It does not know the new settings. For example, it does not apply lanes,
  infrastructure retries, or the webhook rate limit.
- It does not resolve image aliases and does not refuse VM jobs. A job that
  names an alias then has the alias name as its image, and no worker can
  pull it. A retry that it makes keeps the reference and the
  characteristics, but not `image_alias`.
- It does not read `rerun_event_id`. A re-run that a 0.13 coordinator made
  does not count toward the `Reactorcide Checks` status that a v0.12.13
  coordinator computes. Re-run the workflow again after the rollback if you
  need the status.
- A CI event that a v0.12.13 pod records (for example during the rolling
  deploy) has no ref classes. A worker class that needs a ref class
  (`ref_classes_any`, `protected`) does not accept its jobs. Re-run such an
  event after the deploy.
- It does not record the enrollment token of a worker that registers with
  it. The worker keeps the token ID of its last 0.13 registration. Its
  `deactivate-enrollment-token` does not ask for `confirm_permanent`, but the
  trigger of migration `000066` still refuses to activate a token again.

**Before you roll back, delete each secret grant with the delivery `proxy`.**
The old coordinator does not read the `delivery` column. It gives the secret
value of a proxy grant to the job environment. The old coordinator also does
not read `egress_control`, so jobs run without the egress proxy.

```bash
reactorcide secret-grants list --format table
```

### 6.2 Down migrations

Run the Down migrations only when you must remove the schema. The Down
migrations delete data. They delete:

| Migration | Data that Down deletes |
| --- | --- |
| 000047 | Job failure classes and details; `infra_retries` settings |
| 000048 | Principals, principal identities, actor columns, role assignments and group members of principals |
| 000049 | Ref class rules and the ref classes of jobs and events |
| 000050 | Federation issuers, mappings, and issued tokens; OIDC settings and signing keys |
| 000051 | Re-run shifts and limits, storage resources of jobs |
| 000052 | Required set settings, required workflows, proposals |
| 000053 | Workflow owners and contacts, workflow-scoped role assignments |
| 000054 | VCS team links, synced group members, membership history, owner allowlist |
| 000055 | Defaults profile, `draft_prs`, shed levels and actions |
| 000056 | Reporting settings, agreement records, path acceptances |
| 000057 | GitHub Apps, installations, installation repositories |
| 000058 | Event subscriptions, the outbox, deliveries, and delivery attempts |
| 000059 | Worker admission data |
| 000060 | Cache namespace classes, environment defaults, job links, image policies |
| 000061 | Artifacts records, test results and summaries, flakiness, quarantines, provenance keys |
| 000062 | Rollups, alert rules and transitions, heartbeats, SLO targets |
| 000063 | Proxy delivery settings of secret grants, egress control settings. A proxy grant becomes an env grant: delete proxy grants before the Down. |
| 000064 | Priority lanes, the lane, reason, and shift of jobs |
| 000065 | Diagnosis reports and failure class filters of subscriptions |
| 000066 | The enrollment token of each worker (the token rows stay); the trigger that refuses to activate a deactivated token |
| 000067 | The `run_as.administrator` value of each job (`jobs.run_as_administrator`) |
| 000068 | Image aliases, the alias name of each job (`jobs.image_alias`), the carried images of workflow nodes, and the image state of each worker. The resolved reference of each job stays in its image field. |
| 000069 | The runtime of each worker (`workers.image_runtime`); the index `jobs_image_alias_active_idx` |
| 000070 | Only the index `jobs_image_digest_direct_active_idx` (no data) |
| 000071 | The per-image pull failures of each worker (`workers.image_failed`) |
| 000072 | The alias that pinned a direct reference of each job (`jobs.image_pin_alias`), the earlier image of each retried job (`jobs.image_retry_of`), and the carried pin alias of workflow nodes (`workflow_nodes.carried_image_pin_alias`) |
| 000073 | The login history of the instance (`auth_login_history`). A later Up marks the instance again when it has login data. |
| 000074 | Only the column defaults of `jobs.runner_image` and `projects.default_runner_image` go back to `quay.io/catalystcommunity/reactorcide_runner` (no data) |

The Down migrations do not delete artifact files in the object store. Delete
them separately if you need the space.

After a Down to version 46, the schema is the same as the v0.12.13 schema.
This was tested with a production schema copy and on PostgreSQL 17 and 18.
The v0.12.13 integration test package also runs on the database after the
Down. A second Up to version 66 gives the same schema as the first Up. Each
of `000067` to `000074` has an automated test that applies it, runs Down,
and runs Up again.
