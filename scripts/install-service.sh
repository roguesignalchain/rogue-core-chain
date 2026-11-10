#!/usr/bin/env bash
# Unattended or interactive service installer for Rogue Signal Node (Linux systemd).
# AI-Ready: Supports non-interactive automation flags (--user, --system, --datadir, --http-port, --mine, --threads).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
WORKSPACE_ROOT="$(cd "${CORE_DIR}/.." && pwd -P)"

MODE="user"
DATADIR="${HOME}/.local/share/rogue-signal/chaindata"
MINE="false"
THREADS="2"
COINBASE=""

# Resolve genesis source
if [ -f "${SCRIPT_DIR}/../config/genesis.json" ]; then
    GENESIS_SRC="${SCRIPT_DIR}/../config/genesis.json"
elif [ -f "${CORE_DIR}/genesis.json" ]; then
    GENESIS_SRC="${CORE_DIR}/genesis.json"
elif [ -f "${WORKSPACE_ROOT}/genesis.json" ]; then
    GENESIS_SRC="${WORKSPACE_ROOT}/genesis.json"
else
    GENESIS_SRC="/etc/rogue-signal/genesis.json"
fi

# Resolve core-rogue binary
if [ -f "${SCRIPT_DIR}/../bin/core-rogue" ]; then
    BIN_SRC="${SCRIPT_DIR}/../bin/core-rogue"
elif [ -f "${CORE_DIR}/build/bin/core-rogue" ]; then
    BIN_SRC="${CORE_DIR}/build/bin/core-rogue"
elif command -v core-rogue >/dev/null 2>&1; then
    BIN_SRC="$(command -v core-rogue)"
else
    BIN_SRC="/usr/local/bin/core-rogue"
fi

usage() {
    cat <<EOF
Rogue Signal Node Service Installer (systemd)
Usage:
  ./install-service.sh [OPTIONS]

Options:
  --user               Install as systemd user service (default, requires no root)
  --system             Install as system-wide service (requires root/sudo)
  --datadir <path>     Custom data directory (default: ~/.local/share/rogue-signal/chaindata)
  --mine               Enable mining on service start
  --threads <num>      Miner threads (default: 2)
  --coinbase <addr>    Miner etherbase payout address
  --help               Display this help
EOF
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --user) MODE="user"; shift ;;
        --system) MODE="system"; shift ;;
        --datadir) DATADIR="$2"; shift 2 ;;
        --mine) MINE="true"; shift ;;
        --threads) THREADS="$2"; shift 2 ;;
        --coinbase) COINBASE="$2"; shift 2 ;;
        --help) usage ;;
        *) echo "Unknown argument: $1"; usage ;;
    esac
done

echo "=== [Rogue Signal] Installing Node Service (${MODE} mode) ==="

if [ ! -f "${BIN_SRC}" ]; then
    echo "Error: core-rogue binary not found at '${BIN_SRC}'. Run build-core.sh first."
    exit 1
fi

mkdir -p "${DATADIR}"

# Initialize genesis if not yet initialized
if [ ! -d "${DATADIR}/geth/chaindata" ]; then
    echo "Initializing genesis block (Chain ID 800055)..."
    if [ -f "${GENESIS_SRC}" ]; then
        "${BIN_SRC}" --rogue --datadir "${DATADIR}" init "${GENESIS_SRC}"
    fi
fi

# Configure miner arguments
EXTRA_ARGS=""
if [ "${MINE}" == "true" ]; then
    EXTRA_ARGS="--mine --miner.threads ${THREADS}"
    if [ -n "${COINBASE}" ]; then
        EXTRA_ARGS="${EXTRA_ARGS} --miner.etherbase ${COINBASE}"
    fi
fi

if [ "${MODE}" == "user" ]; then
    UNIT_DIR="${HOME}/.config/systemd/user"
    mkdir -p "${UNIT_DIR}"
    SERVICE_FILE="${UNIT_DIR}/rogue-node.service"

    cat <<EOF > "${SERVICE_FILE}"
[Unit]
Description=Rogue Signal User Node
After=network.target

[Service]
Type=simple
ExecStart=${BIN_SRC} --rogue --datadir ${DATADIR} --http --http.addr 127.0.0.1 --http.port 38849 --http.api eth,net,web3,randomx --port 38890 --nat any --syncmode full --cache 512 ${EXTRA_ARGS}
Restart=always
RestartSec=5s
KillSignal=SIGTERM
TimeoutStopSec=15s

[Install]
WantedBy=default.target
EOF

    systemctl --user daemon-reload
    systemctl --user enable rogue-node.service
    systemctl --user restart rogue-node.service
    echo "-> Service installed and started at: ${SERVICE_FILE}"
    echo "-> Check status with: systemctl --user status rogue-node.service"
else
    if [ "$EUID" -ne 0 ]; then
        echo "Error: --system mode requires root privileges. Please run with sudo."
        exit 1
    fi

    SERVICE_USER="${SUDO_USER:-rogue}"
    if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
        useradd --system --home-dir /var/lib/rogue-signal --create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
    fi
    chown -R "${SERVICE_USER}:${SERVICE_USER}" "${DATADIR}"

    SERVICE_FILE="/etc/systemd/system/rogue-node.service"
    cat <<EOF > "${SERVICE_FILE}"
[Unit]
Description=Rogue Signal Headless System Node
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
ExecStart=${BIN_SRC} --rogue --datadir ${DATADIR} --http --http.addr 127.0.0.1 --http.port 38849 --http.api eth,net,web3,randomx --port 38890 --nat any --syncmode full --cache 512 ${EXTRA_ARGS}
Restart=always
RestartSec=5s
LimitNOFILE=65535
TimeoutStopSec=30s
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable rogue-node.service
    systemctl restart rogue-node.service
    echo "-> System service installed and started at: ${SERVICE_FILE}"
    echo "-> Check status with: systemctl status rogue-node.service"
fi

echo "=== Node service installation finished! ==="
