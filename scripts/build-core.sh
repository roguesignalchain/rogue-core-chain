#!/usr/bin/env bash
# Build core-rogue native binary with statically linked RandomX and embedded build metadata.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
WORKSPACE_ROOT="$(cd "${CORE_DIR}/.." && pwd -P)"

echo "=== [Rogue Signal] Compiling core-rogue native executable ==="

cd "${CORE_DIR}"

# 1. Ensure RandomX static library is built
make librandomx

# 2. Extract git commit and timestamp for metadata injection
GIT_COMMIT="$(git rev-parse HEAD 2>/dev/null || echo "unknown")"
GIT_DATE="$(git log -1 --format=%cd --date=format:%Y%m%d 2>/dev/null || date +%Y%m%d)"
VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.1.0")"

echo "-> Git Commit : ${GIT_COMMIT}"
echo "-> Git Date   : ${GIT_DATE}"
echo "-> Version    : ${VERSION}"

# 3. Build geth and core-rogue with static randomx and linker flags
go run build/ci.go install ./cmd/geth
cp build/bin/geth build/bin/core-rogue

echo "-> Verified core-rogue binary:"
"${CORE_DIR}/build/bin/core-rogue" version

# 4. Copy to desktop client sidecar path if desktop repo exists
DESKTOP_BIN_DIR="${WORKSPACE_ROOT}/rogue-desktop-client/src-tauri/bin"
if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client" ]; then
    mkdir -p "${DESKTOP_BIN_DIR}"
    TARGET_TRIPLE="$(rustc -vV 2>/dev/null | sed -n 's|host: ||p' || true)"
    if [ -n "${TARGET_TRIPLE}" ]; then
        SIDECAR_NAME="core-rogue-${TARGET_TRIPLE}"
        cp "${CORE_DIR}/build/bin/core-rogue" "${DESKTOP_BIN_DIR}/${SIDECAR_NAME}"
        echo "-> Updated Desktop Sidecar: ${DESKTOP_BIN_DIR}/${SIDECAR_NAME}"
    fi
fi

echo "=== Build core-rogue completed successfully! ==="
