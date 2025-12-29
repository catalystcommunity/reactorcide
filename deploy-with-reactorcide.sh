#!/bin/bash
set -euo pipefail

# Deploy Reactorcide to VM using runnerlib LOCAL execution
# This demonstrates the "emergency escape hatch" - deploying when infrastructure is down

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1" >&2; exit 1; }
warn() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
info() { echo -e "${BLUE}[INFO]${NC} $1"; }

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --host) REACTORCIDE_DEPLOY_HOST="$2"; shift 2 ;;
        --user) REACTORCIDE_DEPLOY_USER="$2"; shift 2 ;;
        --domains) REACTORCIDE_DEPLOY_DOMAINS="$2"; shift 2 ;;
        --dry-run) DRY_RUN="--dry-run"; shift ;;
        --help)
            cat << 'EOF'
Reactorcide Deployment via Runnerlib

Deploy Reactorcide to a VM using runnerlib's LOCAL execution mode.
This demonstrates running jobs locally without any CI/CD infrastructure.

Usage: ./deploy-with-reactorcide.sh [OPTIONS]

Options:
  --host HOST        SSH host to deploy to
  --user USER        Remote user for deployment
  --domains DOMAINS  Comma-separated list of domains
  --dry-run          Validate without running deployment
  --help             Show this help

Environment Variables:
  REACTORCIDE_DEPLOY_HOST     SSH host to deploy to
  REACTORCIDE_DEPLOY_USER     Remote user for deployment
  REACTORCIDE_DEPLOY_DOMAINS  Comma-separated list of domains
  REACTORCIDE_DB_PASSWORD     Database password (auto-generated if not set)
  REACTORCIDE_JWT_SECRET      JWT secret (auto-generated if not set)
  REACTORCIDE_RUNNER_IMAGE    Runner image (default: containers.catalystsquad.com/public/reactorcide/runnerbase:dev)

Example:
  source ~/my-reactorcide-vm.config
  ./deploy-with-reactorcide.sh

  # Or with command line:
  ./deploy-with-reactorcide.sh --host vm.example.com --user myuser --domains reactorcide.local
EOF
            exit 0
            ;;
        *) error "Unknown option: $1" ;;
    esac
done

# Validate required variables
REACTORCIDE_DEPLOY_HOST="${REACTORCIDE_DEPLOY_HOST:-}"
REACTORCIDE_DEPLOY_USER="${REACTORCIDE_DEPLOY_USER:-}"
REACTORCIDE_DEPLOY_DOMAINS="${REACTORCIDE_DEPLOY_DOMAINS:-}"

missing_vars=()
[ -z "${REACTORCIDE_DEPLOY_HOST}" ] && missing_vars+=("REACTORCIDE_DEPLOY_HOST")
[ -z "${REACTORCIDE_DEPLOY_USER}" ] && missing_vars+=("REACTORCIDE_DEPLOY_USER")
[ -z "${REACTORCIDE_DEPLOY_DOMAINS}" ] && missing_vars+=("REACTORCIDE_DEPLOY_DOMAINS")

if [ ${#missing_vars[@]} -gt 0 ]; then
    error "Missing required variables: ${missing_vars[*]}"
fi

# Export environment variables for the deployment script
export REACTORCIDE_DEPLOY_HOST
export REACTORCIDE_DEPLOY_USER
export REACTORCIDE_DEPLOY_DOMAINS
export REACTORCIDE_DB_PASSWORD="${REACTORCIDE_DB_PASSWORD:-}"
export REACTORCIDE_JWT_SECRET="${REACTORCIDE_JWT_SECRET:-}"
export REACTORCIDE_WORKER_CONCURRENCY="${REACTORCIDE_WORKER_CONCURRENCY:-2}"
export REACTORCIDE_WORKER_POLL_INTERVAL="${REACTORCIDE_WORKER_POLL_INTERVAL:-5}"
export REACTORCIDE_LOG_LEVEL="${REACTORCIDE_LOG_LEVEL:-info}"
export REACTORCIDE_RUNNER_IMAGE="${REACTORCIDE_RUNNER_IMAGE:-containers.catalystsquad.com/public/reactorcide/runnerbase:dev}"

log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log "Reactorcide Self-Deployment via Runnerlib LOCAL Mode"
log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
info "Target: ${REACTORCIDE_DEPLOY_USER}@${REACTORCIDE_DEPLOY_HOST}"
info "Domains: ${REACTORCIDE_DEPLOY_DOMAINS}"
info "Runner Image: ${REACTORCIDE_RUNNER_IMAGE}"
info "Mode: LOCAL execution (no containers, no infrastructure required)"
log ""

# Verify we're in the right directory
[ ! -d "runnerlib" ] && error "runnerlib directory not found. Run from repo root."
[ ! -f "deployment/deploy-vm.sh" ] && error "deployment/deploy-vm.sh not found."

# Run deployment via runnerlib LOCAL mode
log "Executing deployment via runnerlib..."
cd runnerlib && uv run python -m src.cli run \
    --job-command "bash ../deployment/deploy-vm.sh" \
    ${DRY_RUN:-}

EXIT_CODE=$?

if [ $EXIT_CODE -eq 0 ]; then
    log ""
    log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log "✓ Deployment completed successfully!"
    log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log ""
    info "Next steps:"
    info "  1. Check services: ssh ${REACTORCIDE_DEPLOY_HOST} 'cd ~/reactorcide && docker compose -f docker-compose.prod.yml ps'"
    info "  2. Test API health: curl http://${REACTORCIDE_DEPLOY_HOST}:8080/api/v1/health"
    log ""
else
    error "Deployment failed with exit code: $EXIT_CODE"
fi