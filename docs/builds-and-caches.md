# Builds, Caches, and Job Settings

This page tells how Reactorcide segments build caches, which facts a job
gets about its cache, how a job publishes links, how operators set
environment defaults, how the builder runs without root, and how an
organization limits job images.

Reactorcide does not know any build tool (owner decision D6). A remote
cache or a remote execution service is an external system that a job calls.
Reactorcide gives generic parts only: a cache namespace, a write mode, ref
classes, environment defaults, and links.

## Cache Namespaces

The coordinator computes a cache decision for each job when a worker claims
the job. The decision comes from coordinator records only: the execution
profile of the job and the ref classes of the job
([Ref Classes](./ref-classes.md)). The coordinator sends the decision in the
lease. The worker uses it. The worker and the job cannot select a different
namespace.

### Rules

| Item | Rule |
| --- | --- |
| Base namespace | The profile `cache_namespace`. When it is empty, the profile name. A job with no profile uses `standard`. |
| Ref class split | The profile `cache_namespace_classes` is an ordered list. The first class in the list that the job has is added as a suffix: `<base>-<class>`. An empty list gives one namespace for the profile. |
| Artifact namespace | The profile `artifact_namespace`, or the base namespace, with the same suffix. |
| Write mode | Writable when the profile has `trusted_cache_writes: true` and, when the profile has `cache_write_classes`, the job has one of those classes. |
| Missing profile | No writes. The built-in `standard` profile is the exception: it writes, as before namespaces. |

A namespace name has lowercase letters, digits, `.`, `_`, and `-`, and at
most 63 characters. The coordinator changes other characters to `-` and adds
a short hash, so that two different names stay different.

Example: a profile with `cache_namespace: main`,
`cache_namespace_classes: [release, default]`, and
`cache_write_classes: [default, release]`:

| Job ref classes | Namespace | Writable |
| --- | --- | --- |
| `default` | `main-default` | yes |
| `release`, `tag` | `main-release` | yes |
| `pr` | `main` | no |
| `pr`, `fork` | `main` | no |

A child profile must keep the `cache_namespace_classes` of its parent, and
it can only narrow `cache_write_classes`
([Organizations and CI Policy](./organizations-and-ci-policy.md)).

### Compatibility

The default deployment does not change:

- Existing profiles have empty `cache_namespace_classes` and
  `cache_write_classes` (migration `000060`).
- The `standard` profile has the namespace `standard` and writes.
- The namespace `standard` uses the same builder cache volume as before
  (`REACTORCIDE_BUILDER_CACHE_VOLUME`).
- An older coordinator sends no cache decision. A new worker then uses the
  shared volume with writes, as before.

### The shared namespace

The coordinator setting `REACTORCIDE_CACHE_SHARED_NAMESPACE` names the one
namespace that uses the shared, operator-named volume. The default is
`standard`. The shared volume is shared by all organizations on a worker, as
before namespaces.

Every other namespace gets a volume key: the namespace name and a hash of the
organization ID and the namespace. Two organizations with the same namespace
name never share a volume.

To give every namespace its own volume, set
`REACTORCIDE_CACHE_SHARED_NAMESPACE=""` on the coordinator. The `standard`
namespace then starts with a cold cache one time.

### Builder cache volumes

A job with the `builder` capability gets a BuildKit sidecar. The worker
mounts the cache of the namespace at the BuildKit state directory.

| Runner | Shared namespace | Other namespace | No writes |
| --- | --- | --- | --- |
| Docker, containerd | Named volume `<volume>` | Named volume `<volume>-<key>` | No cache mount |
| Docker, containerd with an absolute host path | `<path>/shared` | `<path>/namespaces/<key>` | No cache mount |
| Kubernetes | Sub path `shared` of the PVC `<volume>` | Sub path `namespaces/<key>` of the same PVC | No cache mount |

Docker and nerdctl make a named volume when it does not exist. The kubelet
makes the sub path directory. Thus no new PVC and no new RBAC are necessary.
Use a `ReadWriteMany` PVC when builder pods run on more than one node.

BuildKit needs a writable state directory. Thus "no writes" means that the
sidecar starts with no persistent cache state. A job with no writes cannot
change a cache that other jobs read. It still can read an external cache
(for example a registry cache with `--import-cache`), with credentials that a
secret grant or OIDC gives for its ref classes.

Each namespace, also the shared one, mounts only its own part of the
volume. The shared namespace uses `shared/`, a sibling of `namespaces/`, so
no builder can reach the cache of another namespace. Docker and containerd
named volumes are separate volumes, so the shared namespace keeps the
operator volume itself.

Release note (upgrade): before this layout, the shared namespace on
Kubernetes and with a host path used the ROOT of the volume, which contained
`namespaces/` of every other namespace. A builder of the shared namespace
could read and change them. After the upgrade, the shared namespace starts
one time with a cold cache in `shared/`. An operator can delete the old
BuildKit state at the root of the volume (everything except `shared/` and
`namespaces/`).

