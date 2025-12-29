#!/bin/bash
set -euo pipefail

# Build and push all Reactorcide images to registry
# Usage: ./build-all.sh [tag]
#
# This script builds all three images with a single password prompt:
# - runnerbase: The base runner image for job execution
# - coordinator: The coordinator API service
# - worker: The worker service that processes jobs

TAG="${1:-dev}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "========================================"
echo "Building all Reactorcide images (tag: $TAG)"
echo "========================================"
echo ""

# Get master password once for all builds
if [ -z "${REACTORCIDE_SECRETS_PASSWORD:-}" ]; then
    echo -n "Secrets password: "
    read -s REACTORCIDE_SECRETS_PASSWORD
    echo
    export REACTORCIDE_SECRETS_PASSWORD
fi

echo ""
echo ">>> Building runnerbase..."
echo "----------------------------------------"
"$SCRIPT_DIR/build-runner-base.sh" "$TAG"

echo ""
echo ">>> Building coordinator..."
echo "----------------------------------------"
"$SCRIPT_DIR/build-coordinator.sh" "$TAG"

echo ""
echo ">>> Building worker..."
echo "----------------------------------------"
"$SCRIPT_DIR/build-worker.sh" "$TAG"

echo ""
echo "========================================"
echo "All images built and pushed successfully!"
echo "========================================"
echo ""
echo "Images:"
echo "  - containers.catalystsquad.com/public/reactorcide/runnerbase:$TAG"
echo "  - containers.catalystsquad.com/public/reactorcide/coordinator:$TAG"
echo "  - containers.catalystsquad.com/public/reactorcide/worker:$TAG"
