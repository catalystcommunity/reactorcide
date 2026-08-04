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
- Generated default-user and worker-enrollment Secrets
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

The chart creates `base-reactorcide-user` with a generated user ID. It does not
create an API token during a direct Helm installation.

Create a protected local token file:

```bash
mkdir -p ~/.config/reactorcide
chmod 700 ~/.config/reactorcide

REACTORCIDE_USER_ID="$(
  kubectl get secret base-reactorcide-user \
    -n reactorcide \
    -o jsonpath='{.data.user-id}' | base64 -d
)"

kubectl exec -n reactorcide deployment/reactorcideapp -- \
  /reactorcide token create \
  --name cluster-bootstrap \
  --user-id "${REACTORCIDE_USER_ID}" \
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

web:
  apiTokenSecret:
    name: reactorcide-api-token
    key: token
```

Run `helm upgrade` again. This token lets eval jobs submit workflow children.
It also lets the web application authenticate when you enable it.

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
can read Pods, Pod logs, and Pod resource metrics in its namespace. A cluster
role permits reads from `nodes/proxy` for volume statistics. Review
`templates/rbac-worker.yaml` before you change its namespace or service
account.

See [Worker Operation](../docs/workers.md).

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

The coordinator route handles `/api/*`. The web route handles other paths.
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

Use more than one coordinator replica only after you verify that all selected
dependencies and session behavior support it.

## Upgrade

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

Helm keeps the default-user and worker-enrollment Secrets by policy. Persistent
volume claims can also remain. Inspect and remove retained resources only when
you no longer need their data.
