# 协议、性能与安全优化

> 涵盖 Core-Rogue 共识验证流水线、零堆分配挖矿热循环、矿工调度优化、交易池加固、反向代理感知的 RPC 限流、同步默认值及 EVM 配置文件的工程参考。

---

## 1. 两阶段快速失败并行 `VerifyHeaders` (`consensus/randomx/randomx.go`)

RandomX 封印验证属于 CPU 密集型操作。为防止无效或畸形的 P2P 区块头批次在 CGo `randomx_calculate_hash` 上浪费 CPU 周期，`VerifyHeaders` 实现了基于内存 `headerChainReader`（按哈希与高度索引批次内区块头，避免批内祖先查询访问数据库）的 **两阶段生产者–消费者流水线**：

1. **阶段 1 — 微秒级快速失败验证（顺序生产者）**：
   - 校验 `header.Number == parent.Number + 1`。
   - 强制 `header.UncleHash == types.EmptyUncleHash`（`ErrUnclesNotAllowed`）。
   - 限制 `len(header.Extra) <= 64`（`MaxExtraDataSize = 64`，`ErrExtraDataTooLong`），防御区块头膨胀攻击。
   - 通过 **单次祖先遍历**（`CalculateDifficultyAndMTP11`）计算预期 LWMA-1 难度与 `MTP-11`，校验 `header.Time > mtp`、`header.Time <= now + 30s` 以及 `header.Difficulty == expectedDifficulty`。
   - 校验 `GasLimit <= MaxGasLimit`、`GasUsed <= GasLimit` 及 `misc.VerifyGaslimit`（$\pm 1/1024$ 波动限制）。
   - **区块哈希可塑性防护**：显式拒绝未激活分叉的可选区块头字段（`BaseFee`、`WithdrawalsHash`、`ExcessBlobGas`、`BlobGasUsed`、`ParentBeaconRoot` 必须全为 `nil`）。
2. **阶段 2 — 并行 PoW 封印验证（有界工作池）**：
   - 仅通过阶段 1 且 `seals[i] == true` 的区块头才会被分发到容量受 `runtime.NumCPU()` 限制的协程池（`sem`）。
   - 结果按 **严格的区块头索引顺序** 写入 `results` 通道，并支持通过 `abort` 通道即时取消。

---

## 2. 零堆分配 `VerifySeal` 与挖矿热循环 (`consensus/randomx/randomx.go`)

### 2.1 `VerifySeal` 快速失败预检

在获取 `RxContext` 或调用 CGo 之前，`VerifySeal` 将 `DifficultyToTarget(header.Difficulty)` 转换为 32 字节大端序数组（`TargetToBytes32`），并以 **零堆内存分配** 执行 `HashMeetsTarget(header.MixDigest, targetBE)`：
- 若声称的 `header.MixDigest` 已超过目标阈值，`VerifySeal` 会立即返回 `ErrInvalidPoW`，完全跳过 RandomX 计算。
- 若在 Fast Mode 节点上验证 **历史纪元** 区块（`Epoch(H) < Epoch(Head)`），`VerifySeal` 会路由至 `GetLightContextForHeader`，以 256 MB Light Mode 验证该区块，避免分配 2.08 GB 数据集。

### 2.2 `Seal()` 热循环与会话序列化

内置 CPU 矿工（`RandomX.Seal`）专为最大化持续算力与零内存抖动而设计：

1. **异步会话序列化 (`sealMu` / `activeSealDone`)**：`Seal()` 异步运行，不会因纪元初始化或 VM 预热阻塞 `miner/worker.go` 的 `taskLoop`，从而支持在 `stop` 或新链头到来时立即取消。每个新 `Seal()` 会话会等待 `prevDone`（除非收到 `stop`），确保前一个会话释放其池化 VM 后再获取新 VM，防止快速 recommit 期间发生瞬时 VM 池超额分配。
2. **MTP-11 漂移预休眠**：若 `MTP-11` 将 `header.Time` 推高至超出 `now + MaxFutureDrift`（30 秒），`Seal()` 会在可取消的定时器上等待精确差值（`header.Time - (now + MaxFutureDrift)`）后再开始哈希，避免在会被节点以 `ErrFutureBlock` 拒绝的区块上空耗 CPU。
3. **并行 VM 预热**：在启动挖矿线程前，使用 `GOMAXPROCS` 个工作协程并行调用 `ctx.PrewarmVMs(threads)`。
4. **零分配线程本地哈希**：
   - 每个线程在会话开始时获取一个 VM 并持有至结束（`ctx.AcquireVM()` / `defer ctx.ReleaseVM(vm)`）。
   - 76 字节的 `rx/0` 挖矿数据块（`var input [MiningBlobSize]byte`）在每个工作协程的栈上 **仅分配一次**；每次迭代仅更新偏移量 `39..46` 处的 8 字节小端序 nonce（`binary.LittleEndian.PutUint64(input[39:], nonce)`）。
   - 每个线程从独立加密随机种子偏移开始搜索：`baseNonce + uint64(tid)*0x100000000`（基于 `crypto/rand.Reader` 并带时间戳回退）。
   - 算力计数器每 **64 次哈希**（`attempts % 64 == 0`）批量提交一次，尽量减少原子操作竞争。
   - `HashMeetsTarget(rxHash, targetBE)` 原地比较 32 字节小端序 RandomX 输出与大端序目标值，实现 **零 `big.Int` 分配**。

---

## 3. 矿工任务调度与易用性优化 (`miner/miner.go`, `miner/worker.go`)

Core-Rogue 针对 30 秒 RandomX PoW 节奏专门调优了上游矿工流水线：

- **空交易池抑制预提交**：当 `TxPool` 待处理交易数为零时，`worker` 抑制冗余的空区块预提交（`commitNewWork`），消除对 RandomX 的不必要 CPU 中断与 VM 抖动。
- **`sealhash` 原子去重**：`worker.commit` 跟踪 `w.lastSealHash`，对相同 `sealhash` 的连续封印任务去重，确保每个新链头或 recommit 周期仅触发一次 `"Commit new sealing work"`。
- **对齐的默认配置 (`miner.DefaultConfig`)**：
  - `GasCeil = 50,000,000`（与 `DefaultRogueGenesisBlock().GasLimit` 和 `RPCGasCap` 保持一致）。
  - `Recommit = 10 * time.Second`（针对 30 秒出块间隔调优，替代上游激进的 2 秒间隔）。
- **无感 Etherbase 回退**：若未配置 `--miner.etherbase`（`0x0`），`worker.etherbase()` 会自动回退到 `params.RogueTreasuryAddress` 并仅打印一次警告日志，实现零阻力启动挖矿。
- **即时中止与算力清零**：停止挖矿（`SetThreads(<= 0)`）会立即取消当前封印、重置 EMA 算力追踪器（`tracker.Reset()`）并清理空闲 RandomX VM（`PurgeAllVMs()`）。

---

## 4. 交易池与交易验证加固 (`core/txpool/legacypool/legacypool.go`, `internal/ethapi/api.go`)

由于 Rogue Signal 采用无 EIP-1559 的经典 PoW 手续费市场（`BaseFee` 为 `nil`，100% 交易 Gas 费归矿工所有），RPC 入口与交易池准入执行一致的加固策略：

- **严格限制传统交易（仅限 Type 0）**：当 `chainConfig.GetConsensusEngineType().IsRandomX()` 为真时，`eth_sendRawTransaction`（`internal/ethapi/api.go`）和 `legacypool.validateTxBasics` 均仅接受 `types.LegacyTxType`（`0`），拒绝 EIP-2930（Type 1）、EIP-1559（Type 2）和 EIP-4844（Type 3）交易。
- **内存有界与防垃圾交易限制**：
  - `GlobalSlots = 4096 + 1024`（可执行槽位 + 浮动槽位）。
  - `txMaxSize = 128 KB`（`4 * txSlotSize`）。
  - Rogue Signal 强制 `128 MB` TxPool 内存上限与 `1 Gwei` 最低 Gas 价格（`PriceLimit`）。

---

## 5. HTTP/WebSocket RPC 与反向代理加固 (`node/rpcstack.go`, `cmd/geth/config.go`, `cmd/utils/flags.go`)

