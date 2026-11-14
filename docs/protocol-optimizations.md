# Protocol, Performance & Security Optimizations

> Engineering reference covering Core-Rogue's consensus verification pipeline, zero-allocation mining hot loop, miner worker scheduling, transaction pool hardening, reverse-proxy-aware RPC rate limiting, sync defaults, and EVM profile.

---

## 1. Two-Phase Fail-Fast Parallel `VerifyHeaders` (`consensus/randomx/randomx.go`)

Verifying RandomX seals is CPU-bound. To prevent invalid or malformed P2P header batches from wasting CPU cycles on CGo `randomx_calculate_hash` calls, `VerifyHeaders` implements a **two-phase producer–consumer pipeline** backed by an in-memory `headerChainReader` (indexing the batch by hash and number so intra-batch ancestors resolve without DB roundtrips):

1. **Phase 1 — Microsecond Fail-Fast Validation (Sequential Producer)**:
   - Checks `header.Number == parent.Number + 1`.
   - Enforces `header.UncleHash == types.EmptyUncleHash` (`ErrUnclesNotAllowed`).
   - Enforces `len(header.Extra) <= 64` (`MaxExtraDataSize = 64`, `ErrExtraDataTooLong`) to block header-bloat attacks.
   - Computes expected LWMA-1 difficulty and `MTP-11` in a **single ancestor traversal** (`CalculateDifficultyAndMTP11`), then enforces `header.Time > mtp`, `header.Time <= now + 30s`, and `header.Difficulty == expectedDifficulty`.
   - Validates `GasLimit <= MaxGasLimit`, `GasUsed <= GasLimit`, and `misc.VerifyGaslimit` ($\pm 1/1024$ delta).
   - **Block-Hash Malleability Guard**: Explicitly rejects inactive-fork optional header fields (`BaseFee`, `WithdrawalsHash`, `ExcessBlobGas`, `BlobGasUsed`, `ParentBeaconRoot` must all be `nil`).
2. **Phase 2 — Parallel PoW Seal Verification (Bounded Worker Pool)**:
   - Only headers passing Phase 1 with `seals[i] == true` are dispatched to a `runtime.NumCPU()`-bounded goroutine pool (`sem`).
   - Results are emitted to the caller's `results` channel in **strict header index order** while supporting instant cancellation via `abort`.

---

## 2. Zero-Allocation `VerifySeal` & Mining Hot Loop (`consensus/randomx/randomx.go`)

### 2.1 Fail-Fast `VerifySeal` Target Pre-Check

Before acquiring an `RxContext` or invoking CGo, `VerifySeal` converts `DifficultyToTarget(header.Difficulty)` to a 32-byte big-endian array (`TargetToBytes32`) and executes `HashMeetsTarget(header.MixDigest, targetBE)` with **zero heap allocations**:
- If the claimed `header.MixDigest` already exceeds the target, `VerifySeal` immediately returns `ErrInvalidPoW` without touching RandomX.
- If the block belongs to a **past epoch** (`Epoch(H) < Epoch(Head)`) on a Fast Mode node, `VerifySeal` routes to `GetLightContextForHeader`, verifying the block in 256 MB Light Mode rather than allocating a 2.08 GB dataset.

### 2.2 `Seal()` Hot-Loop & Session Serialization

The internal CPU miner (`RandomX.Seal`) is engineered for maximum sustained hashrate and zero memory churn:

1. **Asynchronous Session Serialization (`sealMu` / `activeSealDone`)**: `Seal()` runs asynchronously so it never blocks `miner/worker.go`'s `taskLoop` on epoch initialization or VM prewarming, allowing instant cancellation on `stop` or new chain heads. Each new `Seal()` session waits for `prevDone` (unless `stop` fires first) so the previous session releases its pooled VMs before the new session acquires them, preventing transient VM pool over-allocation on rapid recommits.
2. **MTP-11 Drift Pre-Sleep**: If `MTP-11` forces `header.Time` beyond `now + MaxFutureDrift` (30s), `Seal()` waits out the exact delta (`header.Time - (now + MaxFutureDrift)`) on a cancellable timer before hashing, avoiding wasted CPU cycles on blocks peers would reject as `ErrFutureBlock`.
3. **Parallel VM Prewarming**: Calls `ctx.PrewarmVMs(threads)` using `GOMAXPROCS` workers prior to launching mining threads.
4. **Zero-Alloc Thread-Local Hashing**:
   - Each thread acquires one VM for the entire session (`ctx.AcquireVM()` / `defer ctx.ReleaseVM(vm)`).
   - The 76-byte `rx/0` mining blob (`var input [MiningBlobSize]byte`) is stack-allocated **once** per worker goroutine; only the 8-byte little-endian nonce at offset `39..46` (`binary.LittleEndian.PutUint64(input[39:], nonce)`) is mutated per iteration.
   - Each thread starts at an independent cryptographically seeded offset: `baseNonce + uint64(tid)*0x100000000` (using `crypto/rand.Reader` with a time-based fallback).
   - Hashrate counters are batched every **64 hashes** (`attempts % 64 == 0`) to minimize atomic contention.
   - `HashMeetsTarget(rxHash, targetBE)` compares the 32-byte little-endian RandomX output against the big-endian target in place with **zero `big.Int` allocations**.

---

## 3. Miner Worker Scheduling & Usability (`miner/miner.go`, `miner/worker.go`)

Core-Rogue tunes the upstream miner pipeline specifically for a 30-second RandomX PoW cadence:

- **Empty-Pool Preliminary Commit Suppression**: When the `TxPool` has zero pending transactions, `worker` suppresses the redundant preliminary empty block commit (`commitNewWork`), preventing unnecessary CPU sealing interrupts and VM pool churn.
- **Atomic `sealhash` Deduplication**: `worker.commit` tracks `w.lastSealHash` and deduplicates consecutive sealing tasks with identical `sealhash`, guaranteeing a single atomic `"Commit new sealing work"` event per new head or recommit cycle.
- **Aligned Defaults (`miner.DefaultConfig`)**:
  - `GasCeil = 50,000,000` (aligned with `DefaultRogueGenesisBlock().GasLimit` and `RPCGasCap`).
  - `Recommit = 10 * time.Second` (tuned for 30s block time instead of upstream's aggressive 2s).
- **Explicit Etherbase Enforcement**: If `--miner.etherbase` is unset (`0x0`), `StartMining` and `worker.commitWork` fail/refuse block assembly explicitly so operators never spend CPU sealing rewards to an unintended address.
- **Immediate Abort & Hashrate Reset**: Stopping the miner (`SetThreads(<= 0)`) immediately cancels active sealing, resets the EMA hashrate tracker (`tracker.Reset()`), and purges idle RandomX VMs (`PurgeAllVMs()`).

---

## 4. Transaction Pool & Validation Hardening (`core/txpool/legacypool/legacypool.go`, `internal/ethapi/api.go`)

Because Rogue Signal operates a classical PoW fee market without EIP-1559 (`BaseFee` is `nil`, 100% of transaction gas fees go to the miner), transaction validation is hardened consistently at both RPC ingress and pool admission:

- **Strict Legacy Transaction Enforcement (Type 0 Only)**: When `chainConfig.GetConsensusEngineType().IsRandomX()` is true, both `eth_sendRawTransaction` (`internal/ethapi/api.go`) and `legacypool.validateTxBasics` strictly accept only `types.LegacyTxType` (`0`), rejecting EIP-2930 (`Type 1`), EIP-1559 (`Type 2`), and EIP-4844 (`Type 3`) transactions.
- **Bounded Memory & Anti-Spam Limits**:
  - `GlobalSlots = 4096 + 1024` (executable + floating capacity).
  - `txMaxSize = 128 KB` (`4 * txSlotSize`).
  - `128 MB` TxPool RAM cap and `1 Gwei` minimum gas price floor (`PriceLimit`) for Rogue Signal.

---

## 5. HTTP / WebSocket RPC & Reverse Proxy Hardening (`node/rpcstack.go`, `cmd/geth/config.go`, `cmd/utils/flags.go`)

Public Rogue Signal RPC nodes include built-in DoS protection and anti-spoofing proxy support:

1. **Per-IP Concurrency Rate Limiting (`--http.maxconnsperip`)**:
   - Default: **`100`** concurrent HTTP/WebSocket requests per client IP (`0` disables).
   - Exceeding the limit returns **`HTTP 429 Too Many Requests`** with a standard **`Retry-After: 1`** header.
2. **Trusted Proxy CIDR Extraction (`--http.trustedproxies`)**:
   - **Fail-Closed Default**: Empty allowlist by default — `X-Forwarded-For` and `X-Real-IP` headers are completely ignored unless `r.RemoteAddr` matches a configured IP or CIDR in `--http.trustedproxies`.
   - **Anti-Spoofing Rightmost Untrusted IP**: When behind multi-hop proxies (Nginx, Caddy, Cloudflare), Core-Rogue parses `X-Forwarded-For` from right to left and selects the **rightmost IP not belonging to `trustedProxies`** (with `X-Real-IP` fallback), preventing clients from spoofing arbitrary `X-Forwarded-For` prefixes.
   - Tested by `TestHTTPServerTrustedProxyAndRetryAfter` (covering spoofing, multi-hop XFF, `X-Real-IP`, WebSocket upgrades, and `limit=0`).
3. **Opt-In Read-Only `randomx` RPC Namespace (`cmd/geth/config.go`)**:
   - Default HTTP/WS modules only expose `eth` (where `EthAPI` exposes read-only `eth_getHashrate` and disabled stubs for `eth_getWork`/`eth_submitWork`/`eth_submitHashrate`).
   - The `randomx` namespace (`randomx_getConsensusStatus`, `randomx_getEpochInfo`, `randomx_getFastMode`, `randomx_getHashrate`) is **opt-in** via `--http.api eth,net,web3,randomx` and contains no mutating mode switches (Fast Mode is startup-only via `--randomx.fast`).

---

## 6. Sync Mode, Initialization & Network Defaults (`cmd/utils/flags.go`, `eth/backend.go`)

- **Default `FullSync` for `--rogue`**: When `--rogue` is active and `--syncmode` is not explicitly provided, Core-Rogue defaults to `downloader.FullSync` instead of `SnapSync`. This ensures young PoW networks do not discard newly propagated blocks while waiting for a snap-sync pivot block.
- **Aligned Default Thread State**: `eth/backend.go` initializes `randomx.Config{Threads: -1}` so local CPU mining remains disabled until `--mine` or `miner_start` is explicitly invoked.
- **Dynamic Bootnodes**: `resolveRogueBootnodes()` reads `ROGUE_BOOTNODES` from the environment if set, falling back to `params.RogueBootnodes`.
- **Isolated Network Ports & Data Directory**: Passing `--rogue` automatically sets P2P `:38888`, HTTP RPC `:38845`, WS RPC `:38846`, and `<datadir>/rogue`.

---

## 7. Hardened EVM Profile from Block 0 (`params/config_rogue.go`)

Rogue Signal activates a modern, security-hardened **Cancun / Shanghai** EVM opcode and gas schedule directly from **Block 0** (`EIP2FBlock` through `EIP1153FBlock` = `0`), while keeping classical PoW fee mechanics:

- **Replay & DoS Protection**: `EIP-155` (Chain ID `800055`), `EIP-2565` (ModExp repricing), `EIP-2929` (cold/warm state access), `EIP-3529` (refund reduction), `EIP-3541` (`0xEF` bytecode rejection), `EIP-3860` (initcode metering & size cap).
- **Modern Developer Opcodes**: `EIP-3651` (Warm `COINBASE`), `EIP-3855` (`PUSH0`), `EIP-5656` (`MCOPY`), `EIP-1153` (`TSTORE`/`TLOAD` transient storage), `EIP-6049` (`SELFDESTRUCT` deprecation).
- **Difficulty Bomb Defused Forever**: `DisposalBlock = 0` — difficulty is governed exclusively by RandomX LWMA-1.

