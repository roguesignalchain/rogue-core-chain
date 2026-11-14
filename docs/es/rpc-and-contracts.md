# Extensiones JSON-RPC, Flags CLI y Contratos de Sistema

> Referencia técnica completa del namespace RPC `randomx` de Core-Rogue, stubs `eth` de minería remota deshabilitados, flags CLI de Rogue Signal y el contrato de sistema `RogueMinerReputation` en el Bloque 0.

---

## 1. Namespace JSON-RPC `randomx` (`consensus/randomx/api.go`)

> [!IMPORTANT]
> Por seguridad en endpoints RPC públicos, el namespace `randomx` **no** viene habilitado por defecto en los módulos HTTP/WS (`cmd/geth/config.go`). Para habilitarlo en nodos locales o autenticados de confianza, pasa `--http.api eth,net,web3,randomx` (o `--ws.api ...`).

### `randomx_getConsensusStatus`
Devuelve en tiempo real los parámetros de consenso, telemetría de la era monetaria activa, estadísticas de memoria del pool de VMs y estado de la época.

**Ejemplo de Solicitud:**
```bash
curl -s -X POST http://127.0.0.1:38845 \
  -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"randomx_getConsensusStatus","params":[],"id":1}'
```

**Esquema de Respuesta (`ConsensusStatus`):**
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
Devuelve el índice de época RandomX actual, bloque semilla, hash canónico de semilla, próximo bloque de transición y bloques restantes.

### `randomx_getFastMode`
Devuelve `true` si RandomX Fast Mode (dataset de ~2.08 GB) está habilitado en el nodo local, o `false` para Light Mode (caché de ~256 MB). Fast Mode se configura al iniciar mediante `--randomx.fast` (`randomx_setFastMode` se omite deliberadamente para evitar picos de asignación de 2.08 GB por RPC en tiempo de ejecución).

### `randomx_getHashrate`
Devuelve el hashrate actual del minero CPU local en H/s (`uint64`).

---

## 2. Comportamiento del Namespace PoW `eth` y Reglas EVM (`consensus/randomx/api.go`)

Para garantizar una minería CPU solitaria e igualitaria dentro del nodo e impedir proxies Stratum externos o granjas puente ASIC/GPU:

| Método | Comportamiento en Rogue Signal |
| :--- | :--- |
| `eth_getWork` | **Deshabilitado** — devuelve `ErrRemoteMiningDisabled` (`remote mining (eth_getWork / eth_submitWork) is disabled on Rogue Signal: only internal node CPU mining is supported`) |
| `eth_submitWork` | **Deshabilitado** — siempre devuelve `false` |
| `eth_submitHashrate` | **Deshabilitado** — siempre devuelve `false` |
| `eth_getHashrate` | Devuelve el hashrate en vivo (`uint64`) del minero CPU interno |
| `eth_sendRawTransaction` | Exige únicamente **Tipo 0 (`LegacyTxType`)** cuando el consenso RandomX está activo; rechaza transacciones tipadas (`0x01` EIP-2930, `0x02` EIP-1559, `0x03` EIP-4844). El opcode `BASEFEE` (`0x48`) de EIP-1559 está inactivo de forma intencional. |

---

## 3. Referencia de Flags CLI de Rogue Signal (`cmd/utils/flags.go`)

| Flag | Por Defecto (con `--rogue`) | Descripción |
| :--- | :--- | :--- |
| `--rogue` | `false` | Activa Chain ID `800055`, génesis embebido, P2P `:38888`, HTTP `:38845`, WS `:38846`, `<datadir>/rogue` y `FullSync` por defecto |
| `--randomx.fast` | `false` | Habilita RandomX Fast Mode (dataset ~2.08 GB RAM). Se preserva incluso si se omite `--mine` al arrancar para que la minería iniciada por RPC/IPC use Fast Mode |
| `--syncmode` | `full` (con `--rogue`) | Modo de sincronización (`full` o `snap`). Por defecto `full` en `--rogue` para evitar descartar bloques propagados en redes jóvenes |
| `--http.maxconnsperip` | `100` | Máximo de solicitudes concurrentes HTTP/WebSocket por IP cliente (`0` desactiva). Al excederlo devuelve `HTTP 429` con `Retry-After: 1` |
| `--http.trustedproxies` | `""` (vacío, fail-closed) | IPs o CIDRs de proxies confiables separados por coma para extraer la IP real vía Rightmost Untrusted IP en `X-Forwarded-For` / `X-Real-IP` |
| `--miner.threads` | `-1` (deshabilitado hasta iniciar) | Número de hilos CPU para el minero interno RandomX |
| `--miner.etherbase` | Fallback a `RogueTreasuryAddress` | Dirección coinbase que recibe el 95% de recompensa + 100% de fees + XP de gamificación |
| `--miner.recommit` | `10s` | Intervalo para recrear el trabajo de sellado |
| `--miner.gaslimit` / `GasCeil` | `50000000` | Techo de gas por bloque alineado con `GasLimit` del Génesis |

**Variables de Entorno**:
- `ROGUE_BOOTNODES`: Lista separada por comas de URLs `enode://...` que sobrescribe `params.RogueBootnodes`.

---

## 4. Contrato de Sistema: `RogueMinerReputation` (`0x...0020`)

Desplegado de forma nativa en el Bloque 0 (`params/genesis_rogue.go`) en `0x0000000000000000000000000000000000000020` (`Nonce: 1`).

### Distribución de Almacenamiento (Storage Layout)
- **Slot `0`**: `Ownable._owner` (inicializado en génesis a `0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`). Por seguridad operacional, se recomienda transferir la propiedad a una billetera multifirma (`transferOwnership`) o llamar a `renounceOwnership()` cuando los metadatos estén finalizados.
- **Slot `1`**: `mapping(address => uint256) private _blocksMined` — actualizado directamente por consenso en `AccumulateRewards` desde el Bloque 1 usando la clave `keccak256(abi.encode(miner, uint256(1)))`.

### Interfaz EVM de Solo Lectura (`eth_call` / `STATICCALL`)

| Firma de Función | Tipo de Retorno | Descripción |
| :--- | :--- | :--- |
| `blocksMined(address miner)` | `uint256` | Bloques canónicos minados por `miner` |
| `getMinerLevel(address miner)` | `uint32` | Nivel cyberpunk actual (`0` a `6`) |
| `meetsLevel(address miner, uint32 minLevel)` | `bool` | Devuelve `true` si `miner` alcanzó `minLevel` (útil para gating on-chain) |
| `getMinerStats(address miner, string lang)` | `(uint256, uint32, string, string, uint256, uint256, uint16)` | Perfil localizado: `(blocksMined, level, title, imageURI, currentXP, nextLevelXP, progressBps)` |
| `getAllRanks(string lang)` | `RankInfo[]` | Tabla completa localizada de metadatos de rangos |

### Ejemplos de Consulta (`cast`)
```bash
# Consultar bloques minados en bruto
cast call 0x0000000000000000000000000000000000000020 \
  "blocksMined(address)(uint256)" 0xTuDireccionMinero \
  --rpc-url http://127.0.0.1:38845

# Verificar si un minero alcanza Nivel 3 (Hash Warden, >= 100 bloques)
cast call 0x0000000000000000000000000000000000000020 \
  "meetsLevel(address,uint32)(bool)" 0xTuDireccionMinero 3 \
  --rpc-url http://127.0.0.1:38845

# Obtener perfil completo localizado en español
cast call 0x0000000000000000000000000000000000000020 \
  "getMinerStats(address,string)((uint256,uint32,string,string,uint256,uint256,uint16))" \
  0xTuDireccionMinero "es" \
  --rpc-url http://127.0.0.1:38845
```

