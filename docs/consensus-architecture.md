# Rogue Signal Consensus & RandomX Architecture

> Deep-dive technical specification of the **Core-Rogue** consensus engine (`consensus/randomx`), CGo memory lifecycle, LWMA-1 difficulty retargeting, exact-cap monetary policy, ECBP-1100 (MESS) finality, and protocol-level miner reputation.

---

## 1. RandomX (`rx/0`) CGo Engine & Memory Lifecycle (`consensus/randomx/rx_cgo.go`)

Core-Rogue compiles the official RandomX C++ library (`librandomx.a`) statically from source (`consensus/randomx/randomx-src`) and interfaces with it via CGo. Because RandomX allocates large off-heap memory regions (~256 MB or ~2.08 GB) and JIT-compiled VM buffers, the Go wrapper is engineered for **zero Use-After-Free (UAF)**, **zero GC churn**, and **predictable memory ceilings**.

### 1.1 Execution Modes: Light Mode vs. Fast Mode

| Mode | Primary Structure | Memory Footprint | Initialization | Target Use Case |
| :--- | :--- | :--- | :--- | :--- |
| **Light Mode** *(default)* | `randomx_cache` | **~256 MB** + ~2.2 MB/VM | Single-threaded Argon2d cache init | Validators, desktop nodes, laptops, historical epoch verification |
| **Fast Mode** (`--randomx.fast` + `--mine`) | `randomx_dataset` | **~2.08 GB** + ~2.2 MB/VM | Parallelized across `runtime.NumCPU() / 2` workers | Dedicated CPU miners and high-throughput servers |

**Fast Mode Memory Optimization (`NewRxContext`)**:
When initializing Fast Mode, `NewRxContext` temporarily allocates a 256 MB `LightCache` using **standard pages** (explicitly masking off `RANDOMX_FLAG_LARGE_PAGES` for the temporary cache so OS HugePages remain reserved for the 2.08 GB `FastDataset`). Once `NewFastDataset` finishes populating the dataset in parallel across `runtime.NumCPU() / 2` goroutines, the temporary `LightCache` is **immediately released** (`cache.Release()`), preventing a ~2.34 GB transient peak from persisting in RAM.

### 1.2 Zero-Config HugePages & Kernel W^X Hardening

`GetRecommendedFlags` automatically enables `randomx_get_flags() | RANDOMX_FLAG_LARGE_PAGES` (plus `RANDOMX_FLAG_FULL_MEM` in Fast Mode) and applies progressive, zero-configuration fallbacks:

1. **HugePages Try-With-Fallback**: Both `NewLightCache`, `NewFastDataset`, and `createVM` (for the 2 MB VM scratchpad) attempt `RANDOMX_FLAG_LARGE_PAGES` first. If the OS kernel denies HugePages (e.g., `vm.nr_hugepages` not configured or insufficient contiguous memory), Core-Rogue transparently falls back to standard 4 KB pages and logs the active page mode (`ctx.LargePages()`).
2. **W^X Kernel Protection Fallback**: On systems enforcing strict Write-XOR-Execute policies (macOS Hardened Runtime, strict SELinux, PaX/grsecurity), `createVM` catches JIT allocation failures (`RANDOMX_FLAG_JIT`) and automatically retries with `RANDOMX_FLAG_SECURE` (W^X-compliant JIT) before falling back to the portable interpreter if all JIT mappings are blocked.

### 1.3 Elastic Bounded VM Pool & Atomic Reference Counting

Standard `sync.Pool` is unsuitable for CGo RandomX VMs because Go's garbage collector periodically drains `sync.Pool` on GC cycles, triggering expensive C++ JIT recompilation and memory fragmentation.

