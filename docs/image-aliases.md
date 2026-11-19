# Image Aliases and Worker Image Sets

An image alias is a name for one pinned image reference. Jobs name the
alias. The coordinator gives each worker the images that it can be asked to
run, before the worker gets a job for them. A worker does not pull an image
during a job.

This page tells you how to manage aliases, how a new pin goes to the
workers, and how to read the image state of a worker. For the job side
(`job.image`, resolution, and the `image_digest` characteristic), see
[Job Definition Reference: Image Aliases](./job-definitions.md#image-aliases).

## Scopes and permissions

| Scope | Who can read and change it |
| --- | --- |
| Organization | An organization admin |
| Project | A project owner or an organization admin |

An API token must also be for the organization of the alias, and its owner
must have the permission in the table. To read aliases and the rollout
status, the token needs `organizations:read` or `policies:manage`. To
create, change, or delete an alias, the token needs `policies:manage`.
Reason: an alias is CI policy of the organization.

- A project alias replaces the organization alias that has the same name,
  for the jobs of that project only.
- Each create, change, promotion, and delete is an audit record. The record
  has the old reference, the new reference, and the actor. The actions are
  `image_alias.create`, `image_alias.set`, `image_alias.promote`, and
  `image_alias.delete`.

## Alias values

| Value | Note |
| --- | --- |
| Name | 1 to 63 characters: `a-z`, `0-9`, `.`, `_`, `-`. The first character is a letter or a digit. |
| Reference | A pinned reference: `oci://host/repository:tag@sha256:<digest>`, `host/repository:tag@sha256:<digest>`, or `s3://bucket/prefix/name:tag@sha256:<digest>`. The digest is the pin. The tag is free-form text. |
| Kind | `vm` or `container`. The default is `vm` when the os is `macos` or `windows`, and `container` for other values. |
| Os, arch | The platform of the image. A worker gets the image only when its os and arch are the same. |
| Characteristics | More worker characteristics that a worker must satisfy (`key=value`). The key `worker_class` is special: the pool of the worker must serve that worker class. You cannot use `os`, `arch`, or `image_digest`. |

The organization image policy checks the reference when you set the alias,
and again when the coordinator creates a job.

## CLI

The commands use the API token (`--token` or `REACTORCIDE_API_TOKEN`) and
the coordinator URL (`--api-url` or `REACTORCIDE_API_URL`). Give `--org`
for the organization scope. Add `--project PROJECT_ID` for the project
scope.

Create an alias:

```bash
reactorcide images alias set --org acme --os windows --arch amd64 \
  windows-vs2022 oci://registry.example.com/vm/windows-vs2022:2026.11@sha256:<digest>
```

Give the alias a new pin. New jobs use the old pin until the rollout is
complete:

```bash
reactorcide images alias set --org acme \
  windows-vs2022 oci://registry.example.com/vm/windows-vs2022:2026.12@sha256:<new-digest>
```

Other commands:

```bash
reactorcide images alias list --org acme
reactorcide images alias get --org acme windows-vs2022
reactorcide images alias delete --org acme windows-vs2022
reactorcide images rollout status --org acme [windows-vs2022]
```

`alias delete` is refused while a job that is not terminal (`submitted`,
`queued`, `scheduling`, `running`, or `cancelling`) uses the alias. The
error gives the number of such jobs. A job uses the alias when:

- It named the alias, and it got this alias at creation. For an
  organization alias, a job of a project that has a project alias with the
  same name uses the project alias, not the organization alias.
- It names a direct pinned reference (no alias name) in the scope of the
  alias, and its digest is the active or the pending digest of the alias.

Wait until the jobs end, or cancel them, and then delete the alias.

`alias set` flags:

| Flag | Use |
| --- | --- |
| `--kind`, `--os`, `--arch` | The kind and the platform of a new alias. `--os` and `--arch` are necessary for a new alias. |
| `--characteristic key=value` | A worker characteristic of a new alias (repeat the flag). |
| `--force` | Use the new pin at once. Do not wait for the workers. |

The kind, the os, the arch, and the characteristics of an alias cannot
change after you create it. `alias set` for an alias that exists changes
only the pin. A flag with another value is refused
(`failed_precondition`); a flag with the same value is not a change. For
another platform or another constraint, create an alias with a new name.
Reason: these values select the workers that get the image. A change
would send jobs to workers that do not have the image yet, without a
rollout.

The result has an outcome: `created`, `pending`, `promoted`, `updated`, or
`unchanged`. When you set the active reference again, the coordinator
cancels a pending pin.

The web UI uses the same operations through the CSIL bridge:
`list-image-aliases`, `get-image-alias`, `set-image-alias`,
`delete-image-alias`, `get-image-rollout-status`, and
`get-worker-image-state`.

## Which workers get an alias

A worker gets an alias in its image set when all of these are true:

1. The worker is image aware: it sends its runtime (`vm`, `docker`,
   `containerd`, or `kubernetes`) at Register, or it sends an image set
   report. An older worker gets no image set and works as before. It cannot
   claim a job that names an alias.
2. The pool of the worker serves the organization of the alias. An
   organization pool serves only its organization. A hosted pool serves
   each organization that grants it a worker class. See
   [Worker Operation](./workers.md#organization-pools-and-worker-classes).
3. The kind agrees with the runtime: `vm` aliases go to `vm` workers.
   `container` aliases go to `docker`, `containerd`, and `kubernetes`
   workers.
4. The os and the arch of the alias are the os and the arch of the worker.
5. The worker satisfies the alias characteristics, with the same rule as
   queues. A list value of a worker characteristic satisfies each value in
   the list.

Organization aliases and project aliases of the organization both go to the
worker. The image set has one entry for each digest. Each entry gives the
alias names that use the digest. The entry reference has no `oci://`
scheme, the same form that the job stores.

An entry is `pending` when its digest is only the pending pin of a rollout:
no served alias has it as its active pin, and no job that is not terminal
uses it. The worker pulls a pending entry, but its startup gate does not
wait for it. The worker is not ready until each entry that is not pending
is ready. See
[Worker Operation: Image sets and pre-seed](./workers.md#image-sets-and-pre-seed).

## Rollout of a new pin

1. `alias set` with a new digest sets the pending pin. The image set of each
   serving worker then has the active digest and the pending digest.
2. Each worker pulls the pending image in the background. It continues to
   run jobs for the images that it has.
3. One coordinator replica (the holder of the lease `image-alias-rollout`)
   checks the pending aliases every 15 seconds. It promotes the pending pin
   to the active pin when both of these are true:
   - At least one serving worker is online.
   - Each online serving worker has the pending digest in its ready
     digests. The state of the other images of the worker does not count:
     a failed image of another alias (on a hosted pool, maybe of another
     organization) does not stop this rollout.
4. After the promotion, new jobs get the new pin. The promotion is the audit
   record `image_alias.promote`.
5. The old digest stays in the image sets while a job that is not terminal
   uses it. When the last such job ends, the digest leaves the sets, and the
   workers can remove the image.

A job with a direct pinned VM reference (no alias name) keeps its digest in
the image sets of the workers that could run it until the job ends: VM
workers whose pool serves the worker class of the job in its organization,
and that satisfy the characteristics of the job. This is true also after
the alias that pinned the digest gets a new pin.

A pending pin that a worker cannot pull makes that worker report `failed`
with the failure of that digest. The rollout then does not promote. The
worker continues to run jobs for the images that it has.

`images rollout status` and the UI show these problems for the alias
owner:

- Serving workers that cannot pull the active pin with a permanent
  failure (not found, digest mismatch, unauthorized or denied, reference
  not valid). Jobs of the alias cannot run on these workers. Fix the image
  or the access to it, or set the alias to another reference.
- Online serving workers that cannot pull the pending pin with a permanent
  failure. The rollout cannot complete.

The rollout status gives a row for each serving worker in a pool that you
can manage (the permission to manage the workers of the pool, the same
rule as the worker list). For the other pools, for example a hosted pool
that also serves other organizations, it gives only the pool name and the
counts: workers, online workers, ready workers, and workers with a
failure. It does not show their worker IDs or host names.

A worker is online when its status is `active`, it has an active session,
and its last heartbeat is less than 90 seconds old. The worker list shows
the same value (`online`). A worker that is offline does not stop a
rollout.

A rollout does not complete while no serving worker is online, or while an
online worker does not have the image. `images rollout status` shows the
reason. Use `--force` to promote at once. Jobs then wait until a worker has
the new image.

## Image set versions

- The coordinator computes the image set of a worker at Register and at
  heartbeats. When the content of the set changes, the version of the set
  increases by one.
- The Register response of an image-aware worker has the full set.
- A heartbeat response has the set only when the worker sent no report, or
  when the report version is not the current version (older, or newer:
  the version of a worker row starts again at 1 after a new enrollment).
  The coordinator does not send version N again to a worker that reports
  N. A worker accepts a set with a lower version when its content is
  different, and takes the version of a set with the same content.
- A replica computes the set of a worker again at most one time each
  minute, and at once after an alias change on that replica. An alias
  change on another replica gets to the worker in one minute or less.

## Job creation refusals

The coordinator refuses a job that can never run with the image of its
alias, when it creates the job. The error names the alias, the key, the
value of the job, the value of the alias, and the fix. Examples:

```text
image alias "mac-xcode" needs the worker characteristic xcode=16, but the job needs xcode=15: no worker can run the job. Set the job characteristic xcode to 16, remove it, or use another image alias
image alias "mac-xcode" is for os "macos", but the job needs os "windows": no worker can run the job. Set the job characteristic os to "macos", or use an image alias for os "windows"
image alias "mac-xcode" is only for worker class "mac-xcode", but the job uses worker class "default": no worker can run the job. Set the job worker_class to "mac-xcode", or use another image alias
```

The refusal gets to you in these places:

| Creation path | Where you see the refusal |
| --- | --- |
| REST `POST /api/v1/jobs` | HTTP 400 with `{"error": "image_alias", "message": "..."}` (HTTP 403 with `image_policy` for the image policy). |
| `reactorcide submit` | `API error (400 image_alias): <message>`. |
| Workflow job (from an eval) | The workflow node is `failed`, disposition `not_admitted`, with the message in the node decision reason and the workflow error. "Reactorcide Checks" reports the workflow as not admitted. The other workflows of the event continue. |
| Eval job (webhook) | The CI event is `failed`, with the message as its reason. "Reactorcide Checks" shows the message. The coordinator does not try the event again. |

## Retry and rerun

A retry or a rerun of a job gets the current pin of its alias. Reason:
after an alias update the old digest leaves the worker image sets, so a job
that keeps the old digest cannot run.

- A job that named an alias: the alias name resolves again (project alias,
  then organization alias). The job gets the active pin.
- A job that named a pinned reference directly: the coordinator records the
  alias that pinned the digest at creation (`jobs.image_pin_alias`). The
  retry resolves through that alias. While the alias still pins the digest
  (active or pending), the retry keeps it. Else the retry gets the active
  pin and shows `alias (ref)`.
- When the digest changes, the new job records the image of the earlier
  run in `image_retry_of` (display form), and the coordinator logs the
  change. `image_display` shows the new `alias (ref)`.
- `image_retry_of` is job metadata. The REST job response, the UI service
  job (`JobSummary`), `reactorcide jobs get`, and the job page of the web UI
  show it. Job lists do not show it. The worker gets it in the lease and
  writes a second line in the job log header:

  ```text
  [reactorcide] image: win (registry.example/win:2026.12@sha256:...)
  [reactorcide] retry of: win (registry.example/win:2026.11@sha256:...)
  ```

  A job without a changed image gets no second line.
- The retry is refused when the alias does not exist any more, when the
  alias is now for another kind or platform, when the alias does not fit
  the job, or when the image policy refuses the new reference. The error
  names the alias. REST gives HTTP 409 `image_gone`. A workflow rerun
  records the refusal on the node.
- A workflow retry or rerun uses the same rule for each node.
- A triggered child job that names no image does not use this rule. It
  keeps the image of its parent (reference, alias, digest), because the
  drain keeps that digest while the parent is not terminal.

## Worker image states

The worker list (`reactorcide workers list`, column `IMAGES`) shows the
image status of each image-aware worker. The command accepts a browser
session or an API token. A token needs `workers:read` or `workers:manage`
for the organization of the pool (`--pool`). Without `--pool`, the owner of
the token must be a global admin and the token must be for all
organizations.

| Status | Meaning |
| --- | --- |
| `healthy` | The newest report is `updated` for the current set version. |
| `updating images` | There is no report yet, the report is for an older version, or the state is `updating`. |
| `image update failed` | The newest report is `failed`. The status shows the reason (for example, no disk space). The worker keeps its old images and tries again. After a start, a worker that does not have an image that is not pending claims no job until the pull is complete. |

`get-worker-image-state` gives the wanted version, the reported version,
the state, the detail, the ready digests, the failed images (digest,
permanent or transient, detail), and the set that the coordinator computes
now, with the images that are ready or failed.

## Limits

- A job with a direct pinned reference must name a digest that an alias in
  its scope with the same kind and platform pins when the coordinator
  creates it. The job stores the reference of that alias. After that, the
  job keeps its digest in the image sets until it ends.
- A triggered child job that keeps the image of its parent is refused when
  no worker can get the image: no alias of the same kind and platform pins
  the digest, and the alias name of the job does not resolve to an alias
  of the same kind and platform. A job that named an alias maps to that
  alias for the drain only when the kind and the platform agree.
- A retry of a direct-reference job that was created before migration
  000072 has no recorded alias. The retry uses an alias that pins the
  digest now. When no alias pins it, the retry is refused.
- The delete check and the job creation do not use one lock. A job that
  the coordinator creates while you delete the alias is refused or keeps
  its digest as above.
- A Kubernetes node that joins after the pre-seed can get a job before the
  DaemonSet pulls the image to that node.
- Two aliases can pin one digest at two locations (for example, a registry
  and S3). The image set gives one reference for the digest: the first
  alias reference in sort order. The reference of a job is used only when
  no alias pins the digest.