### Secure defaults

The secure defaults profile of an organization
(`reactorcide orgs set-defaults ORG --profile secure`, and every new
organization) writes `cache_write_classes: [default, tag]` on the
`standard` execution profile of the organization. Thus a pull request job,
a schedule, and a manual run read no persistent cache state and cannot
change the cache that default-branch and tag builds use. The legacy profile
writes `cache_write_classes: []` (writes for every ref class, the behavior
before). An admin can change the value later with
`reactorcide profiles`; the profile only writes it when it is applied.

## Reserved Job Facts

The coordinator sets these values in the environment of each job. It sets
them after the job variables, so a job, a workflow, a trigger, or an
environment default cannot replace them. Runnerlib refuses them in workflow
and job YAML.

| Variable | Value |
| --- | --- |
| `REACTORCIDE_CACHE_NAMESPACE` | The cache namespace, for example `main-default` |
| `REACTORCIDE_CACHE_WRITABLE` | `true` or `false` |
| `REACTORCIDE_ARTIFACT_NAMESPACE` | The artifact namespace |
| `REACTORCIDE_REF_CLASSES` | The ref classes, comma separated ([Ref Classes](./ref-classes.md)) |

A job command uses these values to select an external cache instance or a
cache prefix. The job code needs no trust logic. For example:

```sh
cache="registry.example/cache/${REACTORCIDE_CACHE_NAMESPACE}"
args="--import-cache type=registry,ref=${cache}"
if [ "${REACTORCIDE_CACHE_WRITABLE}" = "true" ]; then
  args="${args} --export-cache type=registry,ref=${cache},mode=max"
fi
buildctl build ${args} ...
```

The namespace name is local to the organization. When organizations share
one external cache, put the organization in the prefix, or give each
organization its own cache credentials.

