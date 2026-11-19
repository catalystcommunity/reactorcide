# Image Aliases, Worker Image Sets, and Image Routing

Status: plan, 2026-11-17. Owner decisions complete (I1-I18). Building.

## Goal

A macOS or Windows VM job names an image by an alias. The coordinator
resolves the alias to a pinned reference when it creates the job. Only
workers that already have that image claim the job. A job never waits for an
image download. Workers get new images without a restart.

## Owner decisions

| ID | Decision |
| --- | --- |
| I1 | Jobs name VM images by alias. Each alias pins one reference. A full pinned reference in a job is also accepted. |
| I2 | Logs, events, job records, and UI show the alias and the reference together when the job used an alias, and the reference alone when it did not. |
| I3 | A tag is a free-form string. A hash is a valid tag. The pin is the content digest, not the tag. |
| I4 | A worker pulls every image that it can be asked to run before it is ready. A worker never pulls an image during a job. |
| I5 | One worker can hold N images. |
| I6 | Organization admins manage organization aliases. A repository (project) can add more aliases. |
| I7 | Reactorcide does not publish base images. It publishes the tools and instructions to build them and to store them in a store of the user's choice (OCI registry or S3). |
| I8 | Workers get new image sets while they run. A worker reports `image_updating: N` and then `image_updated: N` for image set version N. The coordinator does not send version N again to a worker that reports it. The UI shows "healthy" or "updating images". |
| I9 | The instructions for user images start with how to create a base image and put it in the store of the user's choice. |
| I10 | When an alias gets a new pin, new jobs use the old pin until every worker that serves the alias has the new image. Then the alias changes to the new pin. Other rollout strategies are later work. |
| I11 | An alias goes only to the workers that can serve it (scope, pool, platform, and characteristics). |
| I12 | A worker continues to run jobs for the images that it has while it gets new images. |
| I13 | Container images can have aliases too. Workers pre-seed them. On Kubernetes, pre-seed the nodes where possible (the worker or the coordinator watches and pre-seeds). |
| I14 | `workers list` (CSIL `list-workers`) accepts an API token, with the same check as a session. Revised 2026-11-18: to see workers needs read permission, not control. A token needs `workers:read` or `workers:manage` for the pool organization (`authz.Caps.ViewWorkers`); without a pool, the owner must be a global admin and the token must be for all organizations. The worker mutations (`set-worker-status`, `drain-worker`), `list-pools`, and `list-enrollment-tokens` also accept a token (owner principle: "there is nothing special about a browser"). See docs/ui-auth.md "Sessions and API tokens". |
| I15 | Windows job examples stop at the first error: `$ErrorActionPreference = 'Stop'` and a `$LASTEXITCODE` check after each external program (Windows PowerShell 5.1 is the image default). Reason: with `-Command`, the exit code shows only the last statement. (2026-11-18) |
| I16 | A job that can never run with its alias (os, arch, kind, characteristic, or worker class conflict) is refused at creation. The error names the alias, the key, the job value, the alias value, and the fix. REST and `submit` show it; a workflow job fails its node (`not_admitted`); an eval job fails its CI event. (2026-11-18) |
| I17 | A retry or a rerun after an alias update is allowed and uses the current pin. A job that named an alias resolves that alias again. A job with a direct pinned reference resolves through the alias that pinned it at creation (`jobs.image_pin_alias`, migration 000072); it keeps the digest while that alias still pins it. The new job records `image_retry_of`. Refused only when the alias is deleted, is for another kind or platform, does not fit, or the policy refuses. Triggered children keep the parent image as before. (2026-11-18) |
| I18 | `image_retry_of` is job metadata where it is useful: REST, CSIL `JobSummary`, `reactorcide jobs get`, the job page of the web UI (not job lists), the worker lease (`Lease.image_retry_of`), and a second job log header line `[reactorcide] retry of: <earlier image>`. The coordinator log keeps the retry log line. (2026-11-18) |

## Current state (2026-11-17)

