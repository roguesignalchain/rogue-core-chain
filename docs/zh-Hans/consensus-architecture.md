# Rogue Signal 共识与 RandomX 架构

> **Core-Rogue** 共识引擎（`consensus/randomx`）的深度技术规范，涵盖 CGo 内存生命周期、LWMA-1 难度调整、精确上限货币政策、ECBP-1100 (MESS) 最终性以及协议级矿工声誉系统。

---

## 1. RandomX (`rx/0`) CGo 引擎与内存生命周期 (`consensus/randomx/rx_cgo.go`)

Core-Rogue 从源码（`consensus/randomx/randomx-src`）静态编译官方 RandomX C++ 库（`librandomx.a`），并通过 CGo 进行绑定。由于 RandomX 会在 Go 堆外分配大块内存区域（~256 MB 或 ~2.08 GB）以及 JIT 编译的虚拟机缓冲区，Go 封装层专门针对 **零 Use-After-Free (UAF)**、**零 GC 抖动** 和 **可预测的内存上限** 进行了工程设计。

### 1.1 运行模式：Light Mode 与 Fast Mode

| 模式 | 核心数据结构 | 内存占用 | 初始化方式 | 适用场景 |
| :--- | :--- | :--- | :--- | :--- |
| **Light Mode** *（默认）* | `randomx_cache` | **~256 MB** + ~2.2 MB/VM | 单线程 Argon2d 缓存初始化 | 验证节点、桌面节点、笔记本电脑、历史纪元验证 |
| **Fast Mode** (`--randomx.fast` + `--mine`) | `randomx_dataset` | **~2.08 GB** + ~2.2 MB/VM | 使用 `runtime.NumCPU() / 2` 个工作协程并行初始化 | 专用 CPU 矿工与高性能服务器 |

**Fast Mode 内存优化 (`NewRxContext`)**：
在初始化 Fast Mode 时，`NewRxContext` 会先使用 **标准分页** 临时分配一个 256 MB 的 `LightCache`（显式屏蔽 `RANDOMX_FLAG_LARGE_PAGES`，以便将操作系统的 HugePages 保留给 2.08 GB 的 `FastDataset`）。一旦 `NewFastDataset` 通过 `runtime.NumCPU() / 2` 个协程并行完成数据集填充，临时的 `LightCache` 会被 **立即释放**（`cache.Release()`），防止 ~2.34 GB 的瞬时内存峰值长期滞留在内存中。

### 1.2 零配置 HugePages 与内核 W^X 加固

`GetRecommendedFlags` 会自动启用 `randomx_get_flags() | RANDOMX_FLAG_LARGE_PAGES`（Fast Mode 下附加 `RANDOMX_FLAG_FULL_MEM`），并支持零配置的渐进式回退：

1. **HugePages 尝试与回退**：`NewLightCache`、`NewFastDataset` 以及 `createVM`（用于 2 MB 的 VM scratchpad）均会优先尝试 `RANDOMX_FLAG_LARGE_PAGES`。如果操作系统内核拒绝分配 HugePages（例如未配置 `vm.nr_hugepages` 或连续内存不足），Core-Rogue 会透明回退到标准 4 KB 分页并记录当前分页状态（`ctx.LargePages()`）。
2. **W^X 内核保护回退**：在强制执行严格 Write-XOR-Execute 策略的系统上（macOS Hardened Runtime、严格 SELinux、PaX/grsecurity），`createVM` 会捕获 JIT 分配失败（`RANDOMX_FLAG_JIT`），自动重试 `RANDOMX_FLAG_SECURE`（符合 W^X 规范的 JIT），若所有 JIT 映射均被拦截则回退到可移植解释器模式。

### 1.3 弹性有界 VM 池与原子引用计数

Go 标准库的 `sync.Pool` 不适合管理 CGo RandomX 虚拟机，因为 Go 垃圾回收器会在每次 GC 周期清理 `sync.Pool`，导致代价高昂的 C++ JIT 重编译和内存碎片。

