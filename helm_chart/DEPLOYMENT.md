# Kubernetes Deployment

This guide installs Reactorcide with the Helm chart in this repository.

## Architecture

The chart can install:

- Coordinator deployment and service
- Worker deployment
- Optional web application
- Optional PostgreSQL StatefulSet
- Optional Corndogs subchart
- Worker service account and namespace Role
- Bootstrap API-token and worker-enrollment Secrets
- Optional Gateway API HTTPRoutes

The worker uses the Kubernetes backend and creates a Kubernetes Job for each
Reactorcide job. It connects only to the coordinator. The coordinator connects
to PostgreSQL, Corndogs, and object storage.

## Requirements

You need:

- A Kubernetes cluster that supports `batch/v1` Jobs.
- Helm 3.
- `kubectl` access to the target namespace.
- Access to the Reactorcide and runner image registry.
- A default StorageClass if you enable persistent bundled services.
- A Gateway API controller if you enable HTTPRoutes.

We recommend `fs.inotify.max_user_instances = 8192` or higher on each Linux
node that can run job Pods. See [Linux worker node configuration](../docs/workers.md#linux-worker-node-configuration)
for commands to apply and check this setting.

## Important Defaults

The default `values.yaml` is a configuration reference. These defaults do not
form a complete production installation:

- Bundled PostgreSQL is disabled.
- Bundled Corndogs is disabled.
- The default runner image is empty.
- The object store is ephemeral filesystem storage.
- The web application is disabled.
- VCS integration is disabled.
- Worker enrollment is automatic.
- Pull-request evaluation uses the `isolated` checkout mode.
- Audit events have an age limit of 365 days.

Select each required dependency before installation.

## Evaluation Installation

This installation uses bundled PostgreSQL, bundled Corndogs, and ephemeral
object storage. Data can be lost. Do not use these values for production.

Create `values-evaluation.yaml`:

```yaml
app:
  image:
    tag: "dev"

worker:
  containerRuntime: "kubernetes"
  image:
    tag: "dev"

web:
  enabled: false

defaults:
  runnerImage: "containers.catalystsquad.com/public/reactorcide/runnerbase:dev"

postgres:
  uri: "postgresql://devuser:devpass@reactorcide-postgresql:5432/reactorcide?sslmode=disable"

postgresql:
  enabled: true
  auth:
    username: "devuser"
    password: "devpass"
    database: "reactorcide"
  persistence:
    enabled: false

corndogs:
  enabled: true
  storage:
    backend: file
    file:
      persistence:
        enabled: false

objectStore:
  type: filesystem
  basePath: /tmp/reactorcide-objects
```

Install:

```bash
helm upgrade --install reactorcide ./helm_chart \
  --namespace reactorcide \
  --create-namespace \
  --values values-evaluation.yaml \
  --wait \
  --timeout 10m
```

Verify:

```bash
kubectl get pods -n reactorcide
kubectl get jobs -n reactorcide
kubectl get svc -n reactorcide
kubectl get --raw /readyz >/dev/null
kubectl port-forward -n reactorcide svc/reactorcideapp 6080:6080
```

In a second terminal:

```bash
curl --fail http://127.0.0.1:6080/api/v1/health
```

## Create the First API Token

The chart creates the empty `base-reactorcide-api-token` Secret. It does not
create an API token during a direct Helm installation.

The `jobs/deploy-to-k8s.yaml` job does this task automatically. If the Secret
is empty, the job first deploys the coordinator and worker without the web
application. It creates the API token, stores the token in the Secret, and then
enables the web application. You do not have to do the steps in this section
when you use that job.

Create a protected local token file:

```bash
mkdir -p ~/.config/reactorcide
chmod 700 ~/.config/reactorcide

kubectl exec -n reactorcide deployment/reactorcideapp -- \
  /reactorcide token create \
  --name cluster-bootstrap \
  | sed -n 's/^Token: //p' \
  > ~/.config/reactorcide/api-token

chmod 600 ~/.config/reactorcide/api-token
```

Store the token in a Kubernetes Secret without terminal output:

```bash
kubectl create secret generic reactorcide-api-token \
  --namespace reactorcide \
  --from-file=token="$HOME/.config/reactorcide/api-token"
```

Add these values to the installation:

```yaml
app:
  apiTokenSecret:
    name: reactorcide-api-token
    key: token

worker:
  apiTokenSecret:
    name: reactorcide-api-token
    key: token
```

Run `helm upgrade` again. This token lets eval jobs submit workflow children.
The web application does not use an API token. It sends the session of each
browser to the coordinator.

## Production Dependencies

### PostgreSQL

Set:

```yaml
postgresql:
  enabled: false

postgres:
  uri: "postgresql://USER:PASSWORD@HOST:5432/reactorcide?sslmode=require"
```

The current chart stores this URI in the release Secret. Protect the values
source and Helm release records. Do not commit the URI.

### Corndogs

Use the bundled subchart:

```yaml
corndogs:
  enabled: true
  storage:
    backend: file
    file:
      persistence:
        enabled: true
        size: 10Gi
```

Or use an external Corndogs address:

```yaml
corndogs:
  enabled: false
  baseUrl: "corndogs.example.internal:5080"
```

The value is a CSIL-RPC TCP address. Do not add `http://`.

The subchart is Corndogs chart 0.6.0 (server 0.8.0). Its resilience
policies stay at `compatibility`.

**Known limit:** do not set `corndogs.resilience.submissionKeyPolicy:
required` (`CORNDOGS_SUBMISSION_KEY_POLICY=required`). Reactorcide submits
jobs without a submission key, so a Corndogs server with this policy refuses
each job. This also applies to an external Corndogs server. Keep the default
`compatibility`. Back up the file store before you upgrade
from Corndogs 0.7.x, and stop the old server before the new one starts. A
downgrade to 0.7.x is not supported after guarded tasks exist. See
[Upgrade to 0.13](../docs/upgrade-0.13.md#21-corndogs-080).

### Object storage

Use S3 or an S3-compatible service for durable logs:

```yaml
objectStore:
  type: s3
  bucket: reactorcide-objects
  prefix: reactorcide/
  s3:
    region: us-east-1
    endpoint: ""
    accessKeyId: ""
    secretAccessKey: ""
```

Set `endpoint` for an S3-compatible service. The current chart stores S3
credentials in the release Secret. Do not commit them in a values file.

The `gcs` values are reserved. The coordinator does not implement a GCS object
store.

### Secret encryption

Create the master-key Secret from a protected file:

```bash
openssl rand -base64 32 > ~/.config/reactorcide/master-key
chmod 600 ~/.config/reactorcide/master-key

{
  printf 'primary:'
  tr -d '\n' < ~/.config/reactorcide/master-key
  printf '\n'
} > ~/.config/reactorcide/master-keys
chmod 600 ~/.config/reactorcide/master-keys

kubectl create secret generic reactorcide-master-keys \
  --namespace reactorcide \
  --from-file=master-keys="$HOME/.config/reactorcide/master-keys"
```

Reference it:

```yaml
secrets:
  storageType: database
  existingSecret: reactorcide-master-keys
  existingSecretKey: master-keys
```

Keep the master key stable. Back it up separately from PostgreSQL. Encrypted
secret rows cannot be recovered without it.

## Worker Configuration

Use the Kubernetes runtime:

```yaml
worker:
  enabled: true
  containerRuntime: kubernetes
  concurrency: 2
  jobNamespace: reactorcide
  dataDirPersistence:
    enabled: true
    size: 1Gi
```

The retained PVC in this example keeps the worker identity after Pod deletion.
Use a Pod-owned ephemeral PVC when the spool must use cloud network storage but
must not survive Pod deletion:

```yaml
worker:
  dataDirEphemeralPVC:
    enabled: true
    storageClass: network-ssd
    size: 2Gi
```

The ephemeral PVC survives a container restart in the same Pod. Kubernetes
deletes the PVC when it deletes the Pod. A new Pod gets a new PVC and a new
worker identity. Do not enable `dataDirPersistence` and
`dataDirEphemeralPVC` together.

The chart creates a worker enrollment Secret by default. The coordinator and
worker read the same Secret. Helm keeps it after uninstall.

Use an operator-managed enrollment Secret when required:

```yaml
worker:
  enrollmentTokenSecret:
    name: reactorcide-worker-enrollment
    key: token
```

The worker Role can create and delete Jobs and temporary checkout Secrets. It
can manage the image pre-seed DaemonSets (`apps/daemonsets`), list events, and
exec into job Pods (`pods/exec`, to read the workflow output). It can read
Pods, Pod logs, and Pod resource metrics in its namespace. A cluster role
permits `list` on `nodes` (capacity, topology, and scheduling decisions) and
reads from `nodes/proxy` for volume statistics. The worker also reads CPU and
memory there when metrics-server has no data for a job pod. Review
`templates/rbac-worker.yaml` before you change its namespace or service
account.

Optional worker settings. The defaults keep a fixed lease count and the
in-cluster connection:

```yaml
worker:
  admission:
    mode: resources        # default: concurrency
    maxLeases: 64
    cpu: "64"              # budget; set it when a node autoscaler adds nodes
    memory: "256Gi"
  region: us-east-1        # detected from node labels when empty
  kubeconfig:
    secretName: reactorcide-remote-kubeconfig   # run jobs in a remote cluster
```

See [Worker Operation](../docs/workers.md) ("Resource-aware admission",
"Backlog and autoscaling", "Region, zone, and cloud", and "Remote Kubernetes
cluster").

### Job Pod Settings

These `worker` values control the Kubernetes Jobs that the worker creates:

| Value | Default | Meaning |
| --- | --- | --- |
| `jobNamespace` | empty (the worker namespace) | Namespace of job Pods. |
| `jobServiceAccount` | the worker service account | Service account of job Pods. |
| `jobResourceScope` | empty (`auto`) | `auto`, `pod`, or `container`. See "Resources" in [Worker Operation](../docs/workers.md#resources). |
| `jobStartupTimeout` | `30m` | Maximum wait for the main container to start. |
| `jobAutoscaling` | `auto` | Set `disabled` only when no node autoscaler can add capacity. See [Kubernetes scheduling decisions](../docs/workers.md#kubernetes-scheduling-decisions). |
| `jobNodeSelector`, `jobTolerations`, `jobAffinity` | empty | Placement of job Pods and of the image pre-seed DaemonSet. |
| `imagePreseed.name` | `<worker name>-image-preseed` | DaemonSet name prefix, 52 characters or less. |
| `imagePreseed.toolsImage`, `.pauseImage`, `.timeout` | empty (`busybox:1.36.1-musl`, `registry.k8s.io/pause:3.10`, `30m`) | Images and timeout of the pre-seed DaemonSet. See [Kubernetes image pre-seed](../docs/workers.md#kubernetes-image-pre-seed). |
| `jobApiUrl` | this release's coordinator Service | Coordinator API address of job Pods. Set it for a remote cluster. |
| `extraEnv` | empty | More worker environment variables (`{name, value}` or `{name, valueFrom}`). |

The worker pulls the container images of its image set with the pre-seed
DaemonSet before it requests jobs that use them.

### Egress Proxy

Set `egressProxy.enabled: true` to run the egress proxy and to give the worker
its settings. Create the Secret that `egressProxy.secretName` names before you
install. It must have the keys `ca.crt`, `ca.key`, `job.key`, and
`control.token`. Do not put these values in a values file. See
[Egress Proxy](../docs/egress-proxy.md#8-worker-settings) for all
`egressProxy` values.

### Private Job Images

Four settings control private image pulls. Each setting holds Kubernetes
Secret names only, never credential values:

- Top-level `imagePullSecrets` lets the Reactorcide service pods pull their
  own images.
- `worker.jobImagePullSecrets` adds the listed Secrets to every generated
  job pod.
- `worker.allowedJobImagePullSecrets` permits a job to request the listed
  Secrets with its own `image_pull_secrets` list. With an empty list, the
  worker rejects every job-level request.
- Job `image_pull_secrets` selects secrets for one job.

```yaml
worker:
  allowedJobImagePullSecrets:
    - regcred
```

Each referenced Secret must exist in the job namespace, have type
`kubernetes.io/dockerconfigjson`, and contain credentials for the registry in
the selected image. See
[Job Image Pull Secrets](../docs/workers.md#job-image-pull-secrets).

## Builder Jobs

A job with `capabilities: [builder]` uses a BuildKit sidecar. Configure
optional resources:

```yaml
builder:
  image: "moby/buildkit:v0.17.3"
  configMap: "buildkit-config"
  authSecret: "registry-auth"
  cachePvc: "buildkit-cache"
```

The registry Secret must have a `config.json` key.

Each cache namespace other than the shared one (`standard`) uses the sub path
`namespaces/<key>` of `cachePvc`. A job with no cache writes gets no cache
mount. Use a `ReadWriteMany` claim when builder pods run on more than one
node.

Set `builder.rootless: true` to run the sidecar without privilege (uid 1000,
`Unconfined` seccomp and AppArmor). The job namespace must permit these
settings. See [Builds, Caches, and Job Settings](../docs/builds-and-caches.md).

## Public Routes and TLS

The chart can create Gateway API HTTPRoutes:

```yaml
app:
  gateway:
    enabled: true
    domains:
      - ci.example.com
    gatewayName: shared-gateway
    gatewayNamespace: gateway-system
    sectionName: https

web:
  enabled: true
  gateway:
    enabled: true
    domains:
      - ci.example.com
    gatewayName: shared-gateway
    gatewayNamespace: gateway-system
    sectionName: https
```

The coordinator route handles `/api/*` and `/csil/*`. The web route handles
other paths.
The referenced Gateway must provide TLS.

Enable VCS integration:

```yaml
vcs:
  enabled: true
  baseURL: "https://ci.example.com"
```

Then use [Connect a VCS Repository](../docs/vcs-setup.md).

## Web Authentication

The web application is disabled by default. UI auth mode `none` permits public
read access to public projects. Use LinkKeys auth for accounts and private
projects.

See [UI Authentication and Authorization](../docs/ui-auth.md) before you
enable the web application on a public route.

## Production Values Checklist

Your production values must set:

- Exact coordinator, worker, web, and runner image tags
- External or persistent PostgreSQL
- External or persistent Corndogs
- S3 object storage
- Secret master-key reference
- API-token Secret references
- Kubernetes runtime for the in-cluster worker
- Resource requests and limits
- TLS route
- VCS base URL when VCS is enabled
- UI authentication when the web application is enabled
- Checkout mode for pull-request evaluation
- Audit-event retention age

Use more than one coordinator replica only after you verify that all selected
dependencies and session behavior support it.

## Coordinator Settings

The chart has values for the main coordinator settings (`oidc`,
`rateLimits`, `outboundEvents`, `observability`, `artifacts`, `provenance`,
`cache`, `groupVcsSync`). An empty value does not set the variable, so the
coordinator uses its code default. Use `app.extraEnv` and `worker.extraEnv`
for other variables. Each item is a Kubernetes `EnvVar` with `value` or
`valueFrom`:

```yaml
oidc:
  issuerUrl: "https://ci.example.com/api/v1/oidc"
rateLimits:
  webhookPerMinute: 1200
app:
  extraEnv:
    - name: REACTORCIDE_ADMISSION_RESERVE_AFTER
      value: "10m"
worker:
  extraEnv:
    - name: REACTORCIDE_WORKER_ZONE
      value: us-east-1a
```

Set `metrics.prometheusRule.enabled: true` to make a PrometheusRule with the
CI owner alerts (needs the Prometheus Operator CRD). The table of values and
defaults is in [Upgrade to 0.13](../docs/upgrade-0.13.md#5-helm-chart-values).

## Upgrade

Read [Upgrade to 0.13](../docs/upgrade-0.13.md) before you upgrade from
0.12.x.

Render and review changes:

```bash
helm template reactorcide ./helm_chart \
  --namespace reactorcide \
  --values values-production.yaml \
  > /tmp/reactorcide-rendered.yaml
```

Upgrade:

```bash
helm upgrade reactorcide ./helm_chart \
  --namespace reactorcide \
  --values values-production.yaml \
  --wait \
  --timeout 10m
```

Check rollouts:

```bash
kubectl rollout status -n reactorcide deployment/reactorcideapp
kubectl rollout status -n reactorcide deployment/reactorcide-worker
```

## Troubleshooting

### Coordinator does not start

Check the PostgreSQL URI and database network access:

```bash
kubectl logs -n reactorcide deployment/reactorcideapp
```

### Worker does not enroll

Check both deployments reference the same enrollment Secret:

```bash
kubectl get secret -n reactorcide reactorcide-worker-enrollment
kubectl logs -n reactorcide deployment/reactorcide-worker
```

Do not print the Secret value.

### Jobs do not start

Confirm:

- Corndogs is reachable at the configured TCP address.
- The worker is online.
- Job characteristics match the worker.
- `defaults.runnerImage` or the job image is set.
- `defaults.checkoutMode` is `isolated` or `shared`.
- `isolated` is the compatibility default. `shared` uses one non-shallow,
  blob-filtered object store for pull-request evaluation.
- A project setting overrides this chart value. An empty project setting
  inherits it. There is no organization-level checkout default.
- The worker uses `containerRuntime: kubernetes`.

### Eval runs but child jobs do not start

Confirm that `app.apiTokenSecret` and `worker.apiTokenSecret` reference a valid
API token. Restart both deployments after you create or rotate the Secret.

### Logs disappear

The filesystem object store uses `emptyDir`. Select S3 storage for durable
logs.

## Uninstall

```bash
helm uninstall reactorcide --namespace reactorcide
```

Helm keeps the API-token and worker-enrollment Secrets by policy. Persistent
volume claims can also remain. Inspect and remove retained resources only when
you no longer need their data.
