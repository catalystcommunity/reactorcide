# Reactorcide Kubernetes Deployment Guide

This guide covers deploying Reactorcide to Kubernetes using Helm, including integration with Corndogs for job queueing.

## Prerequisites

- Kubernetes cluster (v1.25+)
- Helm 3.x installed
- kubectl configured to access your cluster
- Container registry access for container images

## Architecture Overview

The complete Reactorcide deployment consists of:

- **Coordinator API**: REST API for job submission and management; the only
  component that talks to corndogs, PostgreSQL, and object storage
- **Workers**: Authenticate to the coordinator over CSIL-RPC and pull work
  through it, then execute jobs using Docker-in-Docker (or containerd/
  Kubernetes). Coordinator-mediated — a worker has no direct corndogs/DB/
  object-store dependency of its own; see "Deploying Workers" below and
  [`docs/workers.md`](../docs/workers.md)
- **PostgreSQL**: Database for job metadata and state
- **Corndogs**: Distributed task queue system (bundled subchart or external service)
- **Object Storage**: MinIO, S3, or GCS for artifacts and logs

## Quick Start

### 1. Add Helm Repository

```bash
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update
```

### 2. Deploy Reactorcide

```bash
# Install with default values
helm install reactorcide ./helm_chart \
  --namespace reactorcide \
  --create-namespace

# Or with custom values
helm install reactorcide ./helm_chart \
  --namespace reactorcide \
  --create-namespace \
  -f custom-values.yaml
```

### 3. Verify Deployment

```bash
kubectl get pods -n reactorcide
kubectl get svc -n reactorcide
```

## Corndogs Integration

Reactorcide requires Corndogs for distributed job queueing. The bundled chart
uses Corndogs' CSIL-RPC HTTP transport on port `5080` and defaults to the
single-replica file backend, so no separate Corndogs Postgres database is
deployed.

### Option 1: Deploy Corndogs in Same Namespace

```yaml
corndogs:
  enabled: true
  replicaCount: 1
  storage:
    backend: file
    file:
      persistence:
        enabled: true
        size: 10Gi
```

### Option 2: Use External Corndogs

If Corndogs is deployed in another namespace or cluster:

```yaml
corndogs:
  enabled: false
  baseUrl: "http://corndogs.other-namespace.svc.cluster.local:5080"
```

## Deploying Workers

Workers are **coordinator-mediated**: they authenticate to the coordinator
over CSIL-RPC and pull work through it instead of talking to corndogs,
PostgreSQL, or object storage directly. A worker deployment needs only:

- a **coordinator URL** (defaults to this release's in-cluster coordinator
  Service — you normally don't need to set this),
- an **enrollment token** (auto-provisioned by default; see below),
- a writable **data directory** for the worker's persisted identity
  (`worker_key` — not a secret; `emptyDir` by default).

**Zero-touch enrollment (default):** a fresh `helm install` needs **no manual
Secret, pool, or kubectl step**. `templates/secret-worker-enrollment.yaml`
generates a stable enrollment token once into a managed Secret
(`<release>-worker-enrollment`, key `token`), annotated
`helm.sh/resource-policy: keep`. Both the **coordinator** deployment
(`REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN`, which seeds the default worker
pool) and the **worker** deployment (`REACTORCIDE_WORKER_ENROLLMENT_TOKEN`)
reference that same Secret via `secretKeyRef`, so the worker enrolls against a
pool the coordinator already knows about. A `lookup` in the template reuses the
already-applied value on `helm upgrade`, keeping the token stable across
redeploys — the token value never appears in `values.yaml` or any manifest.

> `helm template` / `--dry-run` cannot read a live cluster, so they render a
> fresh random value each run; the value that actually persists is the one from
> a real `helm install`/`upgrade`.

**Operator override:** to enroll with your own admin-minted per-pool token,
create the Secret yourself and set `worker.enrollmentTokenSecret.name`. The
chart then does **not** create the managed Secret and both deployments read
yours instead:

1. Create a worker pool + enrollment token via the coordinator admin UI/CLI.
2. Store the token in a Kubernetes Secret:

   ```bash
   kubectl create secret generic reactorcide-worker-enrollment \
     --from-literal=token="$(cat /path/to/enrollment-token)" \
     -n reactorcide
   ```

3. Reference that Secret in your values (never the token value itself):

   ```yaml
   worker:
     enrollmentTokenSecret:
       name: "reactorcide-worker-enrollment"   # set = override; empty = auto-generate
       key: "token"       # default
   ```

The chart wires this via `secretKeyRef` in `templates/deployment-worker.yaml`
and `templates/deployment-app.yaml` the same way `secrets.existingSecret` /
`uiAuth.existingSecret` reference their own Secrets — no plaintext secret value
ever belongs in `values.yaml` or any committed manifest.

**Rotation:** delete the managed Secret and re-run `helm upgrade` to regenerate
and re-seed, or switch to an operator-override Secret and rotate it via the
admin UI (a pool can hold several active tokens for a flag-day-free rollover).

Optional worker settings (all under `worker:` in `values.yaml`):

```yaml
worker:
  concurrency: 2              # concurrent leases (jobs) per worker pod
  containerRuntime: "auto"    # docker | containerd | kubernetes | auto
  os: ""                      # characteristic override; auto-detected otherwise
  arch: ""                    # characteristic override; auto-detected otherwise
  custom: []                  # free-form "key=value" characteristics
  dataDirPersistence:
    enabled: false             # true to keep worker_key stable across restarts
    size: 1Gi                  # only meaningful with replicaCount: 1
```

## Configuration

### Core Settings

```yaml
app:
  enabled: true
  replicaCount: 2
  resources:
    limits:
      cpu: 1000m
      memory: 1Gi
    requests:
      cpu: 100m
      memory: 128Mi

worker:
  enabled: true
  replicaCount: 3
  concurrency: 2  # Jobs per worker
  resources:
    limits:
      cpu: 2000m
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 512Mi
```

### Database Configuration

#### Using Built-in PostgreSQL

```yaml
postgresql:
  enabled: true
  auth:
    username: "reactorcide"
    password: "changeme"
    database: "reactorcide"
  persistence:
    enabled: true
    size: 10Gi
```

#### Using External Database

```yaml
postgresql:
  enabled: false

postgres:
  uri: "postgresql://user:pass@external-db:5432/reactorcide?sslmode=require"
```

### Object Storage

#### Filesystem (Development)

```yaml
objectStore:
  type: filesystem
  basePath: /tmp/reactorcide-objects
```

#### S3/MinIO

```yaml
objectStore:
  type: s3
  bucket: reactorcide-objects
  s3:
    accessKeyId: "your-access-key"
    secretAccessKey: "your-secret-key"
    region: us-east-1
    endpoint: "http://minio:9000"  # For MinIO
```

#### Google Cloud Storage

```yaml
objectStore:
  type: gcs
  bucket: reactorcide-objects
  gcs:
    serviceAccountJson: |
      {
        "type": "service_account",
        ...
      }
```

## Advanced Configuration

### Autoscaling

```yaml
app:
  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 10
    targetCPUUtilizationPercentage: 70

worker:
  autoscaling:
    enabled: true
    minReplicas: 1
    maxReplicas: 20
    targetCPUUtilizationPercentage: 60
    targetMemoryUtilizationPercentage: 80
```

### Prometheus Metrics

```yaml
app:
  prometheus:
    enabled: true
    path: /api/v1/metrics
    port: 9000
```

### Security

```yaml
# Use secrets for sensitive data
objectStore:
  s3:
    accessKeyId: ""  # Set via --set-string
    secretAccessKey: ""  # Set via --set-string
```

### Management UI Auth (RBAC, login, visibility)

Disabled by default (`uiAuth.mode: none` — no login, public-view only). See
[docs/ui-auth.md](../docs/ui-auth.md) for the full operator guide (permission matrix,
first-admin/bootstrap flow, credential rotation). To enable LinkKeys login:

```yaml
uiAuth:
  mode: local-rp                       # or "rp" for a full LinkKeys RP deployment
  localRpName: "my-reactorcide"        # local-rp only
  # linkkeysRpAddr / linkkeysRpFingerprints instead, for mode: rp
  firstAdmin: "you@your-domain.example"
  trustedIdentities: "your-domain.example"
  callbackURL: "https://ci.example.com"  # the web UI's public base URL, not the coordinator's

web:
  enabled: true
  gateway:
    enabled: true
    domains: ["ci.example.com"]
```

The two secret-bearing values (`linkkeysRpApiKey` for `mode: rp`, and
`bootstrapAdminToken`) should reference an existing Kubernetes secret in production rather
than sitting in `values.yaml` in plaintext, the same way `secrets.existingSecret` works for
master keys:

```yaml
uiAuth:
  existingSecret: "reactorcide-ui-auth"
  existingSecretRpApiKeyKey: "rp-api-key"            # default
  existingSecretBootstrapTokenKey: "bootstrap-admin-token"  # default
```

```bash
kubectl create secret generic reactorcide-ui-auth \
  --from-literal=rp-api-key="$(cat /path/to/rp-api-key)" \
  --from-literal=bootstrap-admin-token="$(openssl rand -hex 32)"
```

Consider removing `bootstrapAdminToken`/the `existingSecret` reference to it once your
first real admin (via `firstAdmin` + a normal login) is established — the bootstrap flow is
meant for initial setup, not ongoing use.

If the webapp is only ever reached over plain HTTP (no TLS terminator in front of it — not
recommended for a real deployment), set `web.webCookieInsecure: true` so the session cookie
doesn't require `Secure`; otherwise leave it `false`.

## Production Deployment

### 1. Create Production Values

```yaml
# values-production.yaml
app:
  replicaCount: 3
  resources:
    limits:
      cpu: 2000m
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 512Mi

worker:
  replicaCount: 10
  concurrency: 4
  terminationGracePeriodSeconds: 3600
  # Optional operator override; omit for zero-touch auto-provisioning.
  enrollmentTokenSecret:
    name: "reactorcide-worker-enrollment"  # created out-of-band, see "Deploying Workers"
    key: "token"
  resources:
    limits:
      cpu: 4000m
      memory: 4Gi
    requests:
      cpu: 1000m
      memory: 1Gi

postgresql:
  enabled: false  # Use managed database

postgres:
  uri: "${POSTGRES_URI}"  # Set via secret

objectStore:
  type: s3
  bucket: prod-reactorcide-objects

corndogs:
  enabled: true
  replicaCount: 1
  storage:
    backend: file
    file:
      persistence:
        enabled: true
        size: 10Gi
```

### 2. Deploy with Secrets

```bash
# Create namespace
kubectl create namespace reactorcide-prod

# Create secrets
kubectl create secret generic reactorcide-secrets \
  --from-literal=postgres-uri="postgresql://..." \
  --from-literal=aws-access-key-id="..." \
  --from-literal=aws-secret-access-key="..." \
  -n reactorcide-prod

# Deploy
helm install reactorcide ./helm_chart \
  --namespace reactorcide-prod \
  -f values-production.yaml \
  --set-string postgres.uri="${POSTGRES_URI}" \
  --set-string objectStore.s3.accessKeyId="${AWS_ACCESS_KEY_ID}" \
  --set-string objectStore.s3.secretAccessKey="${AWS_SECRET_ACCESS_KEY}"
```

## Monitoring and Operations

### Health Checks

```bash
# Check API health
kubectl exec -n reactorcide deployment/reactorcideapp -- curl localhost:6080/api/health

# Check worker status
kubectl logs -n reactorcide deployment/reactorcide-worker

# Check job processing
kubectl exec -n reactorcide deployment/reactorcideapp -- curl localhost:6080/api/v1/jobs
```

### Metrics

If Prometheus is enabled:
```bash
kubectl port-forward -n reactorcide svc/reactorcideapp 9000:9000
curl localhost:9000/api/v1/metrics
```

### Troubleshooting

1. **Workers not processing jobs**
   - Check the worker can reach and register with the coordinator:
     `kubectl logs -n reactorcide deployment/reactorcide-worker | grep -i register`
   - With zero-touch defaults, confirm the managed
     `<release>-worker-enrollment` Secret exists and that the coordinator
     seeded its default pool (coordinator logs mention the default worker
     pool); with an operator override, verify
     `worker.enrollmentTokenSecret.name` points at a Secret that actually
     exists and holds a valid, active enrollment token (see
     [`docs/workers.md`](../docs/workers.md))
   - Check worker resource limits

2. **Database connection issues**
   - Verify PostgreSQL is running: `kubectl get pods -n reactorcide | grep postgresql`
   - Check connection string in secrets
   - Review migration job logs: `kubectl logs -n reactorcide job/migrations`

3. **Docker-in-Docker issues**
   - Ensure workers have Docker socket mount
   - Check worker pod security context
   - Verify Docker daemon is running on nodes

## Upgrading

```bash
# Update chart dependencies
helm dependency update ./helm_chart

# Upgrade deployment
helm upgrade reactorcide ./helm_chart \
  --namespace reactorcide \
  -f custom-values.yaml

# Check rollout status
kubectl rollout status -n reactorcide deployment/reactorcideapp
kubectl rollout status -n reactorcide deployment/reactorcide-worker
```

## Uninstalling

```bash
helm uninstall reactorcide --namespace reactorcide
kubectl delete namespace reactorcide
```

## Development with Skaffold

For local development with hot-reloading:

```bash
# Install Skaffold
curl -Lo skaffold https://storage.googleapis.com/skaffold/releases/latest/skaffold-linux-amd64
sudo install skaffold /usr/local/bin/

# Run development environment
skaffold dev

# Or without app (use local app)
skaffold dev --profile no-app
```

## Security Considerations

1. **Network Policies**: Implement network policies to restrict pod-to-pod communication
2. **RBAC**: Use appropriate service accounts with minimal permissions
3. **Secrets Management**: Use external secret managers (Vault, Sealed Secrets) in production
4. **Image Scanning**: Scan container images for vulnerabilities
5. **Pod Security Standards**: Apply appropriate pod security policies

## Support and Documentation

- Main Documentation: [README.md](../README.md)
- Runnerlib Documentation: [runnerlib/docs/](../runnerlib/docs/)
- Corndogs Integration: [corndogs-integration.md](../corndogs-integration.md)
- API Documentation: [coordinator_api/README.md](../coordinator_api/README.md)
