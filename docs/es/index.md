---
hide:
  - navigation # Hide navigation
title: Core-Rogue (Rogue Signal)
---

# Documentación de Core-Rogue

> Implementación oficial en Go del cliente y nodo para **Rogue Signal (`ROGUE`, Chain ID `800055`)** — una blockchain EVM Proof-of-Work resistente a ASIC, centrada en CPU e impulsada por **RandomX (`rx/0`)**.

Construido a partir de [etclabscore/core-geth](https://github.com/etclabscore/core-geth) para mantener 100% de compatibilidad EVM upstream, infraestructura P2P/DB probada en producción y capacidad de actualización a largo plazo, incorporando un motor de consenso RandomX modular (`consensus/randomx`), ajuste de dificultad LWMA-1, finalidad ECBP-1100 (MESS) y gamificación de mineros a nivel de protocolo.

---

## Índice de Documentación Técnica

| Documento | Alcance |
| :--- | :--- |
| [**Guía de Operación de Red y Nodos**](../../GUIA_OPERACION_RED_Y_NODOS.md) | Guía operativa en español: compilación, arranque, minado, monitoreo y resolución de problemas |
| [**Arquitectura de Consenso y RandomX**](consensus-architecture.md) | Ciclo de vida CGo, Pool Elástico de VMs, `EpochManager`, LWMA-1 + MTP-11, emisión exacta de 100M (`EraRemainderWei`), ECBP-1100 (MESS) y gamificación `0x...0020` |
| [**Optimizaciones de Protocolo, Rendimiento y Seguridad**](protocol-optimizations.md) | Pipeline paralelo `VerifyHeaders` en dos fases, hot loop `Seal()` sin asignaciones, planificación del minero, endurecimiento TxPool Tipo 0, rate limiting RPC detrás de proxies y `FullSync` por defecto |
| [**Extensiones JSON-RPC, Flags CLI y Contratos**](rpc-and-contracts.md) | RPCs `randomx_*`, stubs `eth_*` deshabilitados, flags CLI de Rogue Signal y referencia ABI de `RogueMinerReputation` (`0x...0020`) |

---

## Resumen de la Red

| Parámetro | Valor |
| :--- | :--- |
| **Nombre de la Red** | Rogue Signal (`--rogue`) |
| **Chain ID / Network ID** | `800055` (`0xc3517`) |
| **Consenso** | RandomX (`rx/0`), Light Mode ~256 MB por defecto / Fast Mode ~2.08 GB opcional |
| **Tiempo Objetivo de Bloque** | 30 segundos (`LWMA-1`, $N=60$, clamp $\pm 33\%$, `MTP-11` estricto + 30s de deriva) |
| **Finalidad** | ECBP-1100 (MESS) activo desde el Bloque 0 (intervalo de seguridad de `15m` / 30 bloques) |
| **Suministro Máximo** | 100,000,000 ROGUE límite duro exacto en 32 eras de halving (cero emisión residual) |
| **Perfil EVM** | Opcodes Cancun / Shanghai activos desde el Bloque 0 (mercado de fees `LegacyTx` Tipo 0) |
| **Puertos por Defecto** | P2P `38888`, HTTP RPC `38845`, WebSocket `38846` |

---

## Seguridad

No reportes vulnerabilidades de seguridad en issues públicos. Consulta [SECURITY.md](../../SECURITY.md) para el procedimiento de divulgación responsable.

