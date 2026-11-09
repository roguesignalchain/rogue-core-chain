#!/usr/bin/env bash
# Package rogue-core-cli independent tarball for server operators and headless nodes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
WORKSPACE_ROOT="$(cd "${CORE_DIR}/.." && pwd -P)"
VERSION="0.1.0"
ARCH="x86_64"
OS="linux"
PKG_NAME="rogue-core-cli_${VERSION}_${OS}_${ARCH}"
DIST_DIR="${CORE_DIR}/dist"
PKG_DIR="${DIST_DIR}/${PKG_NAME}"

echo "=== [Rogue Signal] Packaging Headless CLI Bundle: ${PKG_NAME} ==="

# 1. Ensure core-rogue binary is compiled
if [ ! -f "${CORE_DIR}/build/bin/core-rogue" ]; then
    "${SCRIPT_DIR}/build-core.sh"
fi

rm -rf "${PKG_DIR}" "${DIST_DIR}/${PKG_NAME}.tar.gz"
mkdir -p "${PKG_DIR}/bin" "${PKG_DIR}/scripts" "${PKG_DIR}/config"

# 2. Copy artifacts
cp "${CORE_DIR}/build/bin/core-rogue" "${PKG_DIR}/bin/core-rogue"
chmod +x "${PKG_DIR}/bin/core-rogue"

if [ -f "${CORE_DIR}/genesis.json" ]; then
    cp "${CORE_DIR}/genesis.json" "${PKG_DIR}/config/genesis.json"
elif [ -f "${WORKSPACE_ROOT}/genesis.json" ]; then
    cp "${WORKSPACE_ROOT}/genesis.json" "${PKG_DIR}/config/genesis.json"
fi

cp "${SCRIPT_DIR}/rogue-node.service" "${PKG_DIR}/scripts/"
cp "${SCRIPT_DIR}/install-service.sh" "${PKG_DIR}/scripts/"
chmod +x "${PKG_DIR}/scripts/install-service.sh"

if [ -f "${CORE_DIR}/AGENTS_OPS.md" ]; then
    cp "${CORE_DIR}/AGENTS_OPS.md" "${PKG_DIR}/"
elif [ -f "${WORKSPACE_ROOT}/AGENTS_OPS.md" ]; then
    cp "${WORKSPACE_ROOT}/AGENTS_OPS.md" "${PKG_DIR}/"
fi
if [ -f "${CORE_DIR}/ai-agent.json" ]; then
    cp "${CORE_DIR}/ai-agent.json" "${PKG_DIR}/"
elif [ -f "${WORKSPACE_ROOT}/ai-agent.json" ]; then
    cp "${WORKSPACE_ROOT}/ai-agent.json" "${PKG_DIR}/"
fi

cat <<EOF > "${PKG_DIR}/README.txt"
Rogue Signal (ROGUE) Headless Core Node v${VERSION}
===================================================
Chain ID : 800055
Mining   : RandomX (rx/0)
Block Time: 10 seconds

QUICK START:
1. Initialize genesis:
   ./bin/core-rogue --rogue --datadir ~/.local/share/rogue-signal/chaindata init ./config/genesis.json

2. Run node:
   ./bin/core-rogue --rogue --datadir ~/.local/share/rogue-signal/chaindata --http --http.addr 127.0.0.1 --http.port 38849 --http.api eth,net,web3,rogue --port 38890

3. Automated systemd service installation:
   ./scripts/install-service.sh --user
EOF

# 3. Create tarball
cd "${DIST_DIR}"
tar -czvf "${PKG_NAME}.tar.gz" "${PKG_NAME}"
rm -rf "${PKG_DIR}"

# 4. Generate SHA256 checksum
sha256sum "${PKG_NAME}.tar.gz" > "${PKG_NAME}.tar.gz.sha256"

echo "=== Generated CLI package: ${DIST_DIR}/${PKG_NAME}.tar.gz ==="