为此，`RxContext` 实现了基于通道的 **弹性有界 VM 池**（`vmPool chan *VM`）：
- **有界容量**：`poolCap = max(4, runtime.NumCPU() + 2)`。
- **并行预热 (`PrewarmVMs`)**：在启动挖矿线程前，`PrewarmVMs(threads)` 会使用 `runtime.GOMAXPROCS(0)` 个协程并行初始化最多 `threads` 个 VM，使挖矿启动时实现零 JIT 编译延迟。
- **线程本地热路径**：挖矿工作协程在封印会话开始时通过 `AcquireVM()` 获取一个 VM 并独占整个循环，仅在找到区块或收到 `stop` 信号时通过 `ReleaseVM(vm)` 归还（单次哈希计算零锁/通道竞争）。
- **突发溢出处理**：当并发 P2P 区块头验证超过 `poolCap` 时，`AcquireVM()` 会分配临时 VM；当池已满时，`ReleaseVM()` 会立即销毁溢出的 VM（`vm.Destroy()`）。
- **内存回收**：调用 `SetThreads(<= 0)`（例如 `miner.stop()`）会触发 `PurgeAllVMs()`，销毁池中所有空闲 VM，立即将 scratchpad 内存归还操作系统。
- **防 UAF 的延迟释放 (`IncRef` / `DecRef`)**：每个调用方在 `RxContext` 上持有原子引用计数（`refs atomic.Int64`）。当纪元上下文被淘汰或退役（`Close()`）时，`retired` 在 `ctx.mu` 保护下置为 `true`，底层 CGo `dataset`/`cache` 及池化 VM 仅在 `refs` 归零时通过 `sync.Once`（`tryRelease()`）精确释放一次。

---

## 2. 纪元生命周期与防重组种子管理 (`consensus/randomx/epoch.go`)

RandomX 会定期轮换加密密钥（种子），以防止长期预计算和专用硬件优化。

### 2.1 纪元与种子常量

| 常量 | 数值 | 时长（按 30 秒/块） | 作用 |
| :--- | :--- | :--- | :--- |
| `EpochLength` | `2048` 区块 | ~17.07 小时 | 每个 RandomX 纪元的区块数量 |
| `SeedLag` | `64` 区块 | ~32 分钟 | 纪元切换前锁定下一纪元种子的提前量 |
| `CacheRetentionBlocks` | `128` 区块 | ~64 分钟 | 保留上一纪元缓存以应对链重组的窗口期 |
| `MaxCachedContexts` | `3` | — | Light Mode 下同时缓存的最大 `RxContext` 数量 |
| `MaxCachedContextsFast` | `2` | — | Fast Mode 下同时缓存的最大 `RxContext` 数量（将内存上限控制在 ~4.2 GB 以内） |
| `MaxResolvedSeedCache` | `2048` 条目 | — | 用于 `O(1)` 区块头到种子解析的有界 FIFO/LRU 缓存（~80 KB 内存） |

对于任意区块高度 $H$：
$$\text{Epoch}(H) = \left\lfloor \frac{H}{2048} \right\rfloor, \qquad \text{SeedBlock}(E) = \begin{cases} 0 & \text{若 } E = 0 \\ E \times 2048 - 64 & \text{若 } E > 0 \end{cases}$$

### 2.2 关键纪元优化

1. **后台预计算 (`PrecomputeNextEpoch`)**：在 `Prepare()` 阶段，当区块高度进入 64 区块滞后窗口（$E \times 2048 - 64 \le H < (E+1) \times 2048$）时，`EpochManager` 会启动后台协程提前初始化下一纪元的 `RxContext`。当边界区块 $E \times 2048$ 到达时，矿工与验证节点可实现 **零停顿切换**。
2. **并发去重与分配信号量**：针对同一 `(seed, fastMode)` 的并发请求会共享同一个 `inflight`（`seedInit`）通道，确保 CGo 内存分配发生在 `EpochManager.mu` 锁 **之外**（不阻塞 RPC 查询），并通过 `allocSem`（容量 `2`）限制并发分配数，防止内存耗尽峰值。
3. **分叉精确种子解析 (`ResolveSeedForHeader`)**：在验证跨越纪元边界的侧链分叉时，`ResolveSeedForHeader` 沿区块头自身的 `ParentHash` 链回溯至 `SeedBlock(E)`，并在命中 `resolvedSeeds` 时实现 $O(1)$ 短路返回。
4. **Fast Mode 节点的历史纪元轻量验证 (`GetLightContextForHeader`)**：在 Fast Mode 节点（`--randomx.fast`）上验证历史纪元的旧区块头（`Epoch(H) < Epoch(Head)`）时，会自动使用 **256 MB Light Mode 上下文**，避免分配 2.08 GB 的 `FastDataset` 或淘汰当前正在挖矿的纪元。反之，若该种子的 Fast Mode 上下文已在缓存中，Light Mode 调用方会直接复用而不会重复分配内存。

---

