# JSON-RPC Extensions, CLI Flags & System Contracts

> Complete technical reference for Core-Rogue's `randomx` RPC namespace, disabled remote-mining `eth` stubs, Rogue Signal CLI flags, and the Block-0 `RogueMinerReputation` EVM system contract.

---

## 1. `randomx` JSON-RPC Namespace (`consensus/randomx/api.go`)

> [!IMPORTANT]
> For security on public RPC endpoints, the `randomx` namespace is **not** enabled by default in HTTP/WS modules (`cmd/geth/config.go`). To enable it on trusted local or authenticated nodes, pass `--http.api eth,net,web3,randomx` (or `--ws.api ...`).

### `randomx_getConsensusStatus`
Returns real-time consensus parameters, active monetary era telemetry, VM pool memory stats, and epoch status.

**Example Request:**
```bash
curl -s -X POST http://127.0.0.1:38845 \
  -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"randomx_getConsensusStatus","params":[],"id":1}'
```

**Response Schema (`ConsensusStatus`):**
```json
{
  "algorithm": "RandomX (rx/0)",
  "targetBlockTime": 30,
  "lwmaWindow": 60,
  "mtpWindow": 11,
  "maxFutureDrift": 30,
  "maxExtraDataSize": 64,
  "finalityMode": "ECBP-1100 (MESS)",
  "messActive": true,
  "unclesAllowed": false,
  "remoteMiningAllowed": false,
  "currentBlock": 1024,
  "currentEra": 0,
  "halvingInterval": 2000000,
  "blocksUntilHalving": 1998976,
  "blockRewardWei": "25000000000000000000",
  "minerRewardWei": "23750000000000000000",
  "treasuryRewardWei": "1250000000000000000",
  "treasuryAddress": "0xe9aae535b7971a9ac8c6af0e4b524d02260d3a03",
  "fastMode": false,
  "localHashrate": 0,
  "vmPool": {
    "totalVMs": 4,
    "activeVMs": 0,
    "mode": "light",
    "allocatedMemoryMB": 264,
    "largePages": true
  },
  "epochInfo": {
    "blockNumber": 1024,
    "epoch": 0,
    "epochLength": 2048,
    "seedLag": 64,
    "seedBlockNumber": 0,
    "seedHash": "0xde4c5e747fbb072d39db2ece5a245e46b6164045e166301dfdba78f86cfbe4dd",
    "nextEpochBlock": 2048,
    "blocksUntilNextEpoch": 1024,
    "fastMode": false
  }
}
```

### `randomx_getEpochInfo`
Returns the current RandomX epoch index, seed block number, canonical seed hash, next transition block, and remaining blocks.

### `randomx_getFastMode`
Returns `true` if RandomX Fast Mode (~2.08 GB dataset) is currently enabled on the local node, or `false` for Light Mode (~256 MB cache).

### `randomx_setFastMode`
Dynamically switches the local node between Light Mode (`false`) and Fast Mode (`true`) at runtime.
- **Parameters**: `[fastMode: boolean]`
- **Returns**: `boolean` (`true`)

### `randomx_getHashrate`
Returns the current local CPU miner hashrate in H/s (`uint64`).

---

## 2. `eth` PoW Namespace Behavior (`consensus/randomx/api.go`)

To enforce egalitarian in-node solo CPU mining and prevent external Stratum proxies or ASIC/GPU bridge pools:

| Method | Behavior on Rogue Signal |
| :--- | :--- |
| `eth_getWork` | **Disabled** — returns `ErrRemoteMiningDisabled` (`remote mining (eth_getWork / eth_submitWork) is disabled on Rogue Signal: only internal node CPU mining is supported`) |
| `eth_submitWork` | **Disabled** — always returns `false` |
| `eth_submitHashrate` | **Disabled** — always returns `false` |
| `eth_getHashrate` | Returns the live hashrate (`uint64`) of the internal CPU miner |
| `eth_sendRawTransaction` | Enforces **Type 0 (`LegacyTxType`)** only when RandomX consensus is active; rejects EIP-2930, EIP-1559, and EIP-4844 transactions |

