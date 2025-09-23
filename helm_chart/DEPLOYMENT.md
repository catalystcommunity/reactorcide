# Reactorcide Kubernetes Deployment Guide

This guide covers deploying Reactorcide to Kubernetes using Helm, including integration with Corndogs for job queueing.

## Prerequisites

- Kubernetes cluster (v1.25+)
- Helm 3.x installed
- kubectl configured to access your cluster
- Docker registry access for container images

## Architecture Overview

The complete Reactorcide deployment consists of:

- **Coordinator API**: REST API for job submission and management
- **Workers**: Process jobs from the queue using Docker-in-Docker
- **PostgreSQL**: Database for job metadata and state
- **Corndogs**: Distributed task queue system (deployed separately)
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

Reactorcide requires Corndogs for distributed job queueing. You have two options:

### Option 1: Deploy Corndogs in Same Namespace

```yaml
# corndogs-values.yaml
service:
  type: ClusterIP
  port: 8080

queues:
  - name: reactorcide-jobs
    maxRetries: 3
    timeout: 3600
```

Deploy Corndogs:
```bash
helm install corndogs <corndogs-chart> \
  --namespace reactorcide \
  -f corndogs-values.yaml
```

Then update Reactorcide values:
```yaml
corndogs:
  enabled: true
  baseUrl: "corndogs:8080"
```

### Option 2: Use External Corndogs

If Corndogs is deployed in another namespace or cluster:

```yaml
corndogs:
  enabled: true
  baseUrl: "http://corndogs.other-namespace.svc.cluster.local:8080"
  apiKey: "your-api-key"  # If authentication is required
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
corndogs:
  apiKey: ""  # Set via --set-string or external secret

objectStore:
  s3:
    accessKeyId: ""  # Set via --set-string
    secretAccessKey: ""  # Set via --set-string
```

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
  baseUrl: "corndogs.prod.svc.cluster.local:8080"
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
   - Check Corndogs connectivity: `kubectl logs -n reactorcide deployment/reactorcide-worker | grep Corndogs`
   - Verify queue exists in Corndogs
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