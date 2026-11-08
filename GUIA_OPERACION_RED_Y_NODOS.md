# 📘 Guía Definitiva de Operación de Red, Nodos y Cliente de Escritorio
## Rogue Signal (ROGUE) — Red Blockchain RandomX PoW

---

## 📋 1. Estado y Verificación Inicial del Sistema

Antes de iniciar cualquier proceso, se verificó el estado del entorno:
- **Procesos activos de red**: **Ninguno**. No hay instancias en ejecución de `geth`, `core-rogue` ni del cliente de escritorio (`tauri`/`vite`).
- **Puertos de red**: **Completamente limpios**. Los puertos estándar `38888` (P2P), `38845` (HTTP RPC), `38846` (WS RPC) y `1420` (Vite) se encuentran libres.

---

## ⚙️ 2. Constantes y Parámetros Oficiales de la Red

| Parámetro | Valor Oficial | Descripción |
| :--- | :--- | :--- |
| **Nombre de Red** | `Rogue Signal` | Red principal de pruebas y producción |
| **Ticker** | `ROGUE` | Moneda nativa con 18 decimales |
| **Chain ID** | `800055` | Identificador único EVM (EIP-155) |
| **Algoritmo PoW** | `RandomX` (`rx/0`) | Minería resistente a ASICs/GPUs exclusiva para CPU casera |
| **Modo de Memoria** | `Light Mode (~256 MB)` | Por defecto siempre; opción `Fast Mode (~2.08 GB)` |
| **Tiempo de Bloque** | 10 segundos | Frecuencia objetivo de emisión |
| **Emisión por Bloque** | 8 ROGUE | Halving cada 6,300,000 bloques (~2 años) |
| **Reparto de Minado** | 95% Minero / 5% Tesorería | 7.6 ROGUE al minero, 0.4 ROGUE a la tesorería |
| **Billetera Tesorería**| `0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03` | Recibe el 5% en cada bloque |
| **Suministro Máximo** | 100,000,000 ROGUE | Hard cap estricto, sin emisión de cola |
| **Dificultad (DAA)** | LWMA-1 ($N=60$) | Ajuste lineal ponderado por bloque |
| **Reglas de Tiempo** | MTP-11 estricto | Límite de desvío futuro de 15 segundos |
| **Rotación de Semilla** | Cada 2048 bloques | Semilla RandomX con lag de 64 bloques |
| **Puerto P2P por defecto** | `38888` | Protocolo devp2p eth/68 |
| **Puerto HTTP RPC** | `38845` | API JSON-RPC |
| **Puerto WS RPC** | `38846` | WebSocket JSON-RPC |
| **Minería Remota** | **Deshabilitada** | `eth_getWork` y `eth_submitWork` bloqueados contra rigs/pools |

---

## 🛠️ 3. Ubicación y Verificación de Binarios Compilados

El núcleo de la red cuenta con sus ejecutables nativos precompilados en el directorio `rogue-core-chain/build/bin/`:
- `core-rogue` o `geth`: Nodo completo Core-Geth con motor de consenso RandomX integrado y reglas EVM Cancun.

> [!NOTE]
> Si en algún momento necesitas recompilar el binario desde el código fuente:
> ```bash
> cd /media/carlos/DATA1/DEV/blockchain_pow/rogue-core-chain
> make core-rogue
> ```

---

## 🚀 4. Paso a Paso: Despliegue de la Red Inicial (Nodo 1 / Bootstrap)

Para arrancar la red desde cero, inicializaremos el primer nodo (Nodo Génesis / Bootstrap) que servirá como ancla para el resto de la red.

### Paso 4.1: Crear el Directorio de Datos e Inicializar el Bloque Génesis

Nos posicionamos en la carpeta raíz del proyecto y creamos una carpeta para los datos del Nodo 1:

```bash
cd /media/carlos/DATA1/DEV/blockchain_pow

# 1. Crear directorio para el nodo inicial
mkdir -p data-node1

# 2. Inicializar la base de datos con el genesis.json
./rogue-core-chain/build/bin/geth --datadir ./data-node1 init genesis.json
```

Verás una salida similar a:
```text
INFO Writing custom genesis block
INFO Wrote genesis block OK config="Chain ID: 800055 (rogue-signal) Consensus: RandomX rx/0"
INFO Successfully wrote genesis state database=chaindata hash=...
```

### Paso 4.2: Iniciar el Nodo 1 por Consola

Ejecuta el siguiente comando para poner en marcha el Nodo 1:

```bash
./rogue-core-chain/build/bin/geth \
  --rogue \
  --datadir ./data-node1 \
  --port 38888 \
  --http \
  --http.addr 0.0.0.0 \
  --http.port 38845 \
  --http.api "eth,net,web3,miner,personal,txpool,admin" \
  --http.corsdomain "*" \
  --ws \
  --ws.addr 0.0.0.0 \
  --ws.port 38846 \
  --ws.api "eth,net,web3,miner,personal,admin" \
  --ws.origins "*"
```

