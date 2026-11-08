# rogue-stratum — RandomX Stratum Bridge for Rogue Signal (ROGUE)

`rogue-stratum` is a high-performance Stratum v1 mining bridge that connects standard, official **XMRig** CPU miners to the **Rogue Signal (ROGUE)** EVM blockchain node (`core-rogue`).

---

## 🏛️ Architecture & Protocol Mapping

The bridge translates between standard Monero RandomX (`rx/0`) Stratum mining messages and the Ethereum JSON-RPC consensus engine:

```
+-------------------+                    +-------------------+                    +--------------------+
|   Official XMRig  |  Stratum v1 (TCP)  |   rogue-stratum   |  JSON-RPC (HTTP)   |     core-rogue     |
|   (rx/0 profile)  | -----------------> |    (Port 3333)    | -----------------> |    (Port 38845)    |
|  Standard CPU/GPU |                    |  Go Stratum Pool  |                    | Consensus: RandomX |
+-------------------+                    +-------------------+                    +--------------------+
```

### 1. Template Blob Layout (76 Bytes / 152 Hex Characters)
RandomX in XMRig operates on standard 76-byte block templates where the 32-bit miner nonce is modified at byte offset **39** (`blob[39..42]`):

| Byte Range | Length | Field Description |
| :--- | :--- | :--- |
| `0 .. 31` | 32 bytes | Header **SealHash** (pre-seal Keccak256 hash) |
| `32 .. 38` | 7 bytes | Reserved zeros (padding) |
| `39 .. 42` | 4 bytes | **Miner Nonce** (injected by XMRig; lower 32-bits of Ethereum Nonce) |
| `43 .. 46` | 4 bytes | **ExtraNonce** (assigned per job; upper 32-bits of Ethereum Nonce) |
| `47 .. 75` | 29 bytes | Reserved zeros (padding) |

### 2. Full Nonce Reconstruction
When XMRig submits a winning share with 4-byte nonce $N_{32}$, `rogue-stratum` combines it with the job's ExtraNonce $E_{32}$:
$$N_{64} = (E_{32} \ll 32) \mid N_{32}$$
This 64-bit value is submitted to `core-rogue` via `eth_submitWork`, allowing `core-rogue`'s `VerifySeal` to reconstruct the exact same 76-byte blob and verify the RandomX hash byte-for-byte.

---

## 🚀 Building & Running

### 1. Build Native Binary
```bash
cd rogue-stratum
go build -o rogue-stratum .
```

### 2. Launch the Bridge
```bash
./rogue-stratum \
  --stratum.host 0.0.0.0 \
  --stratum.port 3333 \
  --node.rpc http://127.0.0.1:38845 \
  --diff 1000
```

### 3. CLI Options
| Flag | Default | Description |
| :--- | :--- | :--- |
| `--stratum.host` | `0.0.0.0` | IP address to bind Stratum TCP server |
| `--stratum.port` | `3333` | Port for incoming miner connections |
| `--node.rpc` | `http://127.0.0.1:38845` | Core-Rogue node HTTP JSON-RPC URL |
| `--diff` | `1000` | Share difficulty target assigned to miners |
| `--poll.interval`| `250ms` | Polling frequency for detecting new work from node |
| `--max.conn` | `1024` | Maximum concurrent miner connections |
| `--log.level` | `info` | Logging verbosity (`debug`, `info`, `warn`, `error`) |

---

## ⛏️ Connecting XMRig

`rogue-stratum` works out-of-the-box with **unmodified, official XMRig** releases.

### Option A: Command Line
```bash
xmrig -o 127.0.0.1:3333 -u 0xYourRogueWalletAddress.worker1 -p x -a rx/0
```

### Option B: `config.json` Configuration
Edit or create your XMRig `config.json`:
```json
{
  "autosave": true,
  "cpu": {
    "enabled": true,
    "huge-pages": true,
    "hw-aes": null,
    "priority": null,
    "memory-pool": true,
    "yield": true,
    "max-threads-hint": 100
  },
  "opencl": false,
  "cuda": false,
  "pools": [
    {
      "algo": "rx/0",
      "coin": null,
      "url": "127.0.0.1:3333",
      "user": "0xYourRogueWalletAddress.worker1",
      "pass": "x",
      "rig-id": "worker1",
      "nicehash": false,
      "keepalive": true,
      "enabled": true,
      "tls": false
    }
  ]
}
```

---

## 🧪 Testing

Run the full automated test suite:
```bash
go test -v ./...
```
Output:
```
=== RUN   TestConfigAndTarget
--- PASS: TestConfigAndTarget (0.00s)
=== RUN   TestJobCreationAndBlobLayout
--- PASS: TestJobCreationAndBlobLayout (0.00s)
=== RUN   TestEndToEndStratumTCP
--- PASS: TestEndToEndStratumTCP (0.10s)
PASS
ok  	github.com/roguesignalchain/rogue-stratum	0.105s
```