- VM images are OCI 1.1 artifacts (`application/vnd.reactorcide.vm-image.v1`).
  `vm-image publish`, `pull`, and `cache prune` exist. The job `image` field
  is an OCI reference.
- The worker pulls on the first job that needs an image, or before it
  registers for each `--vm-image-prefetch` reference. Retention removes images
  that were not used for 30 days.
- Routing does not know about images. A job can go to a worker that must
  first download 30 to 80 GB.
- `vm-image build macos` makes a derived image from a base and provisioning
  scripts. Windows has only the ISO builder (`build-windows-image.ps1`).
- Routing uses characteristic queues. A queue is a canonical set of
  characteristics. At `RequestJob` the coordinator finds every queue that the
  worker satisfies and claims across all of them with one Corndogs
  `GetNextTaskGroup`. A list-valued worker characteristic satisfies a scalar
  queue value that is in the list.
- Corndogs has no queue-count limit. PostgreSQL limits the `queue IN (...)`
  claim to 65,535 bind values. The CSIL frame limit (16 MiB) allows about
  400,000 queue IDs. Neither limit is near for this design.

## Design

### References

A pinned reference names a location and a content digest:

- OCI: `oci://registry.example.com/team/vm/windows-11-vs2022:<tag>@sha256:<digest>`
- S3: `s3://bucket/prefix/windows-11-vs2022:<tag>@sha256:<digest>`

The tag is free form (I3). The digest is the digest of the OCI manifest. An
S3 store uses the OCI image layout (`index.json`, `blobs/sha256/...`) under
the prefix, so both stores keep the same bytes and the same digest. The pull
verifies every blob digest before it extracts the layer (as now).

An unpinned reference (no digest) is accepted only by image tools
(`vm-image publish`, `alias set --resolve`). Job creation and worker image
sets need a digest.

### Aliases

- An alias has a name, a pinned reference, a platform (`os`, `arch`), and
  optional extra worker characteristics (for example
  `worker_class: mac-xcode`) that limit which workers get the image.
- The kind, the platform, and the characteristics cannot change after the
  create. `alias set` changes only the pin. A different platform or
  constraint is a new alias name. Reason: a change would send jobs to
  workers that do not have the image, without a rollout.
- A job that can never run with the image of its alias is refused at
  creation: its `os` or `arch` is not the platform of the alias, an alias
  characteristic has another value than the job characteristic, or the
  alias `worker_class` is not the worker class of the job.
- Scopes: organization (org admin, I6) and project. A project alias with the
  same name as an organization alias replaces it for that project.
- Each alias change is an audit record (old reference, new reference, actor).
- `reactorcide vm-image alias set|get|list|delete`, CSIL operations, and an
  admin UI page.
- The coordinator resolves `image: <alias>` once, at job creation, and stores
  `vm_image_alias` and `vm_image_ref` on the job. A retry and a rerun resolve
  the alias again and use its current pin (I17).

### Image routing

- The resolved digest becomes a job characteristic:
  `vm_image: "sha256:<digest>"`. Different images give different queues
  through the current find-or-create path.
- A worker sends its ready digests as the list characteristic `vm_image`.
  The current satisfies rule then routes each job only to a worker that has
  its image. No new matching code is needed.
- A digest that no alias pins and no job queue uses is retired. The queue
  stays until it is empty and is then deleted.

### Worker image sets

- The coordinator computes the image set for each worker: all aliases (in
  every scope that the worker serves) whose platform and extra
  characteristics the worker satisfies. The set has a version number that
  increases when the set changes.
- The register response and the heartbeat response carry the set and its
  version when the version that the worker reports is older (I8).
- At startup the worker pulls the full set before its first `RequestJob`
  (I4). Before that, it reports `image_updating: N`.
- While it runs, the worker pulls a new set in the background. It reports
  `image_updating: N`, and it continues to claim jobs for images that it
  already has. When the pull is complete, it reports `image_updated: N`, and
  its `vm_image` list includes the new digests.