## 3. LWMA-1 难度调整与 MTP-11 时间规则 (`consensus/randomx/difficulty.go`)

Rogue Signal 采用针对 **30 秒** 出块目标（`DefaultTargetBlockTime = 30`）校准的 **Zawy LWMA-1**（线性加权移动平均）算法，拥有约 30 分钟的响应窗口（`DefaultLWMAWindow = 60`）。

### 3.1 数学公式

对于区块高度 $H$，令 $k = \min(60, H)$ 为有效求解间隔窗口。对于每一步 $i \in [1, k]$：
- 时间戳进行单调性归一化（`if currTime <= prevTime { currTime = prevTime + 1 }`），以中和 MTP-11 允许的乱序时间戳振荡攻击。
- 单块求解时间上限锁定为 $6 \times T = 180\text{s}$（`maxSolveTime`），防止极端时间间隔导致难度崩溃。
- 越近的区块获得越高的线性权重 $i$：

$$\text{WeightedTimes} = \sum_{i=1}^{k} i \cdot \min(\text{solvetime}_i,\; 180)$$
$$\text{AvgTarget} = \frac{1}{k} \sum_{i=1}^{k} \frac{2^{256} - 1}{\text{Difficulty}_i}$$
$$\text{NextTarget} = \frac{\text{AvgTarget} \times \text{WeightedTimes}}{\frac{k(k+1)}{2} \times 30}$$

计算得到的难度 $\text{NextDiff} = \lfloor (2^{256}-1) / \text{NextTarget} \rfloor$ 随后经过：
1. **幅值钳位（Clamping）**：限制在 $[0.67 \times \text{ParentDiff},\; 1.33 \times \text{ParentDiff}]$（`MaxClampDownFactor = 67`，`MaxClampUpFactor = 133`）之间，防范单块算力冲击攻击。
2. **最低难度下限**：`MinDifficulty = 0x10000`（65,536）。

### 3.2 候选时间戳无关性与单次遍历优化

- **候选时间戳无关性**：`CalculateDifficulty` 仅依赖于父链（`parent` 及其 $k$ 个祖先），绝不依赖候选区块自身的时间戳。矿工无法通过篡改 `header.Time` 来操纵当前区块难度。
- **单次祖先遍历 (`CalculateDifficultyAndMTP11`)**：在区块头验证和同步期间，`verifyHeader` 仅需一次向后遍历获取的 $k+1$ 个区块头，即可同时算出预期 LWMA-1 难度与 **过去中位时间（`MTP-11`，`MTPWindow = 11`）**，消除重复数据库/缓存读取。
- **严格时间边界**：所有区块 $H > 0$ 必须满足：
  $$\text{MTP}_{11}(\text{parent}) < \text{header.Time} \le \text{now} + 30\text{s}$$

---

## 4. 货币政策与精确 100M 发行上限 (`consensus/randomx/rewards.go`)

Rogue Signal 在 **32 个减半纪元**（`HalvingInterval = 2,000,000` 区块，按 30 秒/块约 1.90 年/纪元）内执行确定性的零尾部增发（zero tail emission）货币计划。

### 4.1 零截断损失的精确上限数学

对于区块号 $N \ge 1$：
$$\text{Era} = \left\lfloor \frac{N - 1}{2,000,000} \right\rfloor$$

当 $\text{Era} < 32$ 时，基础区块奖励通过整数右移计算：
$$\text{BaseReward}(\text{Era}) = \left\lfloor \frac{25 \times 10^{18}}{2^{\text{Era}}} \right\rfloor \text{ wei}$$

由于对 `25 * 10^18` wei 进行整数右移会在后期纪元引入亚 wei 级截断误差，若直接将 `BaseReward` 乘以 $2,000,000$ 并在 32 个纪元求和，总发行量会比 $100,000,000 \text{ ROGUE}$ 少 `23,283,064,376,000,000 wei`（$\approx 0.023283\text{ ROGUE}$）。

为实现 `100,000,000.000000000000000000 ROGUE` 的 **精确数学硬顶**（零截断损耗），Core-Rogue 在每个减半纪元的最后一个区块（`N % 2,000,000 == 0`）追加 `EraRemainderWei = 727,595,761,750,000 wei`：
$$32 \times 727,595,761,750,000\text{ wei} = 23,283,064,376,000,000\text{ wei}$$
当 $N > 64,000,000$（$\text{Era} \ge 32$）时，`CalculateBlockReward` 永久返回 `0`。

### 4.2 奖励分配与 Coinbase 保护

