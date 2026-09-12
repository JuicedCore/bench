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
| `drunix` (npci/drunix) | ✅ (wraps fabric; CP block-event finality) | deploy + lifecycle + write path verified live | write path **unblocked**: the cause was Drunix's YugabyteDB statedb rejecting non-JSON values, fixed client-side in `pkg/adapters/drunix/valuecodec.go`. The earlier sparse-block diagnosis was wrong ([docs/REMAINING-WORK.md §3](docs/REMAINING-WORK.md)). Runs on YugabyteDB, so normalized runs carry a state-DB caveat |
| `fabricx` (native gRPC) | ✅ broadcast to Arma router + sidecar deliver stream; real submit ack | — | rebuilt on the native path ([adr-016](docs/decisions/adr-016-fabricx-native-grpc.md)); deploy builds Arma + committer from pinned source. Not yet live-verified |
| `neuchain` (pure-Go ZMQ+protobuf+RSA) | ✅ unit-tested (sign, result-frame, tx-build, finality timestamping) | — | server binaries need a 45–90 min C++ build — **compute gated**, `deploy/docker/neuchain/build.sh`. Runs the full normalized mode set once built |
| GCP campaign | `scripts/gcp-run.sh` + Terraform ready | — | **credential gated** — provide `-var project=…` |

## Quick start on a fresh machine

```
git clone <this repo> && cd bench
scripts/preflight.sh                 # can THIS host run it, and with which profile?
scripts/setup.sh [profile]           # build + unit tests + monitoring stack
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform mock   # no network needed
```

`preflight.sh` checks tooling, the Docker daemon, and — the part that actually
bites — whether the host has the CPU/RAM/disk the profile budgets. A run on an
over-committed host measures swap, not the platform. If `local` (16 GB) does not
fit, it names one that does; pass that profile everywhere below.

Then one platform end to end:

```
./bin/benchrunner setup --platform fabric-cft --profile local
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft
./bin/benchrunner teardown --platform fabric-cft
```

Or the whole comparison, one platform at a time with full inter-run isolation —
this is what produces the head-to-head table:

```
scripts/run-all.sh configs/normalized/probe-sweep.yaml local fabric-cft fabric-bft drunix
# -> docs/reports/comparison.html
```

Note the Fabric-family platforms (`fabric-cft`, `fabric-bft`, `drunix`) share
ports and container names, so **only one can be up at a time**; `run-all.sh`
sequences and isolates them for you.

Bring up / tear down / wipe **everything** at once:

```
make up-all          # monitoring + one Fabric-family net + fabricx/neuchain (if built)
make down-all        # stop + remove all platforms + monitoring
make clean           # + caches, connection.env, results, generated reports  (prompts)
make clean-images    # + the pulled platform images (~4-6 GB)
```

Full walkthrough: [docs/guides/quickstart.md](docs/guides/quickstart.md).

## Layout

```
cmd/benchrunner/     CLI: run | suite | report | setup | teardown | list
pkg/adapters/        PlatformAdapter interface + registry; fabric, drunix, fabricx, neuchain, mock
pkg/workloads/       normalized workloads: kv-write, kv-read, kv-mixed, transfer
pkg/loadgen/         key distributions + open/closed-loop generator (coordinated-omission safe)
pkg/metrics/         per-tx T1/T2/T3 collector, HDR histograms, native scrape, docker-stats sampler
pkg/harness/         run config, resource profile, engine, manifest, reporter
chaincodes/kvstore/  Go chaincode for Fabric/Drunix normalized workloads
deploy/docker/       per-platform Compose topologies + up.sh/down.sh, monitoring stack
deploy/profiles/     resource budgets: local (16c/16GB host), gcp-small, gcp-full
deploy/terraform/    GCP infra (main.tf) for the gcp-full campaign
configs/             reusable run definitions
scripts/             setup.sh, up-all.sh, down-all.sh, clean.sh, run-all.sh,
                     gcp-run.sh, neuchain-proto-spike.sh, plot.py
                     (NeuChain image build: deploy/docker/neuchain/build.sh)
docs/                architecture, per-platform notes, workloads, 15 ADRs, guides,
                     REMAINING-WORK.md
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
for charts, `pyyaml` or `yq` for profile parsing). Drunix needs a source checkout
(`BENCH_DRUNIX_REPO`). Run `scripts/preflight.sh` — it checks all of these plus
host capacity and tells you what is missing.

**Host sizing.** Profiles live in `deploy/profiles/`. `local` needs ~14 GB
(11 GB platform + 2 GB load generator + 1 GB monitoring) on a 16 GB machine;
`local-small` needs ~11 GB and fits a 13-14 GB host. Every fairness lever
(orderer batch params, state DB, workload, seed, windows) is identical between
them — only the resource envelope differs, and the manifest records it. Runs are
comparable **within** a profile, never across profiles.

Some platforms need more than this repo:

| Platform | Extra requirement |
| -------- | ----------------- |
| `fabric-cft` / `fabric-bft` | none — images are pulled on first bring-up |
| `drunix` | clones `github.com/npci/drunix`; pulls `npcioss/drunix-*` images |
| `fabricx` | clones `fabric-x-committer` + `fabric-x-orderer` at pinned tags and builds one image serving four roles (~15-20 min, several GB) |
| `neuchain` | a `bench/neuchain:ev` image from a 45-90 min C++ build (`deploy/docker/neuchain/build.sh`, ~25-30 GB disk, 6-10 GB RAM) |