Instead, `RxContext` implements an **Elastic Bounded Channel Pool** (`vmPool chan *VM`):
- **Bounded Capacity**: `poolCap = max(4, runtime.NumCPU() + 2)`.
- **Parallel Prewarming (`PrewarmVMs`)**: Before spawning mining threads, `PrewarmVMs(threads)` initializes up to `threads` VMs in parallel across `runtime.GOMAXPROCS(0)` workers so mining starts with zero JIT compilation latency.
- **Thread-Local Hot Path**: Mining workers call `AcquireVM()` once at the start of a sealing session and hold the VM exclusively for the entire loop, releasing it via `ReleaseVM(vm)` only when the block is found or `stop` is triggered (zero channel/mutex contention per hash).
- **Burst Overflow**: If concurrent P2P header verifications exceed `poolCap`, `AcquireVM()` allocates a temporary VM; `ReleaseVM()` immediately destroys overflow VMs (`vm.Destroy()`) when the pool is full.
- **Memory Reclamation**: Calling `SetThreads(<= 0)` (e.g., `miner.stop()`) invokes `PurgeAllVMs()`, destroying all idle pooled VMs to immediately return scratchpad memory to the OS.
- **UAF-Safe Lazy Release (`IncRef` / `DecRef`)**: Every caller holds an atomic reference (`refs atomic.Int64`) on `RxContext`. When an epoch context is evicted or retired (`Close()`), `retired` is marked `true` under `ctx.mu`, and the underlying CGo `dataset`/`cache` and pooled VMs are released exactly once via `sync.Once` (`tryRelease()`) only when `refs` drops to `0`.

---

## 2. Epoch Lifecycle & Reorg-Safe Seed Management (`consensus/randomx/epoch.go`)

RandomX rotates its cryptographic key (seed) periodically to prevent long-term precomputation and specialized hardware optimization.

### 2.1 Epoch & Seed Constants

| Constant | Value | Duration (at 30s/block) | Role |
| :--- | :--- | :--- | :--- |
| `EpochLength` | `2048` blocks | ~17.07 hours | Number of blocks per RandomX epoch |
| `SeedLag` | `64` blocks | ~32 minutes | Lookback offset before epoch boundary where next seed is locked |
| `CacheRetentionBlocks` | `128` blocks | ~64 minutes | Retention window for previous epoch contexts |
| `MaxCachedContexts` | `3` | — | Maximum concurrent `RxContext` instances in Light Mode |
| `MaxCachedContextsFast` | `2` | — | Maximum concurrent `RxContext` instances in Fast Mode (caps RAM ≤ ~4.2 GB) |
| `MaxResolvedSeedCache` | `2048` entries | — | Bounded FIFO/LRU cache (~80 KB RAM) for `O(1)` header-to-seed resolution |

For any block $H$:
$$\text{Epoch}(H) = \left\lfloor \frac{H}{2048} \right\rfloor, \qquad \text{SeedBlock}(E) = \begin{cases} 0 & \text{if } E = 0 \\ E \times 2048 - 64 & \text{if } E > 0 \end{cases}$$

### 2.2 Key Epoch Optimizations

1. **Background Precomputation (`PrecomputeNextEpoch`)**: During `Prepare()`, once the chain height enters the 64-block lag window ($E \times 2048 - 64 \le H < (E+1) \times 2048$), `EpochManager` spawns a background goroutine to initialize the next epoch's `RxContext` ahead of time. When the boundary block $E \times 2048$ arrives, miners and validators switch epochs with **zero stall**.
2. **In-Flight Deduplication & Allocation Semaphore**: Concurrent requests for the same `(seed, fastMode)` share a single `inflight` `seedInit` channel, ensuring CGo allocation happens **outside** `EpochManager.mu` (so RPC queries never block) and is bounded by `allocSem` (capacity `2`) to prevent simultaneous RAM exhaustion spikes.
3. **Branch-Accurate Seed Resolution (`ResolveSeedForHeader`)**: When validating side-forks that cross an epoch boundary, `ResolveSeedForHeader` walks the header's own `ParentHash` chain back to `SeedBlock(E)`, short-circuiting in $O(1)$ whenever the header or its parent is already present in `resolvedSeeds`.
4. **Historical Epoch Verification in Light Mode (`GetLightContextForHeader`)**: On a Fast Mode node (`--randomx.fast`), verifying a historical header from an older epoch (`Epoch(H) < Epoch(Head)`) automatically uses a **256 MB Light Mode context** instead of allocating a 2.08 GB `FastDataset` or evicting the active mining epoch. Conversely, if a Fast Mode context for that seed is already cached, Light Mode callers reuse it directly without duplicating memory.

---

## 3. LWMA-1 Difficulty Retargeting & MTP-11 Time Rules (`consensus/randomx/difficulty.go`)

