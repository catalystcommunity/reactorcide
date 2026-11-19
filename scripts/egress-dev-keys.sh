#!/usr/bin/env bash
# Make the development keys of the egress proxy (docs/egress-proxy.md) in
# the compose volume reactorcide_egress_keys: ca.crt, ca.key, job.key,
# control.token. An existing file is not replaced. The values are never
# printed.
#
# Usage: scripts/egress-dev-keys.sh [IMAGE]   (default reactorcide/egress:dev,
# built with `docker compose --profile egress build egress-proxy`)
set -euo pipefail

IMAGE="${1:-reactorcide/egress:dev}"
VOLUME="${REACTORCIDE_EGRESS_KEYS_VOLUME:-reactorcide_reactorcide_egress_keys}"

docker volume create "${VOLUME}" >/dev/null
run() { docker run --rm --user 0:0 -v "${VOLUME}:/keys" "${IMAGE}" "$@"; }

# The image has no shell; "ca", "key", and "control-token" refuse to replace
# a file, so a second run only reports that the files exist.
run ca --out /keys >/dev/null 2>&1 && echo "made ca.crt and ca.key" || echo "ca.crt/ca.key: kept the existing files"
run key --out /keys/job.key >/dev/null 2>&1 && echo "made job.key" || echo "job.key: kept the existing file"
run control-token --out /keys/control.token >/dev/null 2>&1 && echo "made control.token" || echo "control.token: kept the existing file"
echo "keys are in docker volume ${VOLUME}"