- The worker keeps an image that its current set names. Retention removes
  only images that are not in the set.
- A pull that fails (store error, no disk space) gives
  `image_update_failed: N` with the reason. The worker keeps its old images
  and claims jobs for them. It tries again with backoff.
- The worker checks the free disk space before a pull and reports the set
  size and the free space.

### Logs and metadata (I2)

- Job log header, job events, outbound events, metrics labels, and the UI
  show `windows-vs2022 (oci://...@sha256:...)` for an alias, and the
  reference alone for a direct reference.
- The worker logs each pull with the alias names that use the digest.

### User images (I7, I9)

- Add `vm-image build windows --from <ref> --provision script.ps1
  --publish <ref>`, the Windows equivalent of `vm-image build macos`.
- Add `vm-image publish` and `pull` support for `s3://`.
- New guide `docs/vm-images-quickstart.md`: create a base image (macOS:
  Tart or the macOS installer; Windows: `build-windows-image.ps1`), publish
  it to an OCI registry or S3, build derived images, set an alias, and see
  workers become ready.

## Work packages

1. References: `oci://` and `s3://` pinned reference parse and format, S3
   image source (OCI layout in S3), tests.
2. Aliases: migration, store, CSIL operations, CLI, audit, resolution at job
   creation, `vm_image_alias` and `vm_image_ref` on jobs, retry and rerun.
3. Routing: `vm_image` job characteristic, worker `vm_image` list
   characteristic, queue retirement.
4. Image sets: set computation and versions, register and heartbeat
   transport, worker states, startup pre-seed gate, background update,
   retention that keeps the set, disk checks.
5. Display: logs, events, UI (worker image state, alias page), metrics.
6. Windows derived build and the user image guide.
7. Hardware validation on lab-wincide and ht0-macbookm5: pre-seed, live
   update with a running job, failed pull, and alias change.

## Contracts

These names are fixed. All work packages use them.

### Database (migration 000068)

`image_aliases`:

| Column | Type | Note |
| --- | --- | --- |
| `alias_id` | uuid pk | |
| `org_id` | text not null | owner organization |
| `project_id` | text null | null for an organization alias |
| `name` | text not null | `^[a-z0-9][a-z0-9._-]{0,62}$` (no `/`, `:`, `@`) |
| `kind` | text not null | `vm` or `container` |
| `os`, `arch` | text not null | platform that the image runs on |
| `characteristics` | jsonb not null default `{}` | more worker characteristics that the worker must satisfy |
| `active_ref`, `active_digest` | text not null | the pin that new jobs get |
| `pending_ref`, `pending_digest` | text null | the next pin, during a rollout (I10) |
| `pending_since` | timestamptz null | |
| `created_at`, `updated_at`, `updated_by` | | |

Unique: (`org_id`, coalesce(`project_id`, ''), `name`).

`jobs` gets `image_alias text null`. The resolved pinned reference goes in
the existing image field (`container_image` for VM and container jobs).

Migration 000072 (I17): `jobs` gets `image_pin_alias text null` (the alias
that pinned a direct pinned reference at creation) and `image_retry_of text
null` (the display image of the earlier run when a retry got a new pin).
`workflow_nodes` gets `carried_image_pin_alias text null`.

`workers` gets `image_set_version bigint not null default 0` (the version
that the coordinator wants), `image_set_hash text`, `image_state text`
(`updating`, `updated`, `failed`), `image_state_version bigint`,
`image_state_detail text`, `image_ready_digests jsonb`.

### Worker CSIL

```
ImageSetEntry = { ref: text, digest: text, kind: text, aliases: [* text], ? pending: bool }
ImageSet = { version: int, images: [* ImageSetEntry] }
ImageFailure = { digest: text, permanent: bool, detail: text }
ImageSetReport = { version: int, state: text, ? detail: text, ready_digests: [* text],
                   ? free_bytes: int, ? required_bytes: int, ? failed: [* ImageFailure] }
RegisterResponse  += ? image_set: ImageSet
HeartbeatRequest  += ? image_set_report: ImageSetReport
HeartbeatResponse += ? image_set: ImageSet   ;; only when the report version is not the wanted one
```

