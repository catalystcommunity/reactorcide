#!/bin/sh
set -e

SEMVER_TAGS_VERSION="v0.4.0"
BUILDKIT_VERSION="0.17.3"

# Assume /workspace is already cloned by the job command
cd /workspace

# -------------------------------------------------------------------
# 1. Install tooling
# -------------------------------------------------------------------
# semver-tags is dynamically linked against glibc; Alpine uses musl (gcompat).
# runc is the OCI worker backend for buildkitd.
echo "=== Installing tooling ==="
apk add --no-cache gcompat runc > /dev/null

# -------------------------------------------------------------------
# 2. Install semver-tags
# -------------------------------------------------------------------
echo "=== Installing semver-tags ${SEMVER_TAGS_VERSION} ==="
wget -q "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" -O /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C /tmp
chmod +x /tmp/semver-tags
export PATH="/tmp:$PATH"

# -------------------------------------------------------------------
# 3. Run semver-tags to determine version bump
# -------------------------------------------------------------------
echo "=== Running semver-tags ==="
semver-tags run --output_json > /tmp/semver-output.txt 2>&1
OUTPUT=$(tail -1 /tmp/semver-output.txt)
echo "Output: ${OUTPUT}"

NEW_TAG=$(echo "${OUTPUT}" | grep -o '"New_release_git_tag":"[^"]*"' | cut -d'"' -f4)
PUBLISHED=$(echo "${OUTPUT}" | grep -o '"New_release_published":"[^"]*"' | cut -d'"' -f4)

if [ "${PUBLISHED}" != "true" ]; then
  echo "No new release needed."
  exit 0
fi

echo "=== New release: ${NEW_TAG} ==="
VERSION="${NEW_TAG#v}"

# -------------------------------------------------------------------
# 4. Install and start buildkitd
# -------------------------------------------------------------------
# We use our own buildkitd (OCI worker) instead of the host docker daemon so
# we can configure the internal registry as plaintext HTTP without touching
# daemon-level insecure-registries config.
echo "=== Installing buildkit ${BUILDKIT_VERSION} ==="
export HOME=/root
export XDG_RUNTIME_DIR=/tmp/run-root
mkdir -p "$XDG_RUNTIME_DIR" "$HOME/.docker"

wget -q "https://github.com/moby/buildkit/releases/download/v${BUILDKIT_VERSION}/buildkit-v${BUILDKIT_VERSION}.linux-amd64.tar.gz" -O /tmp/buildkit.tar.gz
tar -xzf /tmp/buildkit.tar.gz -C /usr/local
rm /tmp/buildkit.tar.gz

# Tell buildkitd the internal registry is plaintext HTTP. The external
# registry stays HTTPS (buildkit's default).
mkdir -p /etc/buildkit
cat > /etc/buildkit/buildkitd.toml <<BKCONF
[registry."${REGISTRY_INTERNAL}"]
  http = true
  insecure = true
BKCONF

echo "=== Starting buildkitd ==="
buildkitd \
  --config /etc/buildkit/buildkitd.toml \
  --oci-worker=true \
  --containerd-worker=false \
  --root="$HOME/.local/share/buildkit" \
  --addr="unix://$XDG_RUNTIME_DIR/buildkit/buildkitd.sock" &

export BUILDKIT_HOST="unix://$XDG_RUNTIME_DIR/buildkit/buildkitd.sock"

for i in $(seq 1 30); do
  if buildctl debug info >/dev/null 2>&1; then
    echo "buildkitd is ready"
    break
  fi
  sleep 1
done

# -------------------------------------------------------------------
# 5. Set up registry auth
# -------------------------------------------------------------------
echo "=== Setting up registry auth ==="
AUTH=$(printf "%s:%s" "$REGISTRY_USER" "$REGISTRY_PASSWORD" | base64 -w 0)
cat > "$HOME/.docker/config.json" <<DOCKEREOF
{
  "auths": {
    "${REGISTRY_INTERNAL}": {"auth": "${AUTH}"}
  }
}
DOCKEREOF
export DOCKER_CONFIG="$HOME/.docker"

