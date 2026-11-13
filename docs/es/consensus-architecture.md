# Arquitectura de Consenso y RandomX de Rogue Signal

> Especificación técnica detallada del motor de consenso de **Core-Rogue** (`consensus/randomx`), ciclo de vida de memoria CGo, ajuste de dificultad LWMA-1, política monetaria con límite exacto, finalidad ECBP-1100 (MESS) y gamificación a nivel de protocolo.

---

## 1. Motor CGo RandomX (`rx/0`) y Ciclo de Vida de Memoria (`consensus/randomx/rx_cgo.go`)

Core-Rogue compila la biblioteca oficial en C++ de RandomX (`librandomx.a`) de forma estática desde el código fuente (`consensus/randomx/randomx-src`) y se enlaza mediante CGo. Dado que RandomX asigna regiones de memoria fuera del heap de Go (~256 MB o ~2.08 GB) y buffers de VM compilados por JIT, el wrapper en Go está diseñado para garantizar **cero Use-After-Free (UAF)**, **cero presión sobre el Garbage Collector (GC)** y **límites de memoria predecibles**.

### 1.1 Modos de Ejecución: Light Mode vs. Fast Mode

| Modo | Estructura Principal | Consumo de RAM | Inicialización | Caso de Uso |
| :--- | :--- | :--- | :--- | :--- |
| **Light Mode** *(por defecto)* | `randomx_cache` | **~256 MB** + ~2.2 MB/VM | Inicialización Argon2d en un hilo | Validadores, nodos de escritorio, laptops, verificación de épocas históricas |
| **Fast Mode** (`--randomx.fast` + `--mine`) | `randomx_dataset` | **~2.08 GB** + ~2.2 MB/VM | Paralelizada entre `runtime.NumCPU() / 2` workers | Mineros CPU dedicados y servidores de alto rendimiento |

**Optimización de Memoria en Fast Mode (`NewRxContext`)**:
Al inicializar Fast Mode, `NewRxContext` asigna temporalmente una `LightCache` de 256 MB usando **páginas estándar** (desactivando explícitamente `RANDOMX_FLAG_LARGE_PAGES` para la caché temporal, de modo que las HugePages del sistema queden reservadas para el `FastDataset` de 2.08 GB). En cuanto `NewFastDataset` termina de poblar el dataset en paralelo usando `runtime.NumCPU() / 2` goroutines, la `LightCache` temporal se **libera de inmediato** (`cache.Release()`), evitando que un pico transitorio de ~2.34 GB permanezca en RAM.

### 1.2 HugePages Zero-Config y Protección para Kernels W^X

`GetRecommendedFlags` habilita automáticamente `randomx_get_flags() | RANDOMX_FLAG_LARGE_PAGES` (más `RANDOMX_FLAG_FULL_MEM` en Fast Mode) y aplica degradaciones progresivas sin configuración manual:

1. **HugePages Try-With-Fallback**: Tanto `NewLightCache`, `NewFastDataset` como `createVM` (para el scratchpad de 2 MB de la VM) intentan primero `RANDOMX_FLAG_LARGE_PAGES`. Si el kernel del SO deniega HugePages (p. ej., `vm.nr_hugepages` no configurado o memoria contigua insuficiente), Core-Rogue degrada de forma transparente a páginas estándar de 4 KB y registra el modo activo (`ctx.LargePages()`).
2. **Fallback para Kernels W^X**: En sistemas con políticas estrictas Write-XOR-Execute (macOS Hardened Runtime, SELinux estricto, PaX/grsecurity), `createVM` captura fallos de asignación JIT (`RANDOMX_FLAG_JIT`) y reintenta automáticamente con `RANDOMX_FLAG_SECURE` (JIT compatible con W^X), degradando finalmente al intérprete portable si todos los mapeos JIT están bloqueados.

### 1.3 Pool Elástico Acotado de VMs y Conteo Atómico de Referencias

El `sync.Pool` estándar de Go no es adecuado para VMs RandomX en CGo porque el recolector de basura de Go vacía `sync.Pool` periódicamente en cada ciclo de GC, provocando costosas recompilaciones JIT en C++ y fragmentación de memoria.