`pending` is true for an entry that is only a pending pin (not the active
pin of a served alias and not a drain digest). The startup gate waits for
every entry that is not pending; a missing value is not pending (strict).
`state` is `updating`, `updated`, or `failed`. A worker that sends no report
(an old worker) gets no image set and routes as before.

### Routing

- Job characteristic `image_digest: "sha256:..."` is added for each job whose
  image is an alias (VM or container) or a VM pinned reference.
- A worker sends the custom characteristic `image_digest: [ready digests]`
  on each `RequestJob`. The existing satisfies rule does the match.
- A VM job with a pinned reference that no alias in its scope pins is
  refused at creation: no worker can have it (I4).
- A container job with a plain reference routes as before (no
  `image_digest`).

### Resolution

- Resolution happens once, at job creation (REST, submit, triggers,
  workflow jobs, schedules). Retry and rerun resolve again through the alias
  of the earlier run (`imagealias.ResolveRetry`, I17).
- `image: <name>`: a project alias with that name, then an organization
  alias. If neither exists, the value is a reference (container jobs only).
- The organization image policy checks the resolved reference.
- Display (I2): `name (ref)` when `image_alias` is set, else `ref`.

### Rollout (I10)

- `alias set` with a new reference sets `pending_*`. The image set of each
  serving worker then has both digests.
- The promotion loop (one coordinator replica, under a lease) moves
  `pending_*` to `active_*` when every online serving worker has the
  pending digest in `image_ready_digests`, and at least one serving worker
  is online. The overall state of the worker does not count, so a failed
  image of another alias (another organization on a hosted pool) does not
  block the promotion. `alias set --force` promotes at once.
- The worker pulls each image independently and reports each failure with
  its class (permanent or transient, `ImageSetReport.failed`, migration
  000071). The startup gate opens when each required entry is ready or
  failed permanently; a transient failure keeps it closed. The rollout
  status shows permanently failed active and pending pins as problems for
  the alias owner, and shows only counts for pools that the caller cannot
  manage.
- A job with a direct pinned reference stores the reference of the alias
  (same kind and platform) that pins its digest. Image sets use alias
  references for a digest when an alias pins it.
- A draining job maps to the alias of its name (project, then
  organization) only when the kind and the platform agree. A triggered
  child that keeps an image is refused when no worker can get it
  (`imagealias.CheckCarried`). A retry and a rerun use
  `imagealias.ResolveRetry` (I17).
- A new image set stops only the pulls of digests that left the set. A
  worker accepts a set with a lower version when the content is different
  (re-enroll); the coordinator sends the set when the report version is
  not the wanted version.
- An old digest stays in image sets while a non-terminal job uses it. Then
  it leaves the sets, and workers can remove it. A non-terminal job with a
  direct pinned VM reference keeps its digest in the sets of the workers
  that could run it (pool class grant, characteristics) until it ends
  (migration 000070 index).
- `alias delete` is refused while a non-terminal job uses the alias (by
  name, or by a direct pinned reference to its active or pending digest).

### Kubernetes pre-seed (I13)

The Kubernetes worker keeps one DaemonSet for each image set, named
`<prefix>-<short hash of the sorted image list>`, with the node selector
and tolerations of its jobs. The full hash is an annotation; the status
counts only when it matches. Desired 0 is not ready. Old DaemonSets of the
same prefix and managed-by label are removed when no runner of the process
uses them and they are older than 30 minutes (creation and used-at). Each image is an init container that
runs a static no-op binary from a shared `emptyDir`. The worker reports
`updated` when the DaemonSet has all pods ready at the current generation.
A node that joins later pulls through the DaemonSet; a job can reach it
before the pull completes (documented limit).

## Open questions

None. Owner answers 2026-11-17 are I10-I13; 2026-11-18 are I14-I18.
