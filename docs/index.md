---
hide:
  - navigation # Hide navigation
title: Core-Rogue (Rogue Signal)
---

# Core-Rogue Documentation

> Official Go client and node implementation for **Rogue Signal (`ROGUE`, Chain ID `800055`)** — an ASIC-resistant, CPU-first Proof-of-Work EVM blockchain powered by **RandomX (`rx/0`)**.

Built downstream from [etclabscore/core-geth](https://github.com/etclabscore/core-geth) to preserve 100% upstream EVM compatibility, Battle-Tested P2P/DB infrastructure, and long-term protocol upgradability while introducing a modular RandomX consensus engine (`consensus/randomx`), LWMA-1 difficulty retargeting, ECBP-1100 (MESS) finality, and protocol-level miner gamification.

---

## Technical Documentation Index

| Document | Scope |
| :--- | :--- |
| [**README**](../README.md) | Project overview, build instructions, node operations, and quick-start commands |
| [**Consensus & RandomX Architecture**](consensus-architecture.md) | CGo memory lifecycle, Elastic VM Pool, EpochManager, LWMA-1 + MTP-11, exact 100M emission (`EraRemainderWei`), ECBP-1100 (MESS), and `0x...0020` gamification |
| [**Protocol, Performance & Security Optimizations**](protocol-optimizations.md) | Two-phase parallel `VerifyHeaders`, zero-alloc `Seal()` hot loop, miner sealing scheduling, TxPool Type-0 hardening, reverse-proxy RPC rate limiting, and `FullSync` defaults |
| [**JSON-RPC, CLI Flags & System Contracts**](rpc-and-contracts.md) | `randomx_*` telemetry/control RPCs, disabled remote-mining `eth_*` stubs, Rogue Signal CLI flags, and `RogueMinerReputation` (`0x...0020`) ABI reference |

---

## Network Summary

| Parameter | Value |
| :--- | :--- |
| **Network Name** | Rogue Signal (`--rogue`) |
| **Chain ID / Network ID** | `800055` (`0xc3517`) |
| **Consensus** | RandomX (`rx/0`), Light Mode ~256 MB default / Fast Mode ~2.08 GB optional |
| **Block Target** | 30 seconds (`LWMA-1`, $N=60$, $\pm 33\%$ clamp, strict `MTP-11` + 30s drift) |
| **Finality** | ECBP-1100 (MESS) active from Block 0 (`15m` / 30-block safety interval) |
| **Supply Cap** | 100,000,000 ROGUE exact hard cap across 32 halving eras (zero tail emission) |
| **EVM Profile** | Cancun / Shanghai opcodes active from Block 0 (Type 0 `LegacyTx` fee market) |
| **Default Ports** | P2P `38888`, HTTP RPC `38845`, WebSocket `38846` |

---

## Security

Please do not report security vulnerabilities in public issues. Review [SECURITY.md](../SECURITY.md) for responsible disclosure procedures.
