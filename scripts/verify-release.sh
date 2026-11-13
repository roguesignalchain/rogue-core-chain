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
FORBIDDEN_PATTERNS=(".rogue-wallet.vault" "*.pem" "*.key" "id_rsa" ".env" ".env.*")
for pattern in "${FORBIDDEN_PATTERNS[@]}"; do
    FOUND=$(find "${CORE_DIR}" -name "${pattern}" -not -name ".env.example" -not -name ".env.template" -not -path "*/.git/*" -not -path "*/node_modules/*" -not -path "*/build/*" -not -path "*/randomx-src/*" || true)
    if [ -n "${FOUND}" ]; then
        echo "SECURITY WARNING: Sensitive file pattern found in core matching '${pattern}':"
        echo "${FOUND}"
        exit 1
    fi
    if [ -d "${WORKSPACE_ROOT}/rogue-desktop-client" ]; then
        FOUND_DESKTOP=$(find "${WORKSPACE_ROOT}/rogue-desktop-client" -name "${pattern}" -not -name ".env.example" -not -name ".env.template" -not -path "*/.git/*" -not -path "*/node_modules/*" -not -path "*/target/*" || true)
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
(cd "${CORE_DIR}" && go test -count=1 ./core/ -run 'TestRogueGenesisBlockAndFinality|TestRogueMESSFinality')
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
    LEAK_CORE="$(git -C "${CORE_DIR}" grep -I -E 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY' -- . ':!scripts/verify-release.sh' ':!*_test.go' ':!tests/*' ':!crypto/*' ':!p2p/*' || true)"
    if [ -n "${LEAK_CORE}" ]; then
        echo "SECURITY ERROR: Potential private key header detected in rogue-core-chain:"
        echo "${LEAK_CORE}"
        exit 1
    fi
    SENSITIVE_RE='(mnemonic|seed_phrase|recovery_phrase|private_key|privkey|wallet_secret)[[:space:]]*[:=][[:space:]]*["'\'']([a-z]{2,}[[:space:]]+){11,}[a-z]{2,}["'\'']|(mnemonic|seed_phrase|recovery_phrase|private_key|privkey|wallet_secret)[[:space:]]*[:=][[:space:]]*["'\''](0x)?[0-9a-fA-F]{64}["'\'']'
    MNEMONIC_CORE="$(git -C "${CORE_DIR}" grep -I -i -E "${SENSITIVE_RE}" -- . ':!scripts/verify-release.sh' ':!*.md' ':!*_test.go' ':!tests/*' || true)"
    if [ -n "${MNEMONIC_CORE}" ]; then
        echo "SECURITY ERROR: Potential mnemonic or hardcoded secret assignment detected in rogue-core-chain:"
        echo "${MNEMONIC_CORE}"
        exit 1
    fi
    HEX64_CORE="$(git -C "${CORE_DIR}" grep -I -n -E '(0x|")[0-9a-fA-F]{64}(")?' -- . ':!scripts/verify-release.sh' || true)"
    if [ -n "${HEX64_CORE}" ]; then
        HEX64_COUNT="$(printf '%s\n' "${HEX64_CORE}" | wc -l)"
        echo "WARNING: Found ${HEX64_COUNT} 64-char hex literal(s) in rogue-core-chain (review for raw private keys vs genesis/test hashes):"
        printf '%s\n' "${HEX64_CORE}" | sed -n '1,20p'
        if [ "${HEX64_COUNT}" -gt 20 ]; then
            echo "... (truncated; run git grep -I -n -E '(0x|\")[0-9a-fA-F]{64}' to inspect all ${HEX64_COUNT} matches)"
        fi
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
    LEAK_DESKTOP="$(git -C "${WORKSPACE_ROOT}/rogue-desktop-client" grep -I -E 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY' -- . || true)"
    if [ -n "${LEAK_DESKTOP}" ]; then
        echo "SECURITY ERROR: Potential private key header detected in rogue-desktop-client:"
        echo "${LEAK_DESKTOP}"
        exit 1
    fi
    # Scan non-test source files in rogue-desktop-client for hardcoded mnemonics/secrets (Rust #[cfg(test)] blocks excluded).
    MNEMONIC_DESKTOP="$(git -C "${WORKSPACE_ROOT}/rogue-desktop-client" ls-files -z -- ':!*.md' ':!*.lock' | xargs -0 -I{} awk '
        FNR==1 { in_test=0 }
        /#\[cfg\(test\)\]/ { in_test=1 }
        !in_test && tolower($0) ~ /(mnemonic|seed_phrase|recovery_phrase|private_key|privkey|wallet_secret)[[:space:]]*[:=][[:space:]]*["\047]([a-z]{2,}[[:space:]]+){11,}[a-z]{2,}["\047]/ { print FILENAME ":" FNR ":" $0 }
        !in_test && tolower($0) ~ /(mnemonic|seed_phrase|recovery_phrase|private_key|privkey|wallet_secret)[[:space:]]*[:=][[:space:]]*["\047](0x)?[0-9a-fA-F]{64}["\047]/ { print FILENAME ":" FNR ":" $0 }
    ' "${WORKSPACE_ROOT}/rogue-desktop-client/{}" 2>/dev/null || true)"
    if [ -n "${MNEMONIC_DESKTOP}" ]; then
        echo "SECURITY ERROR: Potential mnemonic or hardcoded secret assignment detected in rogue-desktop-client:"
        echo "${MNEMONIC_DESKTOP}"
        exit 1
    fi
    HEX64_DESKTOP="$(git -C "${WORKSPACE_ROOT}/rogue-desktop-client" grep -I -n -E '(0x|")[0-9a-fA-F]{64}(")?' -- . || true)"
    if [ -n "${HEX64_DESKTOP}" ]; then
        HEX64_D_COUNT="$(printf '%s\n' "${HEX64_DESKTOP}" | wc -l)"
        echo "WARNING: Found ${HEX64_D_COUNT} 64-char hex literal(s) in rogue-desktop-client (review for raw private keys vs hashes):"
        printf '%s\n' "${HEX64_DESKTOP}" | sed -n '1,20p'
        if [ "${HEX64_D_COUNT}" -gt 20 ]; then
            echo "... (truncated; inspect rogue-desktop-client manually)"
        fi
    fi
fi

echo "=== Pre-release checklist PASSED! ==="
