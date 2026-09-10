# Unified Blockchain Benchmark Harness

Fair, platform-agnostic throughput/latency benchmarking across four permissioned
blockchains with three different transaction models:

| Platform | Model | Consensus |
| -------- | ----- | --------- |
| **Hyperledger Fabric** (`fabric-cft`, `fabric-bft`) | EOV + chaincode | Raft / SmartBFT |
| **Drunix** (NPCI fork of Fabric 2.5.x) | EOV + chaincode | Raft (CFT only) |
| **Fabric-X** | EOV, no chaincode (FSC + Token SDK) | Arma (sharded BFT) |
| **NeuChain** | Ordering-free Execute-Validate | deterministic |

One adapter interface, one load generator, one metrics pipeline. Normalized
workloads for the cross-platform comparison; platform-native workloads for
ceilings. Every methodology decision is written down in [`docs/`](docs/README.md).

## Status

Full breakdown + why the two remaining items need heavy compute or GCP creds:
**[docs/REMAINING-WORK.md](docs/REMAINING-WORK.md)**.

| Platform | Adapter | Live-tested | Notes |
| -------- | ------- | ----------- | ----- |
| `fabric-cft` (Raft) | ✅ | ✅ smoke + probe-sweep (Fabric 2.5.16) | e2e p50 165–565 ms across the sweep |
| `fabric-bft` (SmartBFT) | ✅ | ✅ smoke (Fabric 3.1.5, 4 orderers) | ≈ cft at low load once batching is pinned |
| `drunix` (npci/drunix) | ✅ (wraps fabric; CP block-event finality) | deploy + lifecycle verified live | **write path blocked upstream** — Drunix's sparse-block format panics the Committing Peer on vanilla-SDK writes; needs a Drunix client SDK or CP fix ([docs/REMAINING-WORK.md §3](docs/REMAINING-WORK.md)) |
| `fabricx` (token REST) | ✅ matches the **verified** `fabric-x-samples` API, `httptest`-tested | token `transfer` runs today | normalized `kv-*` needs the custom `/kv` FSC view (stub + code-level spec) — **live-devnet gated** |
| `neuchain` (pure-Go ZMQ+protobuf+RSA) | ✅ unit-tested (sign, result-frame, tx-build) | — | server binaries need a ~1 h C++ build — **compute gated**, `deploy/docker/neuchain/build.sh` |
| GCP campaign | `scripts/gcp-run.sh` + Terraform ready | — | **credential gated** — provide `-var project=…` |

## Quick start

```
scripts/setup.sh                                   # build + monitoring + unit tests
./bin/benchrunner run --config configs/quick-smoke.yaml --platform mock   # no network needed

./bin/benchrunner setup --platform fabric-cft --profile local
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/probe-sweep.yaml --platform fabric-cft
./bin/benchrunner teardown --platform fabric-cft
```

Full walkthrough: [docs/guides/quickstart.md](docs/guides/quickstart.md).

## Layout

```
cmd/benchrunner/     CLI: run | suite | report | setup | teardown | list
pkg/adapters/        PlatformAdapter interface + registry; fabric, drunix, mock (fabricx, neuchain: Phase 3/4)
pkg/workloads/       normalized workloads: kv-write, kv-read, kv-mixed, transfer
pkg/loadgen/         key distributions + open/closed-loop generator (coordinated-omission safe)
pkg/metrics/         per-tx T1/T2/T3 collector, HDR histograms, native scrape, docker-stats sampler
pkg/harness/         run config, resource profile, engine, manifest, reporter
chaincodes/kvstore/  Go chaincode for Fabric/Drunix normalized workloads
deploy/docker/       per-platform Compose topologies + up.sh/down.sh, monitoring stack
deploy/profiles/     resource budgets: local (16c/16GB host), gcp-small, gcp-full
configs/             reusable run definitions
docs/                architecture, per-platform notes, workloads, 15 ADRs, guides
```

## Core ideas

- **T1/T2/T3** — submit-entered / platform-acknowledged / committed-in-block.
  Throughput uses **T3**; latency is **T3 − scheduled-send** (not T3 − actual
  send, so a backlog cannot hide overload). See
  [docs/architecture/metrics-methodology.md](docs/architecture/metrics-methodology.md).
- **Probe-and-sweep** — floor latency, step to saturation, hold at 90% of the
  knee.
- **Fairness by construction** — identical config for normalized runs (LevelDB,
  pinned orderer batch params, one seed); platform-native metrics are collected
  but **never** used in cross-platform comparison. See
  [docs/architecture/fairness-guarantees.md](docs/architecture/fairness-guarantees.md).
- **Every run writes a manifest** capturing every fairness lever + versions +
  git SHA + caveats, so two runs that disagree can be explained.

## Requirements

Go 1.26+, Docker + compose plugin, `git`, `curl`, `jq`, `python3` (+ `matplotlib`
for charts). Drunix needs a source checkout (`BENCH_DRUNIX_REPO`).
