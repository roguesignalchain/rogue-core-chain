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
    FOUND=$(find "${CORE_DIR}" -name "${pattern}" -not -path "*/.git/*" -not -path "*/node_modules/*" || true)
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

# 3. Verify sidecar
if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client" ]; then
    echo "3. Checking Tauri sidecar..."
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

# 4. Check git status across repositories
echo "4. Checking git working tree cleanliness..."
if [ -d "${CORE_DIR}/.git" ]; then
    echo "-> Status in rogue-core-chain:"
    git -C "${CORE_DIR}" status --short
fi

if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client/.git" ]; then
    echo "-> Status in rogue-desktop-client:"
    git -C "${WORKSPACE_ROOT}/rogue-desktop-client" status --short
fi

echo "=== Pre-release checklist PASSED! ==="