Rogue Signal uses **Zawy's LWMA-1** (Linearly Weighted Moving Average) calibrated for a **30-second** block target (`DefaultTargetBlockTime = 30`), giving ~30 minutes of responsive history (`DefaultLWMAWindow = 60`).

### 3.1 Mathematical Formulation

For block height $H$, let $k = \min(60, H)$ be the effective window of solve intervals. For each step $i \in [1, k]$:
- Timestamps are normalized monotonically (`if currTime <= prevTime { currTime = prevTime + 1 }`) to neutralize out-of-order timestamp oscillation attacks permitted by MTP-11.
- Individual solve times are capped at $6 \times T = 180\text{s}$ (`maxSolveTime`) to prevent extreme timestamp gaps from collapsing difficulty.
- Recent blocks receive linearly higher weight $i$:

$$\text{WeightedTimes} = \sum_{i=1}^{k} i \cdot \min(\text{solvetime}_i,\; 180)$$
$$\text{AvgTarget} = \frac{1}{k} \sum_{i=1}^{k} \frac{2^{256} - 1}{\text{Difficulty}_i}$$
$$\text{NextTarget} = \frac{\text{AvgTarget} \times \text{WeightedTimes}}{\frac{k(k+1)}{2} \times 30}$$

The resulting difficulty $\text{NextDiff} = \lfloor (2^{256}-1) / \text{NextTarget} \rfloor$ is then:
1. **Clamped** to $[0.67 \times \text{ParentDiff},\; 1.33 \times \text{ParentDiff}]$ (`MaxClampDownFactor = 67`, `MaxClampUpFactor = 133`) to prevent single-block hashrate shock attacks.
2. **Floored** at `MinDifficulty = 0x10000` (65,536).

### 3.2 Candidate-Timestamp Independence & Unified Traversal

- **Candidate-Timestamp Independence**: `CalculateDifficulty` depends solely on the parent chain (`parent` and its $k$ ancestors), never on the candidate block's own timestamp. Miners cannot manipulate their own block difficulty by shifting `header.Time`.
- **Single-Traversal Optimization (`CalculateDifficultyAndMTP11`)**: During header validation and sync, `verifyHeader` computes **both** the expected LWMA-1 difficulty and the **Past Median Time (`MTP-11`, `MTPWindow = 11`)** in a single backward ancestor walk over the $k+1$ fetched headers, eliminating duplicate database/cache lookups.
- **Strict Timestamp Bounds**: Every block $H > 0$ must satisfy:
  $$\text{MTP}_{11}(\text{parent}) < \text{header.Time} \le \text{now} + 30\text{s}$$

---

## 4. Monetary Policy & Exact 100M Supply Cap (`consensus/randomx/rewards.go`)

Rogue Signal enforces a deterministic, zero-tail-emission monetary schedule across **32 halving eras** (`HalvingInterval = 2,000,000` blocks, ~1.90 years per era at 30s/block).

### 4.1 Zero-Loss Exact Cap Mathematics

For block number $N \ge 1$:
$$\text{Era} = \left\lfloor \frac{N - 1}{2,000,000} \right\rfloor$$

For $\text{Era} < 32$, the base reward is computed via integer right-shift:
$$\text{BaseReward}(\text{Era}) = \left\lfloor \frac{25 \times 10^{18}}{2^{\text{Era}}} \right\rfloor \text{ wei}$$

Because integer right-shifting `25 * 10^18` wei introduces sub-wei truncation in later eras, multiplying `BaseReward` by $2,000,000$ across all 32 eras would fall short of $100,000,000 \text{ ROGUE}$ by exactly `23,283,064,376,000,000 wei` ($\approx 0.023283\text{ ROGUE}$).

To achieve an **exact mathematical hard cap** of `100,000,000.000000000000000000 ROGUE` with zero truncation leak, Core-Rogue adds `EraRemainderWei = 727,595,761,750,000 wei` on the final block of each halving era (`N % 2,000,000 == 0`):
$$32 \times 727,595,761,750,000\text{ wei} = 23,283,064,376,000,000\text{ wei}$$
For $N > 64,000,000$ ($\text{Era} \ge 32$), `CalculateBlockReward` returns `0`.

### 4.2 Reward Split & Coinbase Protection

