#!/usr/bin/env bash
# Release audit and verification checklist script.
# Validates repository cleanliness, absence of secrets, and test sanity before publishing.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
WORKSPACE_ROOT="$(cd "${CORE_DIR}/.." && pwd -P)"

echo "=== [Rogue Signal] Running Pre-Release Audit Checklist ==="

# 1. Check for sensitive files
echo "1. Checking for sensitive artifacts..."
FORBIDDEN_PATTERNS=(".rogue-wallet.vault" "*.pem" "*.key" "id_rsa" ".env")
for pattern in "${FORBIDDEN_PATTERNS[@]}"; do
    FOUND=$(find "${CORE_DIR}" -name "${pattern}" -not -path "*/.git/*" -not -path "*/node_modules/*" -not -path "*/build/*" -not -path "*/randomx-src/*" || true)
    if [ -n "${FOUND}" ]; then
        echo "SECURITY WARNING: Sensitive file pattern found in core matching '${pattern}':"
        echo "${FOUND}"
        exit 1
    fi
    if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client" ]; then
        FOUND_DESKTOP=$(find "${WORKSPACE_ROOT}/rogue-desktop-client" -name "${pattern}" -not -path "*/.git/*" -not -path "*/node_modules/*" -not -path "*/target/*" || true)
        if [ -n "${FOUND_DESKTOP}" ]; then
            echo "SECURITY WARNING: Sensitive file pattern found in desktop matching '${pattern}':"
            echo "${FOUND_DESKTOP}"
            exit 1
        fi
    fi
done
echo "-> Zero sensitive files detected."

# 2. Check binary existence
echo "2. Checking core-rogue build status..."
if [ ! -f "${CORE_DIR}/build/bin/core-rogue" ]; then
    echo "Building core-rogue..."
    "${SCRIPT_DIR}/build-core.sh"
fi

# 3. Code Sanity and Test Verification
echo "3. Running code sanity checks and test suite..."
echo "-> go vet ./consensus/randomx/..."
(cd "${CORE_DIR}" && go vet ./consensus/randomx/...)
echo "-> go test -race ./consensus/randomx/..."
(cd "${CORE_DIR}" && go test -race -count=1 ./consensus/randomx/)
echo "-> go test genesis & MESS in ./core/..."
(cd "${CORE_DIR}" && go test -count=1 ./core/ -run 'TestRogueGenesisBlockAndFinality|TestRogueGenesisJSON|TestRogueMESSFinality')
echo "-> Test suite passed."

# 4. Verify sidecar
if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client" ]; then
    echo "4. Checking Tauri sidecar..."
    TARGET_TRIPLE="$(rustc -vV 2>/dev/null | sed -n 's|host: ||p' || true)"
    if [ -n "${TARGET_TRIPLE}" ]; then
        SIDECAR="${WORKSPACE_ROOT}/rogue-desktop-client/src-tauri/bin/core-rogue-${TARGET_TRIPLE}"
        if [ ! -f "${SIDECAR}" ]; then
            echo "ERROR: Tauri sidecar missing at ${SIDECAR}"
            exit 1
        fi
        echo "-> Sidecar present: ${SIDECAR}"
    fi
fi

# 5. Check git status across repositories and scan for secret leaks
echo "5. Checking git working tree cleanliness and secret absence..."
if [ -d "${CORE_DIR}/.git" ]; then
    echo "-> Status in rogue-core-chain:"
    DIRTY_CORE="$(git -C "${CORE_DIR}" status --porcelain)"
    if [ -n "${DIRTY_CORE}" ]; then
        echo "${DIRTY_CORE}"
        echo "ERROR: Working tree in rogue-core-chain is not clean. Commit or stash changes before release."
        exit 1
    fi
    LEAK_CORE="$(git -C "${CORE_DIR}" grep -I -E 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY' -- . ':!scripts/verify-release.sh' ':!*_test.go' ':!tests/*' ':!crypto/*' ':!p2p/*' ':!accounts/*' ':!cmd/*' ':!internal/*' || true)"
    if [ -n "${LEAK_CORE}" ]; then
        echo "SECURITY ERROR: Potential private key header detected in rogue-core-chain:"
        echo "${LEAK_CORE}"
        exit 1
    fi
fi

if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client/.git" ]; then
    echo "-> Status in rogue-desktop-client:"
    DIRTY_DESKTOP="$(git -C "${WORKSPACE_ROOT}/rogue-desktop-client" status --porcelain)"
    if [ -n "${DIRTY_DESKTOP}" ]; then
        echo "${DIRTY_DESKTOP}"
        echo "ERROR: Working tree in rogue-desktop-client is not clean. Commit or stash changes before release."
        exit 1
    fi
fi

echo "=== Pre-release checklist PASSED! ==="