Rogue Signal 公共 RPC 节点内置 DoS 防护与防伪造代理支持：

1. **按 IP 并发限流 (`--http.maxconnsperip`)**：
   - 默认值：每个客户端 IP 最多 **`100`** 个并发 HTTP/WebSocket 请求（设为 `0` 禁用）。
   - 超限返回 **`HTTP 429 Too Many Requests`** 及标准 **`Retry-After: 1`** 响应头。
2. **受信任代理 CIDR 提取 (`--http.trustedproxies`)**：
   - **默认 Fail-Closed**：默认白名单为空——除非 `r.RemoteAddr` 匹配 `--http.trustedproxies` 中配置的 IP 或 CIDR，否则完全忽略 `X-Forwarded-For` 和 `X-Real-IP`。
   - **最右侧非受信任 IP（Rightmost Untrusted IP）**：在多层代理（Nginx、Caddy、Cloudflare）后方，Core-Rogue 从右向左解析 `X-Forwarded-For` 并选取 **最右侧不属于 `trustedProxies` 的 IP**（回退至 `X-Real-IP`），防止客户端伪造 `X-Forwarded-For` 前缀。
   - 已通过 `TestHTTPServerTrustedProxyAndRetryAfter` 测试覆盖。
3. **只读 `randomx` RPC 命名空间按需启用 (`cmd/geth/config.go`)**：
   - 默认 HTTP/WS 模块仅暴露 `eth`（其中 `EthAPI` 仅提供只读 `eth_getHashrate` 及被禁用的 `eth_getWork`/`eth_submitWork`/`eth_submitHashrate` 存根）。
   - `randomx` 命名空间（`randomx_getConsensusStatus`、`randomx_getEpochInfo`、`randomx_getFastMode`、`randomx_getHashrate`）需通过 `--http.api eth,net,web3,randomx` 显式启用，且不包含运行时模式切换接口（Fast Mode 仅支持启动时通过 `--randomx.fast` 配置）。

---

## 6. 同步模式与网络默认值 (`cmd/utils/flags.go`, `eth/backend.go`)

- **`--rogue` 默认 `FullSync`**：启用 `--rogue` 且未显式指定 `--syncmode` 时，Core-Rogue 默认使用 `downloader.FullSync` 而非 `SnapSync`，避免早期网络在等待 snap-sync 基准块期间丢弃广播区块。
- **对齐的默认线程状态**：`eth/backend.go` 以 `randomx.Config{Threads: -1}` 初始化，确保在显式调用 `--mine` 或 `miner_start` 前保持本地 CPU 挖矿关闭。
- **动态引导节点**：`resolveRogueBootnodes()` 在设置了环境变量 `ROGUE_BOOTNODES` 时优先读取，否则回退到 `params.RogueBootnodes`。
- **隔离端口与数据目录**：传入 `--rogue` 会自动设置 P2P `:38888`、HTTP RPC `:38845`、WS RPC `:38846` 及 `<datadir>/rogue`。

---

## 7. 从 Block 0 启用的加固 EVM 配置 (`params/config_rogue.go`)

Rogue Signal 从 **创世块（Block 0）** 起即激活现代化且经过安全加固的 **Cancun / Shanghai** EVM 操作码与 Gas 规则（`EIP2FBlock` 至 `EIP1153FBlock` = `0`），同时保留经典 PoW 手续费机制：

- **重放与 DoS 防护**：`EIP-155`（Chain ID `800055`）、`EIP-2565`（ModExp 定价）、`EIP-2929`（冷/热状态访问）、`EIP-3529`（减少退款）、`EIP-3541`（拒绝 `0xEF` 字节码）、`EIP-3860`（initcode 计量与大小限制）。
- **现代开发者操作码**：`EIP-3651`（Warm `COINBASE`）、`EIP-3855`（`PUSH0`）、`EIP-5656`（`MCOPY`）、`EIP-1153`（`TSTORE`/`TLOAD` 瞬态存储）、`EIP-6049`（`SELFDESTRUCT` 弃用警示）。
- **永久拆除难度炸弹**：`DisposalBlock = 0`——难度完全由 RandomX LWMA-1 控制。