- **95% / 5% 分配 (`SplitReward`)**：`treasuryReward = (totalReward * 5) / 100`，`minerReward = totalReward - treasuryReward`（保持 wei 级总量守恒）。
- **零销毁 Coinbase 保护**：若区块的 `header.Coinbase` 为 `0x0000...0000`，`AccumulateRewards` 会将 95% 的矿工奖励与游戏化出块计数干净地路由至 `targetTreasury`（`0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`），避免意外销毁发行量。
- **严禁叔块（Uncles）**：`VerifyUncles` 与 `FinalizeAndAssemble` 严格拒绝任何叔块头（`ErrUnclesNotAllowed`），保持状态树线性增长并消除自私挖矿的叔块通胀。

---

## 5. 防 51% 最终性：ECBP-1100 (MESS) 校准 (`eth/sync.go`, `core/blockchain_af.go`)

Rogue Signal 从 **创世块（Block 0）** 起即激活 **ECBP-1100（改进指数主观评分）**（`ECBP1100FBlock = 0`），并移除 `MaxReorgDepth` 以防止永久性网络分区。

- **多项式引力惩罚**：试图进行深度重组的竞争分叉会面临指数级总难度惩罚（最高 $31\times$ 所需 TD），惩罚随分叉与共同祖先的时间跨度放大。
- **30 秒出块节奏校准 (`eth/sync.go`)**：上游 Core-Geth 针对以太坊经典的 13 秒出块校准 `artificialFinalitySafetyInterval`。Core-Rogue 将其校准为 `artificialFinalitySafetyInterval = 30 * 30 * time.Second`（**900 秒 / 15 分钟**），在 30 秒 PoW 正常泊松方差期间避免误报陈旧链警告，同时在被日蚀攻击的节点停滞超过 15 分钟时及时关闭人工最终性。
- **非单调 MTP-11 下溢防护 (`core/blockchain_af.go`)**：由于 MTP-11 允许新区块时间戳略早于某祖先时间戳（只要大于前 11 块的中位数），`ecbp1100` 中的 `current.Time - commonAncestor.Time` 可能发生 `uint64` 下溢。Core-Rogue 在 `current.Time <= commonAncestor.Time` 时显式将 `timeDelta` 钳位为 `0`，并由 `TestRogueMESSNonMonotonicMTP11` 覆盖验证。

---

## 6. 协议级矿工游戏化系统 (`consensus/randomx/gamification.go`, `params/genesis_rogue.go`)

Rogue Signal 在创世块（Block 0）直接通过 `Genesis.Alloc` 部署了纯 Solidity + OpenZeppelin v5 `Ownable` 系统合约（`RogueMinerReputation`），地址为 `0x0000000000000000000000000000000000000020`（`Nonce: 1`，存储槽 `0` 的 `Ownable._owner` 初始化为 `RogueTreasuryAddress`）。

- **共识层状态递增**：自 Block 1 起，`AccumulateRewards` 调用 `IncrementMinerBlockCount(state, minerRecipient)`，在 Solidity 映射槽 `1` 中原子递增 `_blocksMined[miner]`：
  $$\text{SlotKey}(\text{miner}) = \text{keccak256}(\text{abi.encode}(\text{miner},\; \text{uint256}(1)))$$
- **原生重组安全**：由于计数直接保存在 `StateDB` 中，链重组会自动与账户余额同步回滚和重放矿工声誉，无需外部索引器。
- **单一真实来源**：链上智能合约（`STATICCALL`）与链下应用/钱包（`eth_call`）均直接读取 `getMinerStats(address,string)`、`getAllRanks(string)`、`getMinerLevel(address)`、`meetsLevel(address,uint32)` 和 `blocksMined(address)`。

| 等级 | 赛博朋克头衔 | 已挖区块数 | 目标 XP | 状态 |
| :---: | :--- | :---: | :---: | :--- |
| **Lv.0** | **Unranked Runner** | `0` | `1` | `激活` |
| **Lv.1** | **Recon Runner** | `1 – 24` | `25` | `激活` |
| **Lv.2** | **Signal Scraper** | `25 – 99` | `100` | `激活` |
| **Lv.3** | **Hash Warden** | `100 – 499` | `500` | `激活` |
| **Lv.4** | **Cipher Architect** | `500 – 2,499` | `2,500` | `激活` |
| **Lv.5** | **Apex Operator** | `2,500 – 9,999` | `10,000` | `激活` |
| **Lv.6** | **Rogue Singularity** | `10,000+` | `MAX` | `传奇` |

