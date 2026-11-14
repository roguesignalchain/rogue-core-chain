# JSON-RPC 扩展、CLI 参数与系统合约

> Core-Rogue `randomx` RPC 命名空间、被禁用的 `eth` 远程挖矿存根、Rogue Signal CLI 参数以及 Block 0 `RogueMinerReputation` 系统合约的完整技术参考。

---

## 1. `randomx` JSON-RPC 命名空间 (`consensus/randomx/api.go`)

> [!IMPORTANT]
> 出于公共 RPC 端点安全考虑，`randomx` 命名空间 **默认未开启**（`cmd/geth/config.go`）。如需在受信任的本地或认证节点上启用，请传入 `--http.api eth,net,web3,randomx`（或 `--ws.api ...`）。

### `randomx_getConsensusStatus`
返回实时共识参数、当前货币纪元遥测数据、VM 池内存统计以及纪元状态。

**请求示例：**
```bash
curl -s -X POST http://127.0.0.1:38845 \
  -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"randomx_getConsensusStatus","params":[],"id":1}'
```

**响应结构 (`ConsensusStatus`)：**
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
返回当前 RandomX 纪元索引、种子区块号、规范种子哈希、下一切换区块及剩余区块数。

### `randomx_getFastMode`
若本地节点当前启用了 RandomX Fast Mode（~2.08 GB 数据集）则返回 `true`，若为 Light Mode（~256 MB 缓存）则返回 `false`。Fast Mode 需在启动时通过 `--randomx.fast` 配置（已刻意移除 `randomx_setFastMode` 以防止通过 RPC 在运行时触发 2.08 GB 内存分配风险）。

### `randomx_getHashrate`
返回本地内置 CPU 矿工的当前算力（单位 H/s，`uint64`）。

---

## 2. `eth` PoW 命名空间与 EVM 规则 (`consensus/randomx/api.go`)

为强制执行平等的节点内单机 CPU 挖矿并阻断外部 Stratum 代理或 ASIC/GPU 桥接矿池：

| 方法 | Rogue Signal 上的行为 |
| :--- | :--- |
| `eth_getWork` | **已禁用** — 返回 `ErrRemoteMiningDisabled`（`remote mining (eth_getWork / eth_submitWork) is disabled on Rogue Signal: only internal node CPU mining is supported`） |
| `eth_submitWork` | **已禁用** — 始终返回 `false` |
| `eth_submitHashrate` | **已禁用** — 始终返回 `false` |
| `eth_getHashrate` | 返回内置 CPU 矿工的实时算力（`uint64`） |
| `eth_sendRawTransaction` | 当 RandomX 共识激活时仅允许 **Type 0 (`LegacyTxType`)**；拒绝类型化交易（`0x01` EIP-2930、`0x02` EIP-1559、`0x03` EIP-4844）。EIP-1559 `BASEFEE` (`0x48`) 默认处于禁用状态。 |

---

## 3. Rogue Signal CLI 参数参考 (`cmd/utils/flags.go`)

| 参数 | 默认值（带 `--rogue`） | 说明 |
| :--- | :--- | :--- |
| `--rogue` | `false` | 激活 Rogue Signal Chain ID `800055`、内嵌创世块、P2P `:38888`、HTTP `:38845`、WS `:38846`、`<datadir>/rogue` 及默认 `FullSync` |
| `--randomx.fast` | `false` | 启用 RandomX Fast Mode（~2.08 GB RAM 数据集）。即使启动时未传 `--mine` 也会保留该配置，以便后续通过 RPC/IPC 启动挖矿时使用 Fast Mode |
| `--syncmode` | `full`（启用 `--rogue` 时） | 同步模式（`full` 或 `snap`）。`--rogue` 默认使用 `full`，防止早期网络在等待 snap-sync 基准块时丢弃广播区块 |
| `--http.maxconnsperip` | `100` | 每个客户端 IP 允许的最大并发 HTTP/WebSocket 请求数（`0` 表示禁用）。超限返回 `HTTP 429` 及 `Retry-After: 1` |
| `--http.trustedproxies` | `""`（空，fail-closed） | 逗号分隔的受信任代理 IP 或 CIDR，用于从 `X-Forwarded-For` / `X-Real-IP` 中以 Rightmost Untrusted IP 提取真实客户端 IP |
| `--miner.threads` | `-1`（启动前禁用） | 内置 RandomX CPU 挖矿线程数 |
| `--miner.etherbase` | 回退至 `RogueTreasuryAddress` | 接收 95% 区块奖励 + 100% 交易手续费 + 游戏化 XP 的 Coinbase 地址 |
| `--miner.recommit` | `10s` | 重新生成封印任务的时间间隔 |
| `--miner.gaslimit` / `GasCeil` | `50000000` | 与创世块 `GasLimit` 对齐的目标区块 Gas 上限 |

**环境变量**：
- `ROGUE_BOOTNODES`：逗号分隔的 `enode://...` URL 列表，用于覆盖 `params.RogueBootnodes`。

---

## 4. 系统合约：`RogueMinerReputation` (`0x...0020`)

在 Block 0（`params/genesis_rogue.go`）原生部署于地址 `0x0000000000000000000000000000000000000020`（`Nonce: 1`）。

### 存储布局（Storage Layout）
- **Slot `0`**：`Ownable._owner`（创世时初始化为 `0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`）。为加固生产治理安全，建议通过 `transferOwnership` 将所有权转移至多签地址，或在元数据定稿后调用 `renounceOwnership()`。
- **Slot `1`**：`mapping(address => uint256) private _blocksMined` —— 自 Block 1 起在 `AccumulateRewards` 中由共识直接更新，键为 `keccak256(abi.encode(miner, uint256(1)))`。

### 只读 EVM 接口 (`eth_call` / `STATICCALL`)

| 函数签名 | 返回类型 | 说明 |
| :--- | :--- | :--- |
| `blocksMined(address miner)` | `uint256` | `miner` 已挖出的规范区块数 |
| `getMinerLevel(address miner)` | `uint32` | 当前赛博朋克等级（`0` 到 `6`） |
| `meetsLevel(address miner, uint32 minLevel)` | `bool` | 若 `miner` 已达到 `minLevel` 则返回 `true`（用于链上权限门槛） |
| `getMinerStats(address miner, string lang)` | `(uint256, uint32, string, string, uint256, uint256, uint16)` | 本地化档案：`(blocksMined, level, title, imageURI, currentXP, nextLevelXP, progressBps)` |
| `getAllRanks(string lang)` | `RankInfo[]` | 完整的本地化等级元数据表 |

### 查询示例 (`cast`)
```bash
# 查询原始已挖区块数
cast call 0x0000000000000000000000000000000000000020 \
  "blocksMined(address)(uint256)" 0xYourMinerAddress \
  --rpc-url http://127.0.0.1:38845

# 检查矿工是否达到 Level 3（Hash Warden，>= 100 个区块）
cast call 0x0000000000000000000000000000000000000020 \
  "meetsLevel(address,uint32)(bool)" 0xYourMinerAddress 3 \
  --rpc-url http://127.0.0.1:38845

# 获取中文（简体）完整本地化矿工档案
cast call 0x0000000000000000000000000000000000000020 \
  "getMinerStats(address,string)((uint256,uint32,string,string,uint256,uint256,uint16))" \
  0xYourMinerAddress "zh" \
  --rpc-url http://127.0.0.1:38845
```

