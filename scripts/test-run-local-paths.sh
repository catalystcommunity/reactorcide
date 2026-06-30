#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

BACKEND=""
if command -v nerdctl >/dev/null 2>&1 && nerdctl ps >/dev/null 2>&1; then
  BACKEND="containerd"
elif command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1; then
  BACKEND="docker"
else
  echo "SKIP: no usable container runtime found. Tried nerdctl/containerd, then docker."
  exit 0
fi

TMP_DIR="$(mktemp -d /tmp/reactorcide-run-local-paths.XXXXXX)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

SRC_DIR="$TMP_DIR/source"
mkdir -p "$SRC_DIR/nested"
printf 'mounted source\n' > "$SRC_DIR/nested/marker.txt"

run_job() {
  local job_file="$1"

  cd "$ROOT_DIR/coordinator_api"
  if [ "${REACTORCIDE_TEST_USE_BINARY:-}" = "1" ] && [ -x ./reactorcide ]; then
    ./reactorcide run-local --backend "$BACKEND" --job-dir "$SRC_DIR" "$job_file"
  else
    go run . run-local --backend "$BACKEND" --job-dir "$SRC_DIR" "$job_file"
  fi
}

CUSTOM_PATHS_JOB="$TMP_DIR/custom-paths.yaml"
cat > "$CUSTOM_PATHS_JOB" <<'YAML'
name: custom-paths
image: alpine:3.20
code_dir: /job/custom-code
job_dir: /job/custom-code/nested
command: |-
  set -eu
  test "$REACTORCIDE_CODE_DIR" = "/job/custom-code"
  test "$REACTORCIDE_JOB_DIR" = "/job/custom-code/nested"
  test "$(pwd)" = "/job/custom-code/nested"
  test "$(cat marker.txt)" = "mounted source"
  echo "custom path run-local ok"
YAML

ROOT_JOB="$TMP_DIR/root.yaml"
cat > "$ROOT_JOB" <<'YAML'
name: run-as-root
image: alpine:3.20
run_as:
  user: root
command: |-
  set -eu
  test "$(id -u)" = "0"
  echo "root run-local ok"
YAML

run_job "$CUSTOM_PATHS_JOB"
run_job "$ROOT_JOB"
