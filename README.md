# Core-Rogue (Rogue Signal)

> Official Go node implementation for **Rogue Signal (ROGUE)** — an ASIC-resistant Proof-of-Work EVM blockchain powered by **RandomX (`rx/0`)**.
> Based on [etclabscore/core-geth](https://github.com/etclabscore/core-geth).

---

## 🚀 Overview & Specifications

| Parameter | Value | Details |
| :--- | :--- | :--- |
| **Network Name** | Rogue Signal | Official network name |
| **Native Currency** | ROGUE | 18 decimals (`10^18` wei per ROGUE) |
| **Chain ID** | `800055` | Strict EIP-155 replay protection from block 0 |
| **Mining Algorithm**| **RandomX (`rx/0`)** | Monero standard profile (100% compatible with XMRig) |
| **Memory Profile** | **Light Mode (~256 MB RAM) by default** | Low-end PC friendly; optional Fast Mode (~2.08 GB RAM) via `--randomx.fast` |
| **Block Target Time** | **10 seconds** | High responsiveness for DeFi and smart contracts |
| **Gas Limit** | **60,000,000** per block | High throughput without choking CPU nodes |
| **Difficulty Algorithm** | **LWMA-1** ($N=60$) | Linear Weighted Moving Average with bootstrap scaling $k=\min(60, H)$ |
| **Target Clamping** | **$\pm 33\%$ per block** | Clamps adjustment to $[67\%, 133\%]$ to prevent hashrate oscillation attacks |
| **Genesis Difficulty** | `0x10000` (65,536) | Min difficulty threshold for rapid dev startup and CI tests |
| **Timestamp Rules** | **Strict MTP-11** | Block time must exceed Past Median Time of last 11 blocks; max 15s future drift |
| **Total Max Supply** | **100,000,000 ROGUE** | Mathematical hard cap across 32 halving eras (zero tail emission) |
| **Initial Block Reward**| **8 ROGUE** | Halving every 6,300,000 blocks (~2 years at 10s/block) |
| **Reward Split** | **95% Miner / 5% Protocol Treasury** | 7.6 ROGUE to miner, 0.4 ROGUE to protocol treasury per block |
| **Treasury Address** | Configurable in `genesis.json` | Default fallback `0x8000550000000000000000000000000000800055` |
| **Uncle Blocks** | **Strictly Forbidden (0%)** | Uncles rejected (`ErrUnclesNotAllowed`), simplifying consensus |
| **Seed Rotation** | Every 2048 blocks (~5.7h) | Seed lag of 64 blocks (~10m) for background cache precomputation |
| **Cache Retention** | 128 blocks (~20m) | Retains previous epoch cache to validate chain reorgs seamlessly |
| **SealHash Format** | RLP(Header without Nonce & MixDigest) | Evaluates canonical Ethereum header serialization |
| **MixDigest** | 32-byte RandomX hash | Direct inclusion proof stored in header |
| **Default Ports** | P2P: `38888` \| HTTP RPC: `38845` \| WS: `38846` \| Stratum: `3333` | Non-conflicting dedicated ports |

---

## 🛠️ Building Core-Rogue (Native, No Docker)

Core-Rogue embeds the C++ RandomX engine via CGO. It compiles into a 100% self-contained static executable.

### Prerequisites

* **Go**: `1.24+` (or latest stable)
* **CMake**: `3.15+`
* **C/C++ Compiler**: GCC/G++ or Clang

### 1. Clone with Submodules

```bash
git clone --recurse-submodules https://github.com/roguesignalchain/core-rogue.git
cd core-rogue
```

If already cloned without submodules:
```bash
git submodule update --init --recursive
```

### 2. Build on Linux (Ubuntu, Debian, Fedora, Arch)

```bash
# Install dependencies (Ubuntu/Debian)
sudo apt-get update
sudo apt-get install -y build-essential cmake

# Compile RandomX static library and core-rogue executable
make core-rogue
```
The executable will be generated at `./build/bin/core-rogue`.

### 3. Build on macOS (Apple Silicon & Intel)

```bash
# Install dependencies via Homebrew
brew install go cmake

# Compile
make core-rogue
```

### 4. Build on Windows (Native with MinGW-w64)

1. Install [Go for Windows](https://go.dev/dl/) and [CMake](https://cmake.org/download/).
2. Install [MinGW-w64](https://www.mingw-w64.org/) (ensure `gcc`, `g++`, and `mingw32-make` are in your `PATH`).
3. Compile:
```cmd
mingw32-make core-rogue
```

---

## 🔄 Upstream Synchronization

Core-Rogue maintains compatibility with upstream [etclabscore/core-geth](https://github.com/etclabscore/core-geth) to seamlessly inherit security patches and EVM upgrades.

Git remotes are structured as follows:
* `origin` -> `https://github.com/roguesignalchain/core-rogue.git` (this repository)
* `upstream` -> `https://github.com/etclabscore/core-geth.git` (upstream Core-Geth)

To pull in updates from upstream:
```bash
# Fetch upstream branches
git fetch upstream master

# Merge or rebase updates
git merge upstream/master
```

To update the RandomX submodule to the latest upstream release:
```bash
git submodule update --remote --recursive
```

---

## ⛏️ Mining Architecture

Mining on Rogue Signal is decoupled from the node process for maximum efficiency:
* **Mining Algorithm**: RandomX `rx/0` (standard Monero profile).
* **Software**: Standard CPU miners (e.g. **XMRig**).
* **Stratum Bridge**: Nodes expose RPC; a dedicated bridge service (`rogue-stratum`) connects XMRig miners to `core-rogue` via JSON-RPC.

---

# CoreGeth

> A Go client for Ethereum Classic, based on [go-ethereum](https://github.com/ethereum/go-ethereum).

[![OpenRPC](https://img.shields.io/static/v1.svg?label=OpenRPC&message=1.14.0&color=blue)](#openrpc-discovery)
[![Go Reference](https://pkg.go.dev/badge/github.com/etclabscore/core-geth.svg)](https://pkg.go.dev/github.com/etclabscore/core-geth)
[![Go Report Card](https://goreportcard.com/badge/github.com/etclabscore/core-geth)](https://goreportcard.com/report/github.com/etclabscore/core-geth)
[![Tests](https://github.com/etclabscore/core-geth/actions/workflows/test-linux.yml/badge.svg?branch=master)](https://github.com/etclabscore/core-geth/actions/workflows/test-linux.yml)
[![Latest release](https://img.shields.io/github/v/release/etclabscore/core-geth)](https://github.com/etclabscore/core-geth/releases/latest)
[![Release date](https://img.shields.io/github/release-date/etclabscore/core-geth)](https://github.com/etclabscore/core-geth/releases/latest)
[![Last commit](https://img.shields.io/github/last-commit/etclabscore/core-geth)](https://github.com/etclabscore/core-geth/commits/master)

CoreGeth runs Ethereum Classic mainnet, the Mordor testnet and private proof-of-work chains. It keeps the proof-of-work consensus and mining that Ethereum Classic runs on, which upstream go-ethereum removed after Ethereum moved to proof of stake.

It stays as close to upstream go-ethereum as Ethereum Classic allows. RPC, JS and CLI APIs, data storage locations and schemas, and node protocols are kept compatible with upstream, so tooling written for geth works with CoreGeth. Bug reports, fixes and proposals that apply to upstream code belong upstream. [Relationship with upstream go-ethereum](#relationship-with-upstream-go-ethereum) describes how changes are merged.

## Maintenance

CoreGeth is maintained in this repository and has been released from it since March 2020. It has implemented every Ethereum Classic upgrade from Phoenix (2020) to Spiral (2024) as specified through the [ECIP process](https://ecips.ethereumclassic.org). It is the client most of the network runs today, as [etcnodes.org](https://etcnodes.org) shows. The badges above show the latest release and the most recent commit.

If development ever moves to another repository, this README will say so.

## Getting a release

CoreGeth releases are published only on this repository's [releases page](https://github.com/etclabscore/core-geth/releases), with container images at [etclabscore/core-geth](https://hub.docker.com/r/etclabscore/core-geth) on Docker Hub. To check that a binary is a CoreGeth release:

- It comes from `github.com/etclabscore/core-geth/releases` or the `etclabscore/core-geth` Docker Hub image, and nowhere else.
- Its version is in the `v1.12.x` line. Version numbers are assigned by this project and say nothing about where a binary came from, since anyone can tag a fork with a higher number.
- Every archive comes with a `.sha256` file built by this repository's release workflow. Container images have no such file; they are identified by their tag on the `etclabscore` Docker Hub account, and each tag matches a release on the releases page.
- The project's only website is the documentation at [etclabscore.github.io/core-geth](https://etclabscore.github.io/core-geth). Any other domain using the CoreGeth name is unrelated to this project.

## Quickstart

Run an Ethereum Classic node with Docker:

```shell
docker run -d --name coregeth \
  -v coregeth:/root \
  -p 30303:30303 -p 30303:30303/udp \
  -p 127.0.0.1:8545:8545 \
  etclabscore/core-geth:latest \
  --classic --http --http.addr 0.0.0.0
```

For production, pin a release tag such as `v1.12.23` instead of `latest`. Chain data is kept in the `coregeth` volume. Port 30303 is for peers. The JSON-RPC API on port 8545 is published to localhost only, because `--http.addr 0.0.0.0` applies inside the container.

Follow the logs and check sync progress:

```shell
docker logs -f coregeth
curl -s -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}' \
  http://127.0.0.1:8545
```

For binaries, other networks and the full set of flags, see the [installation guide](https://etclabscore.github.io/core-geth/getting-started/installation).

## Networks

Networks selectable with a flag in the `geth` program built from this repository.

| Network                     | Flag        | Consensus     | Status                                                  |
| ---                         | ---         | ---           | ---                                                     |
| Ethereum Classic (ETC)      | `--classic` | PoW (etchash) | supported                                               |
| Mordor (ETC testnet)        | `--mordor`  | PoW (etchash) | supported                                               |
| MintMe.com Coin (MINTME)    | `--mintme`  | PoW           | supported                                               |
| Private chains              | genesis file | PoW / PoA    | supported                                               |
| Ephemeral development chain | `--dev`     | PoA           | supported                                               |

## Documentation

The [CoreGeth documentation](https://etclabscore.github.io/core-geth) covers [installation](https://etclabscore.github.io/core-geth/getting-started/installation), the [command line](https://etclabscore.github.io/core-geth/getting-started/run-cli), the [JSON-RPC API](https://etclabscore.github.io/core-geth/JSON-RPC-API), [building from source](https://etclabscore.github.io/core-geth/developers/build-from-source) and [running a private network](https://etclabscore.github.io/core-geth/tutorials/private-network), and the [go-ethereum documentation](https://geth.ethereum.org/docs/) applies to everything CoreGeth shares with upstream.

## Relationship with upstream go-ethereum

CoreGeth is based on go-ethereum v1.13.14 (February 2024). Since then upstream has removed the code Ethereum Classic still depends on, in particular proof-of-work consensus, mining and the block propagation that goes with them, so merging whole upstream releases is no longer possible. Changes are backported individually instead.

The rules that decide what gets merged:

- If upstream has code for it, CoreGeth uses upstream's code, including where upstream fixed a problem differently. A backport is kept as close to the upstream commit as this codebase allows and comes with a test that exercises it here, since upstream only tested its own tree.
- CoreGeth does not diverge from upstream unless Ethereum Classic requires it.
- The ETC-specific surface (proof-of-work consensus, mining, block propagation, chain configurations) has no upstream to defer to, so changes there need a written rationale and a test that fails without them.
- Consensus rules follow the [ECIP process](https://ecips.ethereumclassic.org). CoreGeth implements accepted ECIPs.
- Bugs in code shared with upstream are reported and fixed upstream first.

## Security

Please do not report vulnerabilities in public issues. Use GitHub's private [vulnerability report](https://github.com/etclabscore/core-geth/security/advisories/new) for this repository; see [SECURITY.md](./SECURITY.md) for details. Fixed vulnerabilities are listed in the notes of the release that fixes them.

## Contribution

Thank you for considering to help out with the source code! We welcome contributions from anyone on the internet, and are grateful for even the smallest of fixes!

If you'd like to contribute to CoreGeth, please fork, fix, commit and send a pull request for the maintainers to review and merge into the main code base. If you wish to submit more complex changes though, please [open an issue](https://github.com/etclabscore/core-geth/issues) first, or ask in the [Ethereum Classic Discord](https://ethereumclassic.org/discord), to ensure those changes are in line with the general philosophy of the project and/or get some early feedback which can make both your efforts much lighter as well as our review and merge procedures quick and simple.

Please make sure your contributions adhere to our coding guidelines:

 * Code must adhere to the official Go [formatting](https://golang.org/doc/effective_go.html#formatting) guidelines (i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
 * Code must be documented adhering to the official Go [commentary](https://golang.org/doc/effective_go.html#commentary) guidelines.
 * Pull requests need to be based on and opened against the `master` branch.
 * Commit messages should be prefixed with the package(s) they modify.
   * E.g. "eth, rpc: make trace configs optional"

Please see the upstream [Developers' Guide](https://geth.ethereum.org/docs/developers/geth-developer/dev-guide) for more details on configuring your environment, managing project dependencies, and testing procedures.

## Forks and other clients

Please be cautious about using other repositories that carry the CoreGeth name. They are not maintained or reviewed by this project, and a release published there is not a CoreGeth release. Forks are welcome. We ask that they use a different name so operators can tell which client they are running.

CoreGeth is one of several Ethereum Classic clients. Others include:

- [getc](https://github.com/go-ethereum-classic/go-ethereum-classic), a Go client based on a newer go-ethereum.
- [Nethermind](https://github.com/NethermindEth/nethermind) with the [ETC plugin](https://github.com/nethermindEtc/nethermind-etc-plugin), a .NET client.
- [Besu](https://github.com/besu-eth/besu) with the [ETC plugin](https://github.com/besu-etc/besu-etc-plugin), a Java client.

Which one to run is the operator's choice. Running more than one client makes the network safer.

## License

The CoreGeth library (i.e. all code outside of the `cmd` directory) is licensed under the [GNU Lesser General Public License v3.0](https://www.gnu.org/licenses/lgpl-3.0.en.html), also included in our repository in the `COPYING.LESSER` file.

The CoreGeth binaries (i.e. all code inside of the `cmd` directory) is licensed under the [GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html), also included in our repository in the `COPYING` file.
