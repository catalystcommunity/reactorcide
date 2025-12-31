#!/bin/bash
set -euo pipefail

# Reactorcide VM Deployment Script
# This script runs LOCALLY via runnerlib to deploy Reactorcide to a remote VM
# Usage: uv run python -m src.cli run --job-command "bash deployment/deploy-vm.sh"

# Color codes for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
    exit 1
}

warn() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

# Validate required environment variables
missing_vars=()
[ -z "${REACTORCIDE_DEPLOY_HOST:-}" ] && missing_vars+=("REACTORCIDE_DEPLOY_HOST")
[ -z "${REACTORCIDE_DEPLOY_USER:-}" ] && missing_vars+=("REACTORCIDE_DEPLOY_USER")
[ -z "${REACTORCIDE_DEPLOY_DOMAINS:-}" ] && missing_vars+=("REACTORCIDE_DEPLOY_DOMAINS")

if [ ${#missing_vars[@]} -gt 0 ]; then
    error "Missing required environment variables: ${missing_vars[*]}"
fi

log "Reactorcide VM Deployment"
log "Target: ${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}"
log "Domains: ${REACTORCIDE_DEPLOY_DOMAINS}"

# Generate secrets if not provided (use hex to avoid URL-unsafe characters)
if [ -z "${REACTORCIDE_DB_PASSWORD:-}" ]; then
    REACTORCIDE_DB_PASSWORD=$(openssl rand -hex 24)
    info "Generated database password"
fi

if [ -z "${REACTORCIDE_JWT_SECRET:-}" ]; then
    REACTORCIDE_JWT_SECRET=$(openssl rand -hex 32)
    info "Generated JWT secret"
fi

# Deployment configuration
REMOTE_DIR="${REACTORCIDE_REMOTE_DIR:-~/reactorcide}"
LOCAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log "Step 1: Testing SSH connection"
if ssh -o ConnectTimeout=10 "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" "echo 'SSH OK'"; then
    info "SSH connection verified"
else
    error "Failed to connect to ${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}"
fi

log "Step 2: Creating remote directory structure"
ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" "mkdir -p ${REMOTE_DIR}"

log "Step 3: Copying deployment files"
rsync -avz --progress \
    "${LOCAL_ROOT}/deployment/" \
    "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}:${REMOTE_DIR}/"

log "Step 4: Creating environment configuration on VM"
ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" bash <<EOF
set -euo pipefail
cd ${REMOTE_DIR}

# Create .env file
cat > .env <<'ENVFILE'
# Database Configuration (for postgres containers)
POSTGRES_USER=reactorcide
POSTGRES_PASSWORD=${REACTORCIDE_DB_PASSWORD}
POSTGRES_DB=reactorcide_db
CORNDOGS_DB_NAME=corndogs_db

# Reactorcide Configuration (all prefixed for clean separation)
REACTORCIDE_DB_URI=postgresql://reactorcide:${REACTORCIDE_DB_PASSWORD}@postgres:5432/reactorcide_db?sslmode=disable
REACTORCIDE_JWT_SECRET=${REACTORCIDE_JWT_SECRET}
REACTORCIDE_OBJECT_STORE_TYPE=filesystem
REACTORCIDE_OBJECT_STORE_BASE_PATH=/data/reactorcide
REACTORCIDE_WORKER_CONCURRENCY=${REACTORCIDE_WORKER_CONCURRENCY:-2}
REACTORCIDE_WORKER_POLL_INTERVAL=${REACTORCIDE_WORKER_POLL_INTERVAL:-5}
REACTORCIDE_CONTAINER_RUNTIME=docker
REACTORCIDE_LOG_LEVEL=${REACTORCIDE_LOG_LEVEL:-info}
ENVFILE

echo "Environment configuration created"
EOF

log "Step 5: Pulling Docker images on VM"
ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" bash <<EOF
set -euo pipefail
cd ${REMOTE_DIR}

# Pull all Reactorcide images from registry
echo "Pulling coordinator image..."
docker compose -f docker-compose.prod.yml pull coordinator-api

echo "Pulling worker image..."
docker compose -f docker-compose.prod.yml pull worker

# Pull runner image (used by worker to spawn job containers)
RUNNER_IMAGE="${REACTORCIDE_RUNNER_IMAGE:-containers.catalystsquad.com/public/reactorcide/runnerbase:dev}"
echo "Pulling runner image: \${RUNNER_IMAGE}..."
docker pull "\${RUNNER_IMAGE}"

# Tag as local name for worker to use
docker tag "\${RUNNER_IMAGE}" reactorcide/runner:latest

echo "Images ready"
EOF

log "Step 6: Starting services"
ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" bash <<EOF
set -euo pipefail
cd ${REMOTE_DIR}

# Stop existing services
echo "Stopping existing services..."
docker compose -f docker-compose.prod.yml down || true

# Start services
echo "Starting services..."
docker compose -f docker-compose.prod.yml up -d

# Wait for startup
sleep 10

# Show status
docker compose -f docker-compose.prod.yml ps
EOF

log "Step 7: Waiting for coordinator to be healthy (migrations run on startup)"
for i in $(seq 1 30); do
    if ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" "curl -sf http://localhost:6080/api/v1/health" > /dev/null 2>&1; then
        info "Coordinator is healthy"
        break
    fi
    echo "Waiting... ($i/30)"
    sleep 2
done

log "Step 8: Verifying deployment"
ssh "${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}" bash <<EOF
set -euo pipefail
cd ${REMOTE_DIR}

# Check health
if curl -sf http://localhost:6080/api/v1/health > /dev/null; then
    echo "✓ Coordinator API is healthy"
else
    echo "✗ API health check failed"
    exit 1
fi
EOF

log ""
log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log "✓ Deployment completed successfully!"
log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log ""
log "Next steps:"
log "  1. Access the API: http://${REACTORCIDE_DEPLOY_HOST}:6080/api/v1/health"
log "  2. Create an API token (SSH to VM and run create-api-token.sh)"
log ""