En su lugar, `RxContext` implementa un **Pool Elástico Acotado basado en canales** (`vmPool chan *VM`):
- **Capacidad Acotada**: `poolCap = max(4, runtime.NumCPU() + 2)`.
- **Precalentamiento Paralelo (`PrewarmVMs`)**: Antes de lanzar los hilos de minado, `PrewarmVMs(threads)` inicializa hasta `threads` VMs en paralelo usando `runtime.GOMAXPROCS(0)` workers, eliminando la latencia de compilación JIT al iniciar el minado.
- **Hot Path Thread-Local**: Cada worker de minado adquiere una VM con `AcquireVM()` al inicio de la sesión de sellado y la conserva en exclusiva durante todo el bucle, liberándola con `ReleaseVM(vm)` solo al encontrar un bloque o recibir `stop` (cero contención de mutex/canal por hash).
- **Desbordamiento en Ráfagas**: Si verificaciones P2P concurrentes superan `poolCap`, `AcquireVM()` asigna una VM temporal; `ReleaseVM()` la destruye inmediatamente (`vm.Destroy()`) si el pool ya está lleno.
- **Reclamación de Memoria**: Llamar a `SetThreads(<= 0)` (p. ej., `miner.stop()`) invoca `PurgeAllVMs()`, destruyendo todas las VMs inactivas del pool para devolver memoria scratchpad al SO de inmediato.
- **Liberación Lazy Segura contra UAF (`IncRef` / `DecRef`)**: Cada llamador mantiene una referencia atómica (`refs atomic.Int64`) sobre `RxContext`. Cuando un contexto de época es desalojado o retirado (`Close()`), `retired` se marca como `true` bajo `ctx.mu`, y la memoria CGo subyacente (`dataset`/`cache` y VMs) se libera exactamente una vez mediante `sync.Once` (`tryRelease()`) solo cuando `refs` llega a `0`.

---

## 2. Ciclo de Vida de Épocas y Gestión de Semillas Reorg-Safe (`consensus/randomx/epoch.go`)

RandomX rota su clave criptográfica (semilla) periódicamente para impedir el precomputo a largo plazo y la optimización en hardware especializado.

### 2.1 Constantes de Época y Semilla

| Constante | Valor | Duración (a 30s/bloque) | Rol |
| :--- | :--- | :--- | :--- |
| `EpochLength` | `2048` bloques | ~17.07 horas | Cantidad de bloques por época RandomX |
| `SeedLag` | `64` bloques | ~32 minutos | Ventana previa al cambio de época donde se fija la siguiente semilla |
| `CacheRetentionBlocks` | `128` bloques | ~64 minutos | Ventana de retención de contextos de la época anterior |
| `MaxCachedContexts` | `3` | — | Máximo de instancias `RxContext` simultáneas en Light Mode |
| `MaxCachedContextsFast` | `2` | — | Máximo de instancias `RxContext` en Fast Mode (limita RAM ≤ ~4.2 GB) |
| `MaxResolvedSeedCache` | `2048` entradas | — | Caché FIFO/LRU acotada (~80 KB RAM) para resolución `O(1)` cabecera-a-semilla |

Para cualquier bloque $H$:
$$\text{Epoch}(H) = \left\lfloor \frac{H}{2048} \right\rfloor, \qquad \text{SeedBlock}(E) = \begin{cases} 0 & \text{si } E = 0 \\ E \times 2048 - 64 & \text{si } E > 0 \end{cases}$$

### 2.2 Optimizaciones Clave de Época

1. **Precomputación en Segundo Plano (`PrecomputeNextEpoch`)**: Durante `Prepare()`, cuando la altura entra en la ventana de lag de 64 bloques ($E \times 2048 - 64 \le H < (E+1) \times 2048$), `EpochManager` lanza una goroutine en segundo plano para inicializar por adelantado el `RxContext` de la siguiente época. Al llegar el bloque frontera $E \times 2048$, mineros y validadores cambian de época con **cero pausas**.
2. **Deduplicación In-Flight y Semáforo de Asignación**: Las solicitudes concurrentes para el mismo `(seed, fastMode)` comparten un único canal `inflight` (`seedInit`), asegurando que la asignación CGo ocurra **fuera** de `EpochManager.mu` (sin bloquear consultas RPC) y acotada por `allocSem` (capacidad `2`) para evitar picos simultáneos de agotamiento de RAM.
3. **Resolución de Semillas Fiel a la Rama (`ResolveSeedForHeader`)**: Al validar side-forks que cruzan una frontera de época, `ResolveSeedForHeader` recorre la cadena propia de `ParentHash` de la cabecera hasta `SeedBlock(E)`, resolviendo en $O(1)$ cuando la cabecera o su padre ya están en `resolvedSeeds`.
4. **Verificación Histórica en Light Mode (`GetLightContextForHeader`)**: En un nodo en Fast Mode (`--randomx.fast`), verificar una cabecera histórica de una época anterior (`Epoch(H) < Epoch(Head)`) usa automáticamente un **contexto Light Mode de 256 MB** en lugar de asignar un `FastDataset` de 2.08 GB o desalojar la época de minado activa. A la inversa, si ya existe un contexto Fast Mode cacheado para esa semilla, los llamadores Light Mode lo reutilizan directamente sin duplicar memoria.