---

## 3. Rogue Signal CLI Flags Reference (`cmd/utils/flags.go`)

| Flag | Default (with `--rogue`) | Description |
| :--- | :--- | :--- |
| `--rogue` | `false` | Activates Rogue Signal Chain ID `800055`, embedded genesis, P2P `:38888`, HTTP `:38845`, WS `:38846`, `<datadir>/rogue`, and `FullSync` default |
| `--randomx.fast` | `false` | Enables RandomX Fast Mode (~2.08 GB RAM dataset). Preserved even if `--mine` is omitted at startup so RPC/IPC-triggered mining runs in Fast Mode |
| `--syncmode` | `full` (when `--rogue` is set) | Sync mode (`full` or `snap`). Defaults to `full` on `--rogue` to avoid discarding propagated blocks on young networks |
| `--http.maxconnsperip` | `100` | Maximum concurrent HTTP/WebSocket requests per client IP (`0` disables). Exceeding returns `HTTP 429` with `Retry-After: 1` |
| `--http.trustedproxies` | `""` (empty, fail-closed) | Comma-separated trusted proxy IPs or CIDRs for extracting the real client IP via Rightmost Untrusted IP in `X-Forwarded-For` / `X-Real-IP` |
| `--miner.threads` | `-1` (disabled until started) | Number of CPU threads for internal RandomX mining |
| `--miner.etherbase` | `RogueTreasuryAddress` fallback | Coinbase address receiving 95% block rewards + 100% tx fees + gamification XP |
| `--miner.recommit` | `10s` | Interval to recreate sealing work |
| `--miner.gaslimit` / `GasCeil` | `50000000` | Target block gas ceiling aligned with Genesis `GasLimit` |

**Environment Variables**:
- `ROGUE_BOOTNODES`: Comma-separated list of `enode://...` URLs overriding `params.RogueBootnodes`.

---

## 4. System Contract: `RogueMinerReputation` (`0x...0020`)

Deployed natively in Block 0 (`params/genesis_rogue.go`) at `0x0000000000000000000000000000000000000020` (`Nonce: 1`).

### Storage Layout
- **Slot `0`**: `Ownable._owner` (initialized at genesis to `0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`).
- **Slot `1`**: `mapping(address => uint256) private _blocksMined` — mutated directly by consensus in `AccumulateRewards` from Block 1 onwards using key `keccak256(abi.encode(miner, uint256(1)))`.

### Read-Only EVM Interface (`eth_call` / `STATICCALL`)

| Function Signature | Return Type | Description |
| :--- | :--- | :--- |
| `blocksMined(address miner)` | `uint256` | Canonical blocks mined by `miner` |
| `getMinerLevel(address miner)` | `uint32` | Current cyberpunk tier (`0` to `6`) |
| `meetsLevel(address miner, uint32 minLevel)` | `bool` | Returns `true` if `miner` has reached `minLevel` (for on-chain gating) |
| `getMinerStats(address miner, string lang)` | `(uint256, uint32, string, string, uint256, uint256, uint16)` | Localized profile: `(blocksMined, level, title, imageURI, currentXP, nextLevelXP, progressBps)` |
| `getAllRanks(string lang)` | `RankInfo[]` | Full localized rank table metadata |

### Query Examples (`cast`)
```bash
# Check raw blocks mined
cast call 0x0000000000000000000000000000000000000020 \
  "blocksMined(address)(uint256)" 0xYourMinerAddress \
  --rpc-url http://127.0.0.1:38845

# Check if a miner meets Level 3 (Hash Warden, >= 100 blocks)
cast call 0x0000000000000000000000000000000000000020 \
  "meetsLevel(address,uint32)(bool)" 0xYourMinerAddress 3 \
  --rpc-url http://127.0.0.1:38845

# Fetch full localized miner stats in English
cast call 0x0000000000000000000000000000000000000020 \
  "getMinerStats(address,string)((uint256,uint32,string,string,uint256,uint256,uint16))" \
  0xYourMinerAddress "en" \
  --rpc-url http://127.0.0.1:38845
```

