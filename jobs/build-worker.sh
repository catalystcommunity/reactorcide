#!/bin/bash
set -euo pipefail

# Build and push worker image to registry
# Usage: ./build-worker.sh [tag]

# Registry configuration - override with environment variables
REGISTRY="${REACTORCIDE_REGISTRY:-containers.catalystsquad.com}"
IMAGE="${REACTORCIDE_IMAGE:-public/reactorcide/worker}"
TAG="${1:-dev}"
USER="${REACTORCIDE_REGISTRY_USER:-todpunk}"

# Get the script directory and project root
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR/.."
RUNNERLIB_DIR="$PROJECT_ROOT/runnerlib"

echo "Building worker image: $REGISTRY/$IMAGE:$TAG"

# Get master password once if not already set
if [ -z "${REACTORCIDE_SECRETS_PASSWORD:-}" ]; then
    echo -n "Secrets password: "
    read -s REACTORCIDE_SECRETS_PASSWORD
    echo
    export REACTORCIDE_SECRETS_PASSWORD
fi

# Retrieve secrets using runnerlib
cd "$RUNNERLIB_DIR"
PASSWORD=$(uv run runnerlib secrets get reactorcide/local-dev registry_password)

if [ -z "$PASSWORD" ]; then
    echo "Error: Registry password not found in secrets"
    echo "Run: cd $RUNNERLIB_DIR && uv run runnerlib secrets set reactorcide/local-dev registry_password"
    exit 1
fi

# Build the Go binary
echo "Building reactorcide binary..."
cd "$PROJECT_ROOT/coordinator_api"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o reactorcide .

# Copy binary to deployment directory for Docker build context
cp reactorcide "$PROJECT_ROOT/deployment/"

# Build Docker image
echo "Building Docker image..."
cd "$PROJECT_ROOT/deployment"
docker build -t "$REGISTRY/$IMAGE:$TAG" -f Dockerfile.worker .

# Transfer image from docker to nerdctl (containerd)
echo "Transferring image to containerd..."
docker save "$REGISTRY/$IMAGE:$TAG" | nerdctl load

# Login and push with nerdctl (handles auth correctly)
echo "Logging into registry..."
echo "$PASSWORD" | nerdctl login "$REGISTRY" -u "$USER" --password-stdin

echo "Pushing image..."
nerdctl push "$REGISTRY/$IMAGE:$TAG"

# Cleanup
rm -f "$PROJECT_ROOT/deployment/reactorcide"

echo "Done! Image pushed to $REGISTRY/$IMAGE:$TAG"
