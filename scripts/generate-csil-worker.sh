#!/usr/bin/env bash
# Regenerate the CSIL-RPC Worker Go packages from
# coordinator_api/csil/reactorcide-worker.csil.
#
# Generates:
#   - coordinator_api/internal/workerapi/csilapi/    (--target go: types +
#     codec + server service interface, consumed by the hand-written
#     dispatcher wiring in coordinator_api/internal/uiapi/dispatcher.go's
#     NewHandlerWithWorker and by the coordinator_api/internal/workerapi
#     implementations).
#   - coordinator_api/internal/workerclient/csilapi/ (--target go-client:
#     types + codec + typed client, consumed by the hand-written HTTP
#     carrier in coordinator_api/internal/workerclient/transport.go, used by
#     the coordinator-mediated worker binary and run loop).
#
# Only the *.gen.go files in those two directories are touched; the
# hand-written transport.go/client.go alongside workerclient/csilapi is
# untouched, same as deps.go/dispatcher wiring alongside workerapi/csilapi.
# Requires the csilgen CLI of the release in CSILGEN_RELEASE on PATH, with the
# generators of the same release in ./.generators or ~/.csilgen/generators/.
# The script stops if `csilgen --version` does not agree with the pin. The CI
# job csil-gen-check (.reactorcide/plugins/plugin_ci_jobs.py) installs this
# release from its GitHub assets and reads the pin from this line.
set -euo pipefail

CSILGEN_RELEASE="csilgen/v0.2.9"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CSIL_INPUT="${ROOT_DIR}/coordinator_api/csil/reactorcide-worker.csil"
SERVER_OUT="${ROOT_DIR}/coordinator_api/internal/workerapi/csilapi"
CLIENT_OUT="${ROOT_DIR}/coordinator_api/internal/workerclient/csilapi"

if ! command -v csilgen >/dev/null 2>&1; then
  echo "error: csilgen CLI not found on PATH (expected e.g. ~/.local/bin/csilgen)" >&2
  exit 1
fi

CSILGEN_EXPECTED="csilgen ${CSILGEN_RELEASE#csilgen/v}"
CSILGEN_ACTUAL="$(csilgen --version 2>/dev/null || true)"
if [[ "${CSILGEN_ACTUAL}" != "${CSILGEN_EXPECTED}" ]]; then
  echo "error: csilgen on PATH reports '${CSILGEN_ACTUAL}', expected '${CSILGEN_EXPECTED}' (${CSILGEN_RELEASE})" >&2
  exit 1
fi

echo "Validating ${CSIL_INPUT}"
csilgen validate --input "${CSIL_INPUT}"

mkdir -p "${SERVER_OUT}" "${CLIENT_OUT}"

echo "Generating coordinator server package -> ${SERVER_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go --output "${SERVER_OUT}"

echo "Generating worker client package -> ${CLIENT_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go-client --output "${CLIENT_OUT}"

echo "Done. Hand-written files (deps.go, dispatcher wiring, transport.go, client.go, etc.) are untouched."
