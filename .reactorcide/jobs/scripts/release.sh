#!/bin/sh
set -e

SEMVER_TAGS_VERSION="v0.4.0"

# Assume /workspace is already cloned by the job command
cd /workspace

# -------------------------------------------------------------------
# 1. Download semver-tags
# -------------------------------------------------------------------
echo "=== Installing semver-tags ${SEMVER_TAGS_VERSION} ==="
wget -q "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" -O /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C /tmp
chmod +x /tmp/semver-tags
export PATH="/tmp:$PATH"

# -------------------------------------------------------------------
# 2. Run semver-tags to determine version bump
# -------------------------------------------------------------------
echo "=== Running semver-tags ==="
OUTPUT=$(semver-tags run --output_json 2>&1 | tail -1)
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
# 3. Set up container registry auth
# -------------------------------------------------------------------
echo "=== Setting up registry auth ==="
mkdir -p /home/reactorcide/.docker
cat > /home/reactorcide/.docker/config.json <<DOCKEREOF
{
  "auths": {
    "${REGISTRY}": {
      "auth": "$(echo -n "${REGISTRY_USER}:${REGISTRY_PASSWORD}" | base64)"
    }
  }
}
DOCKEREOF
export DOCKER_CONFIG=/home/reactorcide/.docker

# -------------------------------------------------------------------
# 4. Build and push runnerbase image
# -------------------------------------------------------------------
echo "=== Building runnerbase image ==="
docker build -t "${REGISTRY}/public/reactorcide/runnerbase:${NEW_TAG}" \
             -t "${REGISTRY}/public/reactorcide/runnerbase:latest" \
             -f runnerlib/Dockerfile.runner \
             runnerlib/
docker push "${REGISTRY}/public/reactorcide/runnerbase:${NEW_TAG}"
docker push "${REGISTRY}/public/reactorcide/runnerbase:latest"

# -------------------------------------------------------------------
# 5. Build Go binary for coordinator/worker containers
# -------------------------------------------------------------------
echo "=== Building Go binary ==="
cd coordinator_api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/reactorcide .
cd /workspace

# -------------------------------------------------------------------
# 6. Build and push coordinator image
# -------------------------------------------------------------------
echo "=== Building coordinator image ==="
cp /tmp/reactorcide deployment/reactorcide
docker build -t "${REGISTRY}/public/reactorcide/coordinator:${NEW_TAG}" \
             -t "${REGISTRY}/public/reactorcide/coordinator:latest" \
             -f deployment/Dockerfile.coordinator \
             deployment/
docker push "${REGISTRY}/public/reactorcide/coordinator:${NEW_TAG}"
docker push "${REGISTRY}/public/reactorcide/coordinator:latest"

# -------------------------------------------------------------------
# 7. Build and push worker image
# -------------------------------------------------------------------
echo "=== Building worker image ==="
docker build -t "${REGISTRY}/public/reactorcide/worker:${NEW_TAG}" \
             -t "${REGISTRY}/public/reactorcide/worker:latest" \
             -f deployment/Dockerfile.worker \
             deployment/
docker push "${REGISTRY}/public/reactorcide/worker:${NEW_TAG}"
docker push "${REGISTRY}/public/reactorcide/worker:latest"

# -------------------------------------------------------------------
# 8. Cross-compile CLI for all platforms
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
# 9. Install gh CLI and create GitHub release
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
