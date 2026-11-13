---
hide:
  - navigation # Hide navigation
title: Core-Rogue (Rogue Signal)
---

# Core-Rogue 技术文档

> **Rogue Signal (`ROGUE`, Chain ID `800055`)** 官方 Go 客户端与节点实现——由 **RandomX (`rx/0`)** 驱动的抗 ASIC、CPU 优先的工作量证明（PoW）EVM 区块链。

基于 [etclabscore/core-geth](https://github.com/etclabscore/core-geth) 构建，保留 100% 上游 EVM 兼容性、久经考验的 P2P/数据库基础设施与长期协议可升级性，同时引入模块化的 RandomX 共识引擎（`consensus/randomx`）、LWMA-1 难度调整、ECBP-1100 (MESS) 最终性以及协议级矿工游戏化系统。

---

## 技术文档目录

| 文档 | 内容范围 |
| :--- | :--- |
| [**README (English)**](../../README.md) | 项目概览、源码编译指南、节点运维与快速上手命令 |
| [**共识与 RandomX 架构**](consensus-architecture.md) | CGo 内存生命周期、弹性 VM 池、`EpochManager`、LWMA-1 + MTP-11、精确 100M 发行（`EraRemainderWei`）、ECBP-1100 (MESS) 与 `0x...0020` 矿工游戏化 |
| [**协议、性能与安全优化**](protocol-optimizations.md) | 两阶段并行 `VerifyHeaders`、零分配 `Seal()` 热循环、矿工任务调度、TxPool Type-0 加固、反向代理 RPC 限流与默认 `FullSync` |
| [**JSON-RPC 扩展、CLI 参数与系统合约**](rpc-and-contracts.md) | `randomx_*` 遥测/控制 RPC、禁用的远程挖矿 `eth_*` 存根、Rogue Signal CLI 参数与 `RogueMinerReputation` (`0x...0020`) ABI 参考 |

---

## 网络参数概览

| 参数 | 数值 |
| :--- | :--- |
| **网络名称** | Rogue Signal (`--rogue`) |
| **Chain ID / Network ID** | `800055` (`0xc3517`) |
| **共识算法** | RandomX (`rx/0`)，默认 Light Mode ~256 MB / 可选 Fast Mode ~2.08 GB |
| **目标出块时间** | 30 秒（`LWMA-1`, $N=60$, $\pm 33\%$ 钳位，严格 `MTP-11` + 30 秒漂移限制） |
| **最终性机制** | 自 Block 0 起激活 ECBP-1100 (MESS)（`15m` / 30 区块安全间隔） |
| **最大供应量** | 100,000,000 ROGUE 精确硬顶，跨越 32 个减半纪元（零尾部增发） |
| **EVM 规范** | 自 Block 0 起激活 Cancun / Shanghai 操作码（Type 0 `LegacyTx` 手续费市场） |
| **默认端口** | P2P `38888`, HTTP RPC `38845`, WebSocket `38846` |

---

## 安全声明

请勿在公开 Issue 中报告安全漏洞。请参阅 [SECURITY.md](../../SECURITY.md) 了解负责任的漏洞披露流程。

