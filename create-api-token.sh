#!/bin/bash
set -euo pipefail

# Script to create initial API token for Reactorcide
# Run this on the VM or via SSH

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
    exit 1
}

# Configuration - use environment variable or command line argument
REMOTE_HOST="${1:-${REACTORCIDE_DEPLOY_HOST:-}}"
TOKEN_NAME="${2:-admin-token}"
TOKEN_DESCRIPTION="${3:-Initial admin token for testing}"

# Check required environment variables
missing_vars=()
[ -z "${REACTORCIDE_DEPLOY_HOST:-}" ] && [ -z "$1" ] && missing_vars+=("REACTORCIDE_DEPLOY_HOST")

if [ ${#missing_vars[@]} -gt 0 ]; then
    error "Missing required environment variables: ${missing_vars[*]}"
    exit 1
fi

log "Creating API token on ${REMOTE_HOST}"

# Generate a secure token (64 hex chars = 32 bytes)
TOKEN=$(openssl rand -hex 32)
USER_ID="550e8400-e29b-41d4-a716-446655440000"  # Default user ID from deployment

log "Generated token: ${TOKEN}"

# Hash the token using SHA256 (PostgreSQL expects bytea format)
# Note: The coordinator API needs to hash tokens the same way when validating
log "Creating token in database..."

# Use coordinator API's token creation if available, otherwise direct SQL
CONTAINER_ID=$(ssh ${REMOTE_HOST} "docker ps -q -f name=coordinator-api" 2>/dev/null)

if [ -n "$CONTAINER_ID" ]; then
    log "Using coordinator API to create token..."
    # Try to use the coordinator API's token creation command if it exists
    CREATED_TOKEN=$(ssh ${REMOTE_HOST} "cd ~/reactorcide && docker compose -f docker-compose.prod.yml exec -T coordinator-api /reactorcide token create --name '${TOKEN_NAME}' 2>/dev/null" || echo "")

    if [ -n "$CREATED_TOKEN" ]; then
        TOKEN="$CREATED_TOKEN"
        log "Token created via coordinator API"
    else
        log "Coordinator API token creation not available, using direct database insert..."
        # Hash the token for storage (using sha256 and converting to PostgreSQL bytea format)
        TOKEN_HASH=$(echo -n "${TOKEN}" | sha256sum | awk '{print $1}')
        SQL_COMMAND="INSERT INTO api_tokens (user_id, token_hash, name, is_active) VALUES ('${USER_ID}', decode('${TOKEN_HASH}', 'hex'), '${TOKEN_NAME}', true) RETURNING token_id, name;"
        ssh ${REMOTE_HOST} "cd ~/reactorcide && docker compose -f docker-compose.prod.yml exec -T postgres psql -U reactorcide -d reactorcide_db -c \"${SQL_COMMAND}\""
    fi
else
    error "Coordinator API container not found"
fi

if [ $? -eq 0 ]; then
    log "API token created successfully!"
    echo ""
    echo "========================================="
    echo "API Token Details:"
    echo "========================================="
    echo "Token Name: ${TOKEN_NAME}"
    echo "Token Value: ${TOKEN}"
    echo ""
    echo "Save this token securely - it cannot be retrieved again!"
    echo ""
    echo "Test the token with:"
    echo "  export REACTORCIDE_API_TOKEN='${TOKEN}'"
    echo "  ssh ${REMOTE_HOST} 'curl -H \"Authorization: Bearer '${TOKEN}'\" http://localhost:8080/api/v1/jobs'"
    echo ""
    echo "Submit a test job:"
    echo "  ssh ${REMOTE_HOST} 'curl -X POST http://localhost:8080/api/v1/jobs \\"
    echo "    -H \"Authorization: Bearer '${TOKEN}'\" \\"
    echo "    -H \"Content-Type: application/json\" \\"
    echo "    -d \"{\\\"name\\\":\\\"test-job\\\",\\\"image\\\":\\\"alpine:latest\\\",\\\"command\\\":\\\"echo Hello from Reactorcide\\\"}\"'"
    echo "========================================="
else
    error "Failed to create API token"
fi