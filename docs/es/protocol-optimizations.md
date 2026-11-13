# Optimizaciones de Protocolo, Rendimiento y Seguridad

> Referencia de ingeniería sobre el pipeline de verificación de consenso de Core-Rogue, bucle de minado sin asignaciones en heap, planificación del worker de minado, endurecimiento del TxPool, limitación de tasa RPC detrás de proxies inversos, valores por defecto de sincronización y perfil EVM.

---

## 1. Pipeline Paralelo `VerifyHeaders` en Dos Fases (`consensus/randomx/randomx.go`)

Verificar sellos RandomX es intensivo en CPU. Para evitar que lotes de cabeceras P2P inválidos o malformados desperdicien ciclos de CPU en llamadas CGo `randomx_calculate_hash`, `VerifyHeaders` implementa un **pipeline productor–consumidor en dos fases** respaldado por un `headerChainReader` en memoria (que indexa el lote por hash y número para resolver ancestros intra-lote sin consultas a disco):

1. **Fase 1 — Validación Fail-Fast en Microsegundos (Productor Secuencial)**:
   - Verifica `header.Number == parent.Number + 1`.
   - Exige `header.UncleHash == types.EmptyUncleHash` (`ErrUnclesNotAllowed`).
   - Limita `len(header.Extra) <= 64` (`MaxExtraDataSize = 64`, `ErrExtraDataTooLong`) contra ataques de inflado de cabeceras.
   - Calcula la dificultad LWMA-1 esperada y `MTP-11` en un **único recorrido de ancestros** (`CalculateDifficultyAndMTP11`), validando `header.Time > mtp`, `header.Time <= now + 30s` y `header.Difficulty == expectedDifficulty`.
   - Valida `GasLimit <= MaxGasLimit`, `GasUsed <= GasLimit` y `misc.VerifyGaslimit` (delta $\pm 1/1024$).
   - **Protección contra Maleabilidad de Block Hash**: Rechaza explícitamente campos opcionales de forks inactivos (`BaseFee`, `WithdrawalsHash`, `ExcessBlobGas`, `BlobGasUsed`, `ParentBeaconRoot` deben ser `nil`).
2. **Fase 2 — Verificación Paralela de Sellos PoW (Pool Acotado)**:
   - Solo las cabeceras que superan la Fase 1 con `seals[i] == true` se envían a un pool de goroutines acotado por `runtime.NumCPU()` (`sem`).
   - Los resultados se emiten al canal `results` en **estricto orden de índice**, soportando cancelación inmediata vía `abort`.

---

## 2. `VerifySeal` y Bucle de Minado Sin Asignaciones (`consensus/randomx/randomx.go`)

### 2.1 Pre-Chequeo Fail-Fast en `VerifySeal`

Antes de adquirir un `RxContext` o invocar CGo, `VerifySeal` convierte `DifficultyToTarget(header.Difficulty)` a un arreglo big-endian de 32 bytes (`TargetToBytes32`) y ejecuta `HashMeetsTarget(header.MixDigest, targetBE)` con **cero asignaciones en heap**:
- Si el `header.MixDigest` declarado ya supera el target, `VerifySeal` retorna `ErrInvalidPoW` inmediatamente sin tocar RandomX.
- Si el bloque pertenece a una **época pasada** (`Epoch(H) < Epoch(Head)`) en un nodo en Fast Mode, `VerifySeal` delega en `GetLightContextForHeader`, verificando el bloque en Light Mode de 256 MB en lugar de asignar un dataset de 2.08 GB.

### 2.2 Hot Loop de `Seal()` y Serialización de Sesiones

El minero CPU interno (`RandomX.Seal`) está diseñado para máximo hashrate sostenido y cero presión de memoria:

1. **Serialización Asíncrona de Sesiones (`sealMu` / `activeSealDone`)**: `Seal()` se ejecuta de forma asíncrona para no bloquear el `taskLoop` de `miner/worker.go` durante la inicialización de época o precalentamiento de VMs, permitiendo cancelación inmediata ante `stop` o nuevas cabezas de cadena. Cada nueva sesión espera a `prevDone` (salvo `stop`) para que la sesión previa libere sus VMs al pool antes de adquirir nuevas, evitando sobre-asignación transitoria en recommits rápidos.
2. **Espera Inteligente de Deriva `MTP-11`**: Si `MTP-11` empuja `header.Time` más allá de `now + MaxFutureDrift` (30s), `Seal()` espera el delta exacto (`header.Time - (now + MaxFutureDrift)`) en un temporizador cancelable antes de hashear, evitando desperdiciar CPU en bloques que los pares rechazarían con `ErrFutureBlock`.
3. **Precalentamiento Paralelo de VMs**: Invoca `ctx.PrewarmVMs(threads)` usando workers `GOMAXPROCS` antes de lanzar los hilos de minado.
4. **Hashing Thread-Local Sin Asignaciones**:
   - Cada hilo adquiere una VM para toda la sesión (`ctx.AcquireVM()` / `defer ctx.ReleaseVM(vm)`).
   - El blob de minado `rx/0` de 76 bytes (`var input [MiningBlobSize]byte`) se asigna en el stack **una sola vez** por goroutine; solo se muta el nonce little-endian de 8 bytes en el offset `39..46` (`binary.LittleEndian.PutUint64(input[39:], nonce)`).
   - Cada hilo parte desde un offset independiente sembrado criptográficamente: `baseNonce + uint64(tid)*0x100000000` (vía `crypto/rand.Reader` con fallback temporal).
   - Los contadores de hashrate se agrupan cada **64 hashes** (`attempts % 64 == 0`) para minimizar contención atómica.
   - `HashMeetsTarget(rxHash, targetBE)` compara la salida little-endian de 32 bytes contra el target big-endian in-place con **cero asignaciones `big.Int`**.

---

## 3. Planificación del Worker de Minado (`miner/miner.go`, `miner/worker.go`)

Core-Rogue ajusta el pipeline del minero específicamente para bloques RandomX cada 30 segundos:

- **Supresión de Commit Preliminar Vacío**: Cuando el `TxPool` no tiene transacciones pendientes, `worker` suprime el commit preliminar redundante de bloque vacío (`commitNewWork`), evitando interrupciones innecesarias de CPU y rotación de VMs.
- **Deduplicación Atómica por `sealhash`**: `worker.commit` registra `w.lastSealHash` y deduplica tareas consecutivas con idéntico `sealhash`, garantizando un único evento `"Commit new sealing work"` por cabeza o ciclo de recommit.
- **Valores por Defecto Alineados (`miner.DefaultConfig`)**:
  - `GasCeil = 50,000,000` (alineado con `DefaultRogueGenesisBlock().GasLimit` y `RPCGasCap`).
  - `Recommit = 10 * time.Second` (ajustado al tiempo de bloque de 30s frente a los 2s agresivos de upstream).
- **Fallback No Bloqueante de Etherbase**: Si `--miner.etherbase` no está configurado (`0x0`), `worker.etherbase()` usa `params.RogueTreasuryAddress` con una única advertencia en log, permitiendo iniciar el minado sin fricción.
- **Aborto Inmediato y Reinicio de Hashrate**: Detener el minero (`SetThreads(<= 0)`) cancela el sellado activo al instante, reinicia el rastreador EMA de hashrate (`tracker.Reset()`) y purga las VMs inactivas (`PurgeAllVMs()`).

---

## 4. Endurecimiento de TxPool y Validación (`core/txpool/legacypool/legacypool.go`, `internal/ethapi/api.go`)

Dado que Rogue Signal opera un mercado clásico de comisiones PoW sin EIP-1559 (`BaseFee` es `nil`, el 100% de las comisiones de gas van al minero), la validación se endurece de forma consistente tanto en el ingreso RPC como en el pool:

- **Exigencia Estricta de Transacciones Legacy (Solo Tipo 0)**: Cuando `chainConfig.GetConsensusEngineType().IsRandomX()` es verdadero, tanto `eth_sendRawTransaction` (`internal/ethapi/api.go`) como `legacypool.validateTxBasics` aceptan exclusivamente `types.LegacyTxType` (`0`), rechazando transacciones EIP-2930 (`Tipo 1`), EIP-1559 (`Tipo 2`) y EIP-4844 (`Tipo 3`).
- **Límites Acotados de Memoria y Anti-Spam**:
  - `GlobalSlots = 4096 + 1024` (capacidad ejecutable + flotante).
  - `txMaxSize = 128 KB` (`4 * txSlotSize`).
  - Tope de `128 MB` de RAM en TxPool y piso mínimo de gas price de `1 Gwei` (`PriceLimit`) para Rogue Signal.

---

## 5. Endurecimiento RPC HTTP/WebSocket y Proxies Inversos (`node/rpcstack.go`, `cmd/geth/config.go`, `cmd/utils/flags.go`)

Los nodos RPC públicos de Rogue Signal incorporan protección DoS nativa y soporte anti-spoofing para proxies:

1. **Límite de Concurrencia por IP (`--http.maxconnsperip`)**:
   - Por defecto: **`100`** solicitudes concurrentes HTTP/WebSocket por IP cliente (`0` desactiva).
   - Al superar el límite responde **`HTTP 429 Too Many Requests`** con cabecera **`Retry-After: 1`**.
2. **Extracción CIDR de Proxies Confiables (`--http.trustedproxies`)**:
   - **Por Defecto Fail-Closed**: Lista vacía por defecto — `X-Forwarded-For` y `X-Real-IP` se ignoran por completo salvo que `r.RemoteAddr` pertenezca a una IP o CIDR configurado en `--http.trustedproxies`.
   - **IP No Confiable Más a la Derecha (Rightmost Untrusted IP)**: Detrás de proxies multi-salto (Nginx, Caddy, Cloudflare), Core-Rogue recorre `X-Forwarded-For` de derecha a izquierda y selecciona la **primera IP más a la derecha que no pertenezca a `trustedProxies`** (con fallback a `X-Real-IP`), impidiendo la falsificación de prefijos `X-Forwarded-For`.
   - Verificado por `TestHTTPServerTrustedProxyAndRetryAfter`.
3. **Namespace `randomx` Opt-In (`cmd/geth/config.go`)**:
   - Los módulos HTTP/WS por defecto solo exponen `eth` (donde `EthAPI` expone `eth_getHashrate` de solo lectura y stubs bloqueados para `eth_getWork`/`eth_submitWork`/`eth_submitHashrate`).
   - El namespace `randomx` (que incluye `randomx_setFastMode`) es **opt-in** mediante `--http.api eth,net,web3,randomx` para evitar que atacantes remotos disparen asignaciones de `FastDataset` de 2.08 GB en endpoints públicos.

---

## 6. Modo de Sincronización y Valores por Defecto (`cmd/utils/flags.go`, `eth/backend.go`)

- **`FullSync` por Defecto para `--rogue`**: Cuando `--rogue` está activo y no se especifica `--syncmode`, Core-Rogue selecciona `downloader.FullSync` en lugar de `SnapSync`, evitando que redes jóvenes descarten bloques propagados mientras esperan un pivote de snap-sync.
- **Estado Inicial de Hilos Alineado**: `eth/backend.go` inicializa `randomx.Config{Threads: -1}` para mantener deshabilitado el minado CPU local hasta invocar `--mine` o `miner_start`.
- **Bootnodes Dinámicos**: `resolveRogueBootnodes()` lee `ROGUE_BOOTNODES` desde el entorno si está definida, usando `params.RogueBootnodes` como respaldo.
- **Puertos y Directorio de Datos Aislados**: `--rogue` fija automáticamente P2P `:38888`, HTTP RPC `:38845`, WS RPC `:38846` y `<datadir>/rogue`.

---

## 7. Perfil EVM Endurecido desde el Bloque 0 (`params/config_rogue.go`)

Rogue Signal activa un conjunto moderno y endurecido de opcodes y reglas de gas **Cancun / Shanghai** desde el **Bloque 0** (`EIP2FBlock` hasta `EIP1153FBlock` = `0`), conservando las comisiones PoW clásicas:

- **Protección contra Replay y DoS**: `EIP-155` (Chain ID `800055`), `EIP-2565` (reprecio ModExp), `EIP-2929` (acceso frío/caliente a estado), `EIP-3529` (reducción de reembolsos), `EIP-3541` (rechazo de bytecode `0xEF`), `EIP-3860` (medición y límite de initcode).
- **Opcodes Modernos**: `EIP-3651` (Warm `COINBASE`), `EIP-3855` (`PUSH0`), `EIP-5656` (`MCOPY`), `EIP-1153` (`TSTORE`/`TLOAD`), `EIP-6049` (deprecación de `SELFDESTRUCT`).
- **Bomba de Dificultad Desactivada para Siempre**: `DisposalBlock = 0` — la dificultad es gobernada exclusivamente por RandomX LWMA-1.

