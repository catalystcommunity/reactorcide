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
#
# Only the *.gen.go files in those two directories are touched; the
# hand-written dispatcher.go/transport.go files alongside them are untouched.
# Requires the csilgen CLI (~/.local/bin/csilgen) with generators installed
# under ~/.csilgen/generators/ (see csilgen/README.md).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CSIL_INPUT="${ROOT_DIR}/coordinator_api/csil/reactorcide-ui.csil"
SERVER_OUT="${ROOT_DIR}/coordinator_api/internal/uiapi/csilapi"
CLIENT_OUT="${ROOT_DIR}/webapp/internal/uiclient/csilapi"

if ! command -v csilgen >/dev/null 2>&1; then
  echo "error: csilgen CLI not found on PATH (expected e.g. ~/.local/bin/csilgen)" >&2
  exit 1
fi

echo "Validating ${CSIL_INPUT}"
csilgen validate --input "${CSIL_INPUT}"

mkdir -p "${SERVER_OUT}" "${CLIENT_OUT}"

echo "Generating coordinator server package -> ${SERVER_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go --output "${SERVER_OUT}"

echo "Generating webapp client package -> ${CLIENT_OUT}"
csilgen generate --input "${CSIL_INPUT}" --target go-client --output "${CLIENT_OUT}"

echo "Done. Hand-written files (dispatcher.go, context.go, transport.go, etc.) are untouched."
