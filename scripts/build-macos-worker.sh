#!/usr/bin/env bash
#
# build-macos-worker.sh -- build and code-sign the reactorcide CLI/worker with
# the macOS "vm" JobRunner backend (Virtualization.framework via
# github.com/Code-Hex/vz/v3) enabled.
#
# The vz backend needs Cgo (CGO_ENABLED=1), the `vz` build tag, and a signed
# binary carrying the com.apple.security.virtualization entitlement. The normal
# release build cross-compiles darwin with CGO_ENABLED=0 and therefore selects
# the no-op stub (lifecycle_darwin_novz.go) -- run THIS script on an Apple
# Silicon Mac to produce a worker that can actually boot guest VMs.
#
# Ad-hoc signing (`-s -`) is sufficient for local/dev use on the same machine.
# A distributable build would substitute a real signing identity.
#
# Usage:
#   scripts/build-macos-worker.sh            # -> coordinator_api/reactorcide
#   OUTPUT=/tmp/reactorcide scripts/build-macos-worker.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CODE_DIR="${REPO_ROOT}/coordinator_api"
ENTITLEMENTS="${REPO_ROOT}/deployment/macos/vz.entitlements"
OUTPUT="${OUTPUT:-${CODE_DIR}/reactorcide}"

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "error: this script must run on macOS (Apple Silicon)" >&2
	exit 1
fi
if [[ "$(uname -m)" != "arm64" ]]; then
	echo "error: the macOS VM backend requires Apple Silicon (arm64); found $(uname -m)" >&2
	exit 1
fi
if [[ ! -f "${ENTITLEMENTS}" ]]; then
	echo "error: entitlements file not found: ${ENTITLEMENTS}" >&2
	exit 1
fi

echo "Building reactorcide with the vz macOS VM backend..."
(
	cd "${CODE_DIR}"
	CGO_ENABLED=1 go build -tags vz -o "${OUTPUT}" ./
)

echo "Code-signing ${OUTPUT} with the virtualization entitlement (ad-hoc)..."
codesign --force --entitlements "${ENTITLEMENTS}" -s - "${OUTPUT}"

echo "Verifying signature/entitlements..."
codesign --display --entitlements - "${OUTPUT}" >/dev/null

echo "Done: ${OUTPUT}"
echo "It now carries com.apple.security.virtualization and can run the 'vm' backend."