---

## 3. Ajuste de Dificultad LWMA-1 y Reglas Temporales MTP-11 (`consensus/randomx/difficulty.go`)

Rogue Signal utiliza **LWMA-1 de Zawy** (Linearly Weighted Moving Average) calibrado para un objetivo de **30 segundos** por bloque (`DefaultTargetBlockTime = 30`), con ~30 minutos de historial reactivo (`DefaultLWMAWindow = 60`).

### 3.1 Formulación Matemática

Para la altura $H$, sea $k = \min(60, H)$ la ventana efectiva de intervalos. Para cada paso $i \in [1, k]$:
- Los timestamps se normalizan de forma monótona (`if currTime <= prevTime { currTime = prevTime + 1 }`) para neutralizar ataques de oscilación temporal permitidos por MTP-11.
- Los tiempos de resolución individuales se acotan a $6 \times T = 180\text{s}$ (`maxSolveTime`) para impedir que huecos extremos desplomen la dificultad.
- Los bloques recientes reciben un peso linealmente mayor $i$:

$$\text{WeightedTimes} = \sum_{i=1}^{k} i \cdot \min(\text{solvetime}_i,\; 180)$$
$$\text{AvgTarget} = \frac{1}{k} \sum_{i=1}^{k} \frac{2^{256} - 1}{\text{Difficulty}_i}$$
$$\text{NextTarget} = \frac{\text{AvgTarget} \times \text{WeightedTimes}}{\frac{k(k+1)}{2} \times 30}$$

La dificultad resultante $\text{NextDiff} = \lfloor (2^{256}-1) / \text{NextTarget} \rfloor$ se somete a:
1. **Clamping**: Acotada a $[0.67 \times \text{ParentDiff},\; 1.33 \times \text{ParentDiff}]$ (`MaxClampDownFactor = 67`, `MaxClampUpFactor = 133`) para evitar ataques de choque de hashrate en un solo bloque.
2. **Piso Mínimo**: `MinDifficulty = 0x10000` (65,536).

### 3.2 Independencia del Timestamp Candidato y Recorrido Unificado

- **Independencia del Timestamp Candidato**: `CalculateDifficulty` depende exclusivamente de la cadena padre (`parent` y sus $k$ ancestros), nunca del timestamp del bloque candidato. Los mineros no pueden manipular su propia dificultad alterando `header.Time`.
- **Optimización de Recorrido Único (`CalculateDifficultyAndMTP11`)**: Durante la validación de cabeceras y sincronización, `verifyHeader` calcula **tanto** la dificultad LWMA-1 esperada como el **Past Median Time (`MTP-11`, `MTPWindow = 11`)** en un único recorrido hacia atrás sobre las $k+1$ cabeceras obtenidas, eliminando lecturas duplicadas de base de datos/caché.
- **Límites Temporales Estrictos**: Todo bloque $H > 0$ debe cumplir:
  $$\text{MTP}_{11}(\text{parent}) < \text{header.Time} \le \text{now} + 30\text{s}$$

---

## 4. Política Monetaria y Límite Exacto de 100M (`consensus/randomx/rewards.go`)

Rogue Signal aplica un calendario monetario determinista sin emisión residual (*zero tail emission*) a lo largo de **32 eras de halving** (`HalvingInterval = 2,000,000` bloques, ~1.90 años por era a 30s/bloque).

### 4.1 Matemática del Límite Exacto sin Pérdidas

Para el bloque $N \ge 1$:
$$\text{Era} = \left\lfloor \frac{N - 1}{2,000,000} \right\rfloor$$

Para $\text{Era} < 32$, la recompensa base se calcula mediante desplazamiento de bits entero:
$$\text{BaseReward}(\text{Era}) = \left\lfloor \frac{25 \times 10^{18}}{2^{\text{Era}}} \right\rfloor \text{ wei}$$

Dado que el desplazamiento entero de `25 * 10^18` wei introduce truncamientos sub-wei en eras posteriores, multiplicar `BaseReward` por $2,000,000$ en las 32 eras quedaría por debajo de $100,000,000 \text{ ROGUE}$ por exactamente `23,283,064,376,000,000 wei` ($\approx 0.023283\text{ ROGUE}$).

Para lograr un **límite duro matemático exacto** de `100,000,000.000000000000000000 ROGUE` con cero pérdida por truncamiento, Core-Rogue suma `EraRemainderWei = 727,595,761,750,000 wei` en el último bloque de cada era (`N % 2,000,000 == 0`):
$$32 \times 727,595,761,750,000\text{ wei} = 23,283,064,376,000,000\text{ wei}$$
Para $N > 64,000,000$ ($\text{Era} \ge 32$), `CalculateBlockReward` devuelve `0`.

### 4.2 Reparto de Recompensas y Protección de Coinbase

- **División 95% / 5% (`SplitReward`)**: `treasuryReward = (totalReward * 5) / 100`, `minerReward = totalReward - treasuryReward` (conservando la suma exacta en wei).
- **Guardia contra Quema de Coinbase**: Si `header.Coinbase` es `0x0000...0000`, `AccumulateRewards` redirige limpiamente tanto el 95% del minero como el contador de gamificación hacia `targetTreasury` (`0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03`) sin quemar emisión.
- **Sin Uncles**: `VerifyUncles` y `FinalizeAndAssemble` rechazan estrictamente cualquier bloque uncle (`ErrUnclesNotAllowed`), manteniendo lineal el crecimiento del estado y eliminando la inflación por *selfish mining*.

---

## 5. Finalidad Anti-51%: Calibración ECBP-1100 (MESS) (`eth/sync.go`, `core/blockchain_af.go`)

Rogue Signal activa **ECBP-1100 (Modified Exponential Subjective Scoring)** desde el **Bloque 0** (`ECBP1100FBlock = 0`) y elimina `MaxReorgDepth` para evitar particiones permanentes de red.

- **Gravedad Polinómica**: Las bifurcaciones competidoras que intentan reorganizaciones profundas enfrentan una penalización exponencial de dificultad total (hasta $31\times$ TD requerido) escalada según el tiempo transcurrido desde el ancestro común.
- **Calibración a Cadencia de 30 Segundos (`eth/sync.go`)**: Core-Geth upstream calibraba `artificialFinalitySafetyInterval` para bloques de 13 segundos. Core-Rogue calibra `artificialFinalitySafetyInterval = 30 * 30 * time.Second` (**900 segundos / 15 minutos**), evitando falsas advertencias de cadena obsoleta durante la varianza normal de Poisson de 30s, sin dejar de desactivar la finalidad artificial si un nodo eclipsado se estanca más de 15 minutos.
- **Guardia contra Underflow bajo MTP-11 No Monótono (`core/blockchain_af.go`)**: Dado que MTP-11 permite que el timestamp de un bloque sea ligeramente menor que el de un ancestro (siempre que supere la mediana de los 11 anteriores), `current.Time - commonAncestor.Time` podía sufrir underflow `uint64` en `ecbp1100`. Core-Rogue fija `timeDelta` a `0` cuando `current.Time <= commonAncestor.Time`, verificado por `TestRogueMESSNonMonotonicMTP11`.

---

## 6. Gamificación de Mineros a Nivel de Protocolo (`consensus/randomx/gamification.go`, `params/genesis_rogue.go`)

Rogue Signal despliega un contrato de sistema en Solidity puro + OpenZeppelin v5 `Ownable` (`RogueMinerReputation`) directamente en el Bloque 0 en `0x0000000000000000000000000000000000000020` (`Nonce: 1`, `Ownable._owner` inicializado en el slot `0` con `RogueTreasuryAddress`).

- **Incremento en Consenso**: Desde el Bloque 1, `AccumulateRewards` llama a `IncrementMinerBlockCount(state, minerRecipient)`, incrementando atómicamente `_blocksMined[miner]` en el slot `1` de Solidity:
  $$\text{SlotKey}(\text{miner}) = \text{keccak256}(\text{abi.encode}(\text{miner},\; \text{uint256}(1)))$$
- **Seguridad Nativa ante Reorgs**: Al residir en `StateDB`, las reorganizaciones de cadena revierten y reaplican la reputación junto con los saldos sin indexadores externos.
- **Fuente Única de Verdad**: Tanto contratos inteligentes (`STATICCALL`) como clientes/dApps (`eth_call`) consultan directamente `getMinerStats(address,string)`, `getAllRanks(string)`, `getMinerLevel(address)`, `meetsLevel(address,uint32)` y `blocksMined(address)`.

| Nivel | Título de Rango | Bloques Minados | XP Objetivo | Estado |
| :---: | :--- | :---: | :---: | :--- |
| **Lv.0** | **Unranked Runner** | `0` | `1` | `Activo` |
| **Lv.1** | **Recon Runner** | `1 – 24` | `25` | `Activo` |
| **Lv.2** | **Signal Scraper** | `25 – 99` | `100` | `Activo` |
| **Lv.3** | **Hash Warden** | `100 – 499` | `500` | `Activo` |
| **Lv.4** | **Cipher Architect** | `500 – 2,499` | `2,500` | `Activo` |
| **Lv.5** | **Apex Operator** | `2,500 – 9,999` | `10,000` | `Activo` |
| **Lv.6** | **Rogue Singularity** | `10,000+` | `MAX` | `Legendario` |