# Helper: build and push an image with both :$NEW_TAG and :latest.
# Uses buildctl's multi-name output so the build only runs once.
build_and_push() {
  context="$1"
  dockerfile="$2"
  repo="$3"
  echo "=== Building and pushing ${repo}:${NEW_TAG} and :latest ==="
  buildctl build \
    --frontend dockerfile.v0 \
    --local context="$context" \
    --local dockerfile="$context" \
    --opt filename="$dockerfile" \
    --opt build-arg:CACHEBUST="$(date +%s)" \
    --no-cache \
    --output "type=image,\"name=${repo}:${NEW_TAG},${repo}:latest\",push=true"
}

# -------------------------------------------------------------------
# 6. Build and push runnerbase image
# -------------------------------------------------------------------
build_and_push runnerlib Dockerfile.runner \
  "${REGISTRY_INTERNAL}/public/reactorcide/runnerbase"

# -------------------------------------------------------------------
# 7. Build Go binary for coordinator/worker containers
# -------------------------------------------------------------------
echo "=== Building Go binary ==="
cd coordinator_api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/reactorcide .
cd /workspace

# -------------------------------------------------------------------
# 8. Build and push coordinator image
# -------------------------------------------------------------------
cp /tmp/reactorcide deployment/reactorcide
build_and_push deployment Dockerfile.coordinator \
  "${REGISTRY_INTERNAL}/public/reactorcide/coordinator"

# -------------------------------------------------------------------
# 9. Build and push worker image
# -------------------------------------------------------------------
build_and_push deployment Dockerfile.worker \
  "${REGISTRY_INTERNAL}/public/reactorcide/worker"

rm -f deployment/reactorcide

# -------------------------------------------------------------------
# 10. Build and push web UI image
# -------------------------------------------------------------------
echo "=== Building web UI binary ==="
cd /workspace/webapp
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /tmp/reactorcide-web .
cd /workspace

cp /tmp/reactorcide-web deployment/reactorcide-web
build_and_push deployment Dockerfile.web \
  "${REGISTRY_INTERNAL}/public/reactorcide/web"

rm -f deployment/reactorcide-web

# -------------------------------------------------------------------
# 11. Cross-compile CLI for all platforms
# -------------------------------------------------------------------
echo "=== Cross-compiling CLI ==="
RELEASE_DIR="/tmp/release"
mkdir -p "${RELEASE_DIR}"

cd /workspace/coordinator_api

PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
for PLATFORM in ${PLATFORMS}; do
  OS=$(echo "${PLATFORM}" | cut -d/ -f1)
  ARCH=$(echo "${PLATFORM}" | cut -d/ -f2)
  BINARY="reactorcide"
  if [ "${OS}" = "windows" ]; then
    BINARY="reactorcide.exe"
  fi

  echo "  Building ${OS}/${ARCH}..."
  CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" go build -o "/tmp/${BINARY}" .

  ARCHIVE_NAME="reactorcide-${VERSION}-${OS}-${ARCH}"
  if [ "${OS}" = "windows" ]; then
    cd /tmp && zip "${RELEASE_DIR}/${ARCHIVE_NAME}.zip" "${BINARY}" && cd /workspace/coordinator_api
  else
    tar -czf "${RELEASE_DIR}/${ARCHIVE_NAME}.tar.gz" -C /tmp "${BINARY}"
  fi
  rm -f "/tmp/${BINARY}"
done

cd /workspace

# -------------------------------------------------------------------
# 12. Install gh CLI and create GitHub release
# -------------------------------------------------------------------
echo "=== Creating GitHub release ==="
GHCLI_VERSION="2.63.2"
wget -q "https://github.com/cli/cli/releases/download/v${GHCLI_VERSION}/gh_${GHCLI_VERSION}_linux_amd64.tar.gz" -O /tmp/gh.tar.gz
tar -xzf /tmp/gh.tar.gz -C /tmp
mkdir -p /home/reactorcide/bin
cp "/tmp/gh_${GHCLI_VERSION}_linux_amd64/bin/gh" /home/reactorcide/bin/gh
export PATH="/home/reactorcide/bin:$PATH"

GH_TOKEN="${GITHUB_PAT}" gh release create "${NEW_TAG}" \
  --repo "${REACTORCIDE_REPO}" \
  --title "${NEW_TAG}" \
  --notes "Released by Reactorcide CI" \
  ${RELEASE_DIR}/*

echo "=== Released ${NEW_TAG} ==="