`run-local` sets the cache values from the local context. The cache is
read-only (`REACTORCIDE_CACHE_WRITABLE=false`) unless you give
`--cache-writable` ([Runtime Behavior](./runtime-behavior.md#job-facts-and-defaults)).

## Job Links

A job can publish named links, for example an external build results page.
Write a `links` key in the workflow output file
(`/job/workflow-output.json`, at most 1 MiB):

```json
{
  "vars": {},
  "links": [
    {"name": "Build results", "url": "https://builds.example/run/42"}
  ]
}
```

Rules:

- A name has 1 to 100 characters and no control characters.
- A URL is absolute, uses `https` or `http`, has a host, has no user
  information, and has at most 2048 characters.
- A job has at most 20 links. Names are unique.
- The coordinator drops an invalid link and logs the reason. An invalid link
  does not fail the job.
- A new output replaces all links of the job.

The workflow output file exists for workflow jobs (`RC_WF_OUTPUT_FILE`). On
Kubernetes, the worker reads it only for workflow jobs.

The links show in:

- The job page of the UI.
- The PR comment entry of the workflow or the job.
- REST: `GET /api/v1/jobs/JOB_ID/links`.
- CSIL: `list-job-links`.
- CLI: `reactorcide job-links JOB_ID`.

## Environment Defaults

An operator can set non-secret environment defaults per organization, per
worker class, and per project. Examples: package proxy URLs (`GOPROXY`,
`PIP_INDEX_URL`, `npm_config_registry`) and registry mirror names.

Precedence, highest first:

1. The job environment (job, workflow, and coordinator values)
2. Project defaults
3. Worker class defaults (the worker class of the job)
4. Organization defaults

A default fills a key only when the job environment does not have the key.

Limits:

- A key matches `[A-Za-z_][A-Za-z0-9_]*` and has at most 128 characters.
- `REACTORCIDE_*` and `RC_WF*` keys are reserved. The API and the database
  refuse them.
- A value has at most 8192 bytes and cannot hold a `${secret:...}`
  reference. Defaults are not secret: use a
  [secret grant](./vcs-credentials-and-secret-grants.md) for a secret.
- A scope has at most 100 defaults.

The coordinator applies the defaults when a worker claims the job. A change
applies to jobs that a worker claims after the change.

### REST

| Method | Path | Capability |
| --- | --- | --- |
| `GET` | `/api/v1/organizations/ORG/env-defaults` | `organizations:read` |
| `PUT`, `DELETE` | `/api/v1/organizations/ORG/env-defaults/KEY` | `organizations:manage` |
| `GET` | `/api/v1/organizations/ORG/worker-classes/CLASS/env-defaults` | `workers:read` |
| `PUT`, `DELETE` | `/api/v1/organizations/ORG/worker-classes/CLASS/env-defaults/KEY` | `workers:manage` |
| `GET` | `/api/v1/projects/PROJECT_ID/env-defaults` | `projects:read` |
| `PUT`, `DELETE` | `/api/v1/projects/PROJECT_ID/env-defaults/KEY` | `projects:manage` |

The `PUT` body is `{"value": "..."}`. The admin API token has all of these
capabilities.

### CLI

```bash
reactorcide env-defaults set --org acme GOPROXY=https://goproxy.internal
reactorcide env-defaults set --org acme --worker-class fast PIP_INDEX_URL=https://pypi.internal/simple
reactorcide env-defaults set --project "$PROJECT_ID" NPM_CONFIG_REGISTRY=https://npm.internal
reactorcide env-defaults list --org acme
reactorcide env-defaults unset --org acme GOPROXY
```

### UI

The organization settings page and the worker class page have an
"Environment defaults" form. The UI needs an org admin.

## Registry Mirrors for BuildKit

The registry mirrors of the BuildKit sidecar are operator configuration. They
are not job settings. Put them in the `buildkitd.toml` of the builder
(`REACTORCIDE_BUILDER_CONFIG_PATH`; Helm `builder.configMap`):

```toml
[registry."docker.io"]
  mirrors = ["mirror.internal:5000"]

[registry."mirror.internal:5000"]
  http = false
  ca = ["/etc/buildkit/certs/internal-root.crt"]
```

See `deployment/buildkitd.example.toml`. A job cannot change this file.

Reactorcide does not host a dependency mirror. Run a pull-through proxy and
point the defaults at it. To make the proxy the only path, also use
[egress control](./egress-proxy.md). Without egress control, "use the
mirror" is a convention, not a guarantee.

## Rootless Builder

By default the BuildKit sidecar is privileged and runs as root. The operator
can select the rootless mode:

| Setting | Value |
| --- | --- |
| Worker environment | `REACTORCIDE_BUILDER_ROOTLESS=true` |
| Helm | `builder.rootless: true` |

In the rootless mode:

- The image is `moby/buildkit:v0.17.3-rootless` when
  `REACTORCIDE_BUILDER_IMAGE` is empty or is the default image. Set
  `REACTORCIDE_BUILDER_IMAGE` to use a different rootless image.
- The sidecar is not privileged. It runs as uid 1000.
- Docker and containerd: `--security-opt seccomp=unconfined` and
  `--security-opt apparmor=unconfined`.
- Kubernetes: `runAsUser: 1000`, `runAsNonRoot: true`, and `Unconfined`
  seccomp and AppArmor profiles. The cluster must permit these settings
  (for example the Pod Security level `baseline` does not permit an
  Unconfined seccomp profile; use `privileged` for the job namespace, or an
  exemption for the builder).
- buildkitd runs with `--oci-worker-no-process-sandbox`. Build steps then
  share the process namespace of the sidecar.
- The state directory is `/home/user/.local/share/buildkit`. The registry
  auth file mounts at `/home/user/.docker/config.json`. The `buildkitd.toml`
  mounts at `/etc/buildkit/buildkitd.toml`.

The node kernel must permit unprivileged user namespaces. The rootless mode
is slower on some kernels. Test it on your nodes before you turn it on.

## Image Policy

An organization can limit the images of its jobs. The policy is off by
default.

| Field | Meaning |
| --- | --- |
| `enabled` | Turns the policy on. |
| `allowed_prefixes` | Image reference prefixes, for example `ghcr.io/acme/` or `docker.io/library/`. A prefix matches at a boundary (`/`, `:`, `@`), so `ghcr.io/acme` does not match `ghcr.io/acme-evil/x`. No wildcards. |
| `require_digest` | Refuses an image with no `@sha256:` digest. |

The coordinator compares the full reference too: `postgres:17` is
`docker.io/library/postgres:17`. With the policy on and no prefixes, no image
is permitted.

The policy applies to the job image: the `image` of the job, or the runner
image of the project, or the default runner image of the worker. It does not
apply to the operator images (the BuildKit sidecar, DinD, and the egress
images).

Enforcement:

- REST job submit: the coordinator refuses the job with HTTP 403 and the
  error `image_policy`.
- Claim: the coordinator checks every job again when a worker claims it. This
  covers jobs from workflows and triggers. A refused job fails with the
  failure class `job` and the error
  `image policy denied: image "..." is not under an allowed registry prefix ...`.

A policy change applies to jobs that a worker claims after the change.

```bash
reactorcide image-policy set --org acme --enabled \
  --allow ghcr.io/acme/ --allow docker.io/library/ --require-digest
reactorcide image-policy get --org acme
```

REST: `GET` and `PUT /api/v1/organizations/ORG/image-policy` (`PUT` needs
`policies:manage`). CSIL: `get-image-policy`, `put-image-policy`. The
organization settings page of the UI has an "Image policy" form.

## Audit

Each change writes an audit record: `env_default.set`, `env_default.delete`,
and `image_policy.set`.