- **95% / 5% Split (`SplitReward`)**: `treasuryReward = (totalReward * 5) / 100`, `minerReward = totalReward - treasuryReward` (preserving exact wei conservation).
- **Zero-Burn Coinbase Guard**: If a block's `header.Coinbase` is `0x0000...0000`, `AccumulateRewards` routes both the 95% miner reward and the gamification block credit cleanly to `targetTreasury` (`0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`) instead of burning issuance.
- **No Uncles**: `VerifyUncles` and `FinalizeAndAssemble` strictly reject any uncle headers (`ErrUnclesNotAllowed`), keeping state growth linear and eliminating selfish-mining uncle inflation.

---

## 5. Anti-51% Finality: ECBP-1100 (MESS) Calibration (`eth/sync.go`, `core/blockchain_af.go`)

Rogue Signal activates **ECBP-1100 (Modified Exponential Subjective Scoring)** from **Block 0** (`ECBP1100FBlock = 0`) and removes `MaxReorgDepth` to prevent permanent network partitions.

- **Polynomial Gravity**: Competing forks attempting deep reorganizations face an exponential total-difficulty penalty (up to $31\times$ required TD) scaled by the time span since the common ancestor.
- **30-Second Cadence Calibration (`eth/sync.go`)**: Upstream Core-Geth calibrated `artificialFinalitySafetyInterval` for Ethereum Classic's 13-second blocks. Core-Rogue calibrates `artificialFinalitySafetyInterval = 30 * 30 * time.Second` (**900 seconds / 15 minutes**), preventing false stale-chain warnings during normal 30-second PoW Poisson variance while still disabling artificial finality if an eclipsed node's head stalls beyond 15 minutes.
- **Non-Monotonic MTP-11 Underflow Guard (`core/blockchain_af.go`)**: Because MTP-11 allows a block's timestamp to be slightly earlier than an ancestor's timestamp (as long as it exceeds the median of the past 11 blocks), `current.Time - commonAncestor.Time` could underflow `uint64` in `ecbp1100`. Core-Rogue explicitly clamps `timeDelta` to `0` when `current.Time <= commonAncestor.Time`, verified by `TestRogueMESSNonMonotonicMTP11`.

---

## 6. Protocol-Level Miner Gamification (`consensus/randomx/gamification.go`, `params/genesis_rogue.go`)

Rogue Signal embeds a pure Solidity + OpenZeppelin v5 `Ownable` system contract (`RogueMinerReputation`) directly in Block 0 at `0x0000000000000000000000000000000000000020` (`Nonce: 1`, `Ownable._owner` initialized at storage slot `0` to `RogueTreasuryAddress`).

- **Consensus-Level State Increment**: Starting at Block 1, `AccumulateRewards` calls `IncrementMinerBlockCount(state, minerRecipient)`, atomically incrementing `_blocksMined[miner]` at Solidity mapping slot `1`:
  $$\text{SlotKey}(\text{miner}) = \text{keccak256}(\text{abi.encode}(\text{miner},\; \text{uint256}(1)))$$
- **Native Reorg Safety**: Because counts live in `StateDB`, chain reorganizations automatically rewind and reapply miner reputation alongside balances without external indexers.
- **Single Source of Truth**: Both on-chain smart contracts (`STATICCALL`) and off-chain dApps/wallets (`eth_call`) query `getMinerStats(address,string)`, `getAllRanks(string)`, `getMinerLevel(address)`, `meetsLevel(address,uint32)`, and `blocksMined(address)` directly.

| Level | Rank Title | Blocks Mined | Target XP | Status |
| :---: | :--- | :---: | :---: | :--- |
| **Lv.0** | **Unranked Runner** | `0` | `1` | `Active` |
| **Lv.1** | **Recon Runner** | `1 – 24` | `25` | `Active` |
| **Lv.2** | **Signal Scraper** | `25 – 99` | `100` | `Active` |
| **Lv.3** | **Hash Warden** | `100 – 499` | `500` | `Active` |
| **Lv.4** | **Cipher Architect** | `500 – 2,499` | `2,500` | `Active` |
| **Lv.5** | **Apex Operator** | `2,500 – 9,999` | `10,000` | `Active` |
| **Lv.6** | **Rogue Singularity** | `10,000+` | `MAX` | `Legendary` |

