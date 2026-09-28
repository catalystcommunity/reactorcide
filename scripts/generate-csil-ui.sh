#!/usr/bin/env bash
# Regenerate the CSIL-RPC UI/Auth Go packages from coordinator_api/csil/reactorcide-ui.csil.
#
# Generates:
#   - coordinator_api/internal/uiapi/csilapi/  (--target go: types + codec + server
#     service interfaces, consumed by the hand-written dispatcher in
#     coordinator_api/internal/uiapi/)
#   - webapp/internal/uiclient/csilapi/        (--target go-client: types + codec +
#     typed client, consumed by the hand-written HTTP carrier in
#     webapp/internal/uiclient/)
#   - webapp/ui/src/api/csilapi/               (--target typescript-client: types +
#     codec + typed client, consumed by the SolidJS SPA through the browser
#     transport in webapp/ui/src/api/transport.ts)
#
# Only the *.gen.go files in those two directories are touched; the
# hand-written dispatcher.go/transport.go files alongside them are untouched.
# Requires the csilgen CLI of the release in CSILGEN_RELEASE on PATH, with the
# generators of the same release in ./.generators or ~/.csilgen/generators/.
# The script stops if `csilgen --version` does not agree with the pin. The CI
# job csil-gen-check (.reactorcide/plugins/plugin_ci_jobs.py) installs this
# release from its GitHub assets and reads the pin from this line.
set -euo pipefail

CSILGEN_RELEASE="csilgen/v0.2.9"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CSIL_INPUT="${ROOT_DIR}/coordinator_api/csil/reactorcide-ui.csil"
SERVER_OUT="${ROOT_DIR}/coordinator_api/internal/uiapi/csilapi"
CLIENT_OUT="${ROOT_DIR}/webapp/internal/uiclient/csilapi"
TS_OUT="${ROOT_DIR}/webapp/ui/src/api/csilapi"

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

mkdir -p "${SERVER_OUT}" "${CLIENT_OUT}" "${TS_OUT}"

echo "Generating coordinator server package -> ${SERVER_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go --output "${SERVER_OUT}"
# The CLI uses the coordinator package's client with the same transport as
# worker administration commands. Generate the client into this package too.
csilgen generate --input "${CSIL_INPUT}" --target go-client --output "${SERVER_OUT}"

echo "Generating webapp client package -> ${CLIENT_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go-client --output "${CLIENT_OUT}"

echo "Generating SPA TypeScript client -> ${TS_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target typescript-client --output "${TS_OUT}"

echo "Done. Hand-written files (dispatcher.go, context.go, transport.go, etc.) are untouched."