> [!TIP]
> **Modo Minado Directo en Nodo 1:**
> Si quieres que este nodo comience a minar inmediatamente con la CPU, agrega los flags `--mine`, `--miner.threads` y tu billetera receptora (`--miner.etherbase`):
> ```bash
> ./rogue-core-chain/build/bin/geth \
>   --rogue \
>   --datadir ./data-node1 \
>   --port 38888 \
>   --http \
>   --http.addr 0.0.0.0 \
>   --http.port 38845 \
>   --http.api "eth,net,web3,miner,personal,txpool,admin" \
>   --http.corsdomain "*" \
>   --ws \
>   --ws.addr 0.0.0.0 \
>   --ws.port 38846 \
>   --ws.api "eth,net,web3,miner,personal,admin" \
>   --ws.origins "*" \
>   --mine \
>   --miner.threads 2 \
>   --miner.etherbase 0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03
> ```
> * **Memoria Ligera (por defecto)**: Consume ~256 MB de RAM para inicializar el caché RandomX.
> * **Memoria Rápida (`--randomx.fast`)**: Agrega este flag si cuentas con más de 3 GB de RAM disponibles; inicializa el dataset completo de 2.08 GB y multiplica el rendimiento de hasheo por ~4x.

---

## 🔗 5. Paso a Paso: Conectar un Segundo Nodo (Nodo 2 / Peer)

Para validar la sincronización P2P en la misma máquina o en otra máquina de la red local:

### Paso 5.1: Obtener la Dirección `enode` del Nodo 1

Abre una nueva terminal y conéctate a la consola interactiva del Nodo 1:

```bash
./rogue-core-chain/build/bin/geth attach --exec "admin.nodeInfo.enode" ./data-node1/geth.ipc
```
Esto retornará una cadena con formato:
`"enode://<clave-publica-nodo-1>@127.0.0.1:38888"`

*(Copia esta cadena completa).*

### Paso 5.2: Inicializar y Levantar el Nodo 2 en Puertos Alternos

En otra pestaña de consola:

```bash
cd /media/carlos/DATA1/DEV/blockchain_pow

# 1. Crear directorio para el Nodo 2
mkdir -p data-node2

# 2. Inicializar con el mismo archivo génesis
./rogue-core-chain/build/bin/geth --datadir ./data-node2 init genesis.json

# 3. Iniciar Nodo 2 especificando el Nodo 1 como bootnode y usando puertos alternos
./rogue-core-chain/build/bin/geth \
  --rogue \
  --datadir ./data-node2 \
  --port 38889 \
  --authrpc.port 8552 \
  --http \
  --http.addr 127.0.0.1 \
  --http.port 38847 \
  --http.api "eth,net,web3,miner,personal,txpool,admin" \
  --http.corsdomain "*" \
  --ws \
  --ws.port 38848 \
  --ws.origins "*" \
  --bootnodes "ENODE_DEL_NODO_1_AQUI"
```

### Paso 5.3: Conectar Peers Manualmente en Caliente (Opcional)

Si arrancaste el Nodo 2 sin `--bootnodes`, puedes enlazar los nodos en cualquier momento desde la consola interactiva del Nodo 2:

```bash
./rogue-core-chain/build/bin/geth attach ./data-node2/geth.ipc
```
Dentro de la consola de JavaScript:
```javascript
admin.addPeer("enode://<clave-publica-nodo-1>@127.0.0.1:38888")
net.peerCount // Debe devolver 1
admin.peers   // Muestra la información de conexión del peer
```

---

## 💻 6. Uso de la Consola Interactiva de Geth (CLI)

Para consultar el estado de la red, saldos y bloques mediante la línea de comandos, utiliza `geth attach`:

```bash
# Conectar por socket local IPC:
./rogue-core-chain/build/bin/geth attach ./data-node1/geth.ipc

# O conectar por HTTP RPC:
./rogue-core-chain/build/bin/geth attach http://127.0.0.1:38845
```

### Comandos más útiles:

```javascript
// Altura actual de la cadena (número de bloques minados)
eth.blockNumber

// Consultar el último bloque minado
eth.getBlock("latest")

// Consultar saldo de una cuenta (en Wei y en ROGUE)
eth.getBalance("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03")
web3.fromWei(eth.getBalance("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03"), "ether")

// Iniciar y detener la minería en caliente
miner.setEtherbase("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03")
miner.start(2) // Minar con 2 hilos de CPU
miner.stop()   // Detener la minería

// Ver estado del pool de transacciones
txpool.status

// Ver cantidad de nodos conectados
net.peerCount
```

---

## 🖥️ 7. Paso a Paso: Iniciar el Cliente de Escritorio (Rogue Signal Desktop)

El cliente de escritorio está construido con **Tauri v2**, **Rust**, **React 19**, **TypeScript** y **Tailwind CSS**. Administra automáticamente el nodo local como subproceso supervisado y ofrece control total de minería doméstica por CPU.

### Paso 7.1: Requisitos de Sistema (Ubuntu/Debian)

Si es la primera vez que se ejecuta en la máquina, asegúrate de tener las dependencias nativas instaladas:

```bash
sudo apt update && sudo apt install -y \
  libwebkit2gtk-4.1-dev \
  build-essential \
  curl \
  wget \
  file \
  libssl-dev \
  libayatana-appindicator3-dev \
  librsvg2-dev
```

### Paso 7.2: Ejecución en Modo Desarrollo (Hot-Reload)

Para lanzar la aplicación de escritorio en modo desarrollo:

```bash
cd /media/carlos/DATA1/DEV/blockchain_pow/rogue-desktop-client

# Iniciar la interfaz gráfica de Tauri v2
npm run tauri dev
```

* Tauri compilará el backend en Rust (`src-tauri/`), iniciará el servidor web Vite en el puerto `1420` y abrirá la ventana gráfica nativa de Rogue Signal Desktop.

> [!NOTE]
> Si prefieres probar únicamente la interfaz web en tu navegador sin abrir la ventana nativa de Tauri:
> ```bash
> npm run dev
> ```
> Y abre en tu navegador: `http://localhost:1420`.

### Paso 7.3: Compilar el Instalador de Producción (`.deb` / `.AppImage`)

Para empaquetar la aplicación de escritorio final lista para distribución:

```bash
cd /media/carlos/DATA1/DEV/blockchain_pow/rogue-desktop-client
npm run tauri build
```
Los archivos compilados quedarán generados en:
`src-tauri/target/release/bundle/deb/` y `src-tauri/target/release/bundle/appimage/`.

---

## 🕹️ 8. Flujo de Trabajo en el Cliente de Escritorio

Una vez abierta la aplicación gráfica:

1. **Creación o Desbloqueo del Vault Criptográfico**:
   - En la primera apertura, genera una frase semilla de 12 palabras (BIP-39).
   - Define tu contraseña maestra. El backend de Rust derivará una clave de cifrado mediante **Argon2id** y protegerá tu frase secreta con **AES-256-GCM**.
   - Se creará automáticamente tu primera dirección EVM (`m/44'/60'/0'/0/0`).
2. **Pestaña "Dashboard" y Supervisión Automática del Nodo Local**:
   - Al abrir el cliente, Tauri inicia automáticamente el binario `rogue-core-chain/build/bin/geth --rogue` como subproceso supervisado en modo `full` sync.
3. **Pestaña "Minería CPU" (Solo Mining para Computadores Caseros)**:
   - Haz clic en **Verificar Recursos del Sistema** para inspeccionar cuántos núcleos lógicos y cuánta memoria RAM libre tiene tu computador.
   - Selecciona un perfil seguro de uso de CPU: **Bajo Impacto (25%)**, **Equilibrado (50%)** o **Máxima Potencia (75%)**, o ajusta manualmente los hilos.
   - El modo de memoria predeterminado es siempre **Light Mode (~256 MB RAM)** para cuidar tu equipo; opcionalmente puedes alternar a **Fast Mode (~2.08 GB RAM)** si cuentas con más de 3 GB de RAM libre.
   - Al presionar **Iniciar Minería en Solitario**, el cliente sincroniza automáticamente tu dirección activa de billetera (`miner_setEtherbase`) y activa el minero interno (`miner_start`).
4. **Pestaña "Wallet" (Transacciones y Saldos)**:
   - Consulta tu saldo en tiempo real en ROGUE y gestiona múltiples billeteras y categorías de direcciones.
   - Envía transferencias firmadas 100% de manera local y aislada en Rust.
5. **Pestaña "Settings" (Configuración y Diagnóstico de Hardware)**:
   - Incluye también el botón **Verificar Recursos del Sistema** para ajustar el límite de caché RAM y parámetros del nodo sin riesgo.

---

## ⚠️ 9. Solución de Problemas Frecuentes (Troubleshooting)

### Error: `listen tcp :38888: bind: address already in use`
- **Causa**: Ya hay una instancia de geth corriendo en segundo plano o el puerto no se liberó.
- **Solución**:
  ```bash
  killall -9 geth core-rogue
  ```

### Rendimiento bajo en minería o error de memoria RandomX Fast Mode
- **Causa**: El modo `--randomx.fast` requiere 2.1 GB de RAM continuos. Si tu equipo tiene recursos limitados, el proceso podría ralentizarse o cerrarse por falta de memoria.
- **Solución**: Desactiva el Fast Mode (ejecuta en Light Mode, que solo requiere ~256 MB de RAM).

### Los nodos no se conectan entre sí (`net.peerCount == 0`)
- **Causa**: Ambos nodos deben tener exactamente el mismo `genesis.json` inicializado. Si el hash del bloque 0 no coincide, los nodos rechazarán el apretón de manos (handshake).
- **Solución**:
  1. Verifica que ambos se hayan inicializado con `init genesis.json`.
  2. En el Nodo 2, ejecuta manualmente `admin.addPeer("<enode_del_nodo_1>")`.
  3. Verifica que el firewall o puerto local `38888` permita tráfico TCP y UDP.

---

*Desarrollado y mantenido bajo las directrices oficiales del ecosistema Rogue Signal.*

