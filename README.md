# Unified Blockchain Benchmark Harness

Fair, platform-agnostic throughput and latency benchmarking of permissioned
blockchains built on different transaction models. It uses one adapter interface,
one load generator and one metrics pipeline, with every methodology decision
written down.

This README is the complete guide, meant to be read top to bottom. Each section
summarizes one part of the harness and links to the document with the full detail.

**Contents**

1. [What it benchmarks](#1-what-it-benchmarks)
2. [Status](#2-status)
3. [Documentation map](#documentation-map)
4. [How the harness works](#4-how-the-harness-works)
5. [Methodology: how numbers are measured](#5-methodology-how-numbers-are-measured)
6. [Fairness: what is comparable](#6-fairness-what-is-comparable)
7. [Workloads](#7-workloads)
8. [Run configs](#8-run-configs)
9. [Profiles and hardware](#9-profiles-and-hardware)
10. [Install and first run](#10-install-and-first-run)
11. [Running every combination](#running-every-combination)
12. [Results: where they are and how to read them](#12-results-where-they-are-and-how-to-read-them)
13. [Latest results](#latest-results)
14. [Troubleshooting](#troubleshooting)
15. [Repository layout, development, extending](#15-repository-layout-development-extending)

---

## 1. What it benchmarks

| Platform (`--platform`) | Model | Consensus | Pinned version | Adapter |
| ----------------------- | ----- | --------- | -------------- | ------- |
| `fabric-cft` | Execute-Order-Validate + chaincode | Raft | Fabric 2.5.16 | [`pkg/adapters/fabric`](pkg/adapters/fabric) |
| `fabric-bft` | Execute-Order-Validate + chaincode | SmartBFT (4 orderers) | Fabric 3.1.5 | [`pkg/adapters/fabric`](pkg/adapters/fabric) |
| `drunix` | EOV + chaincode; NPCI fork of Fabric 2.5 with Lite/Committing peer split | Raft (CFT only) | `npci/drunix` at a pinned commit | [`pkg/adapters/drunix`](pkg/adapters/drunix) |
| `fabricx` | client-signed namespace read/write sets, no chaincode | Arma (sharded BFT) | committer v1.0.5, orderer v1.0.6 | [`pkg/adapters/fabricx`](pkg/adapters/fabricx) |
| `neuchain` | ordering-free Execute-Validate | deterministic | `iDC-NEU/NeuChain@ev` | [`pkg/adapters/neuchain`](pkg/adapters/neuchain) |
| `mock` | in-process fake, no network | — | — | [`pkg/adapters/mock`](pkg/adapters/mock) |

Two kinds of workload:
- **Normalized** workloads give the cross-platform comparison: same logical work,
  identical load, identical fairness levers.
- **Platform-native** workloads would show each platform's ceiling. They are
  designed but not yet implemented.

The harness runs the same way on a Linux workstation and on GCP: the same
installer, deploy scripts, configs and profiles.

Platform deep dives: [Fabric](docs/platforms/fabric.md) ·
[Drunix](docs/platforms/drunix.md) · [Fabric-X](docs/platforms/fabric-x.md)
([integration notes](docs/platforms/fabricx-integration.md)) ·
[NeuChain](docs/platforms/neuchain.md)
([client implementation](docs/platforms/neuchain-client-implementation.md)).

## 2. Status

| Platform | Live-verified on `local-small` | Notes |
| -------- | ------------------------------ | ----- |
| `fabric-cft` | ✅ smoke + probe-sweep to the knee | knee 1000 TPS; a peer panicked on a failed host disk write past the knee, so no headline ([REMAINING-WORK §5](docs/REMAINING-WORK.md#5-fabric-cft-peer-panic-on-a-failed-disk-write)) |
| `fabric-bft` | ✅ smoke + full probe-sweep | knee 500 TPS, headline 450 TPS |
| `drunix` | ✅ smoke + probe-sweep to the knee | knee 500 TPS; Lite Peer OOM-killed past the knee, so no headline. Runs on YugabyteDB (state-DB caveat) |
| `fabricx` | ✅ smoke + probe-sweep to the knee | knee 3500 TPS; Arma container OOM-killed past the knee, so no headline. PostgreSQL (state-DB caveat). Results before 2026-09-14 are invalid |
| `neuchain` | ⚠️ adapter unit-tested; server image not built | 45–90 min C++ build: [NeuChain build guide](docs/neuchain/build-and-portability-guide.md) |
| GCP | ✅ Terraform validated, installer tested on 5 distros | not yet applied against a real project |

What is finished, what is left, and why: [docs/REMAINING-WORK.md](docs/REMAINING-WORK.md).

## Documentation map

Every document in the repository, grouped by what you want to do.

### Reading paths

| You want to… | Read, in order |
| ------------ | -------------- |
| understand the project | this README → [architecture/overview](docs/architecture/overview.md) → [metrics-methodology](docs/architecture/metrics-methodology.md) → [fairness-guarantees](docs/architecture/fairness-guarantees.md) |
| run benchmarks | §9–§12 below → [quickstart](docs/guides/quickstart.md) → [COMMANDS.md](COMMANDS.md) → [CONFIGS.md](CONFIGS.md) → [running-benchmarks](docs/guides/running-benchmarks.md) |
| quote or analyse results | §12–§13 → [interpreting-results](docs/guides/interpreting-results.md) → [normalization-status](docs/architecture/normalization-status.md) → [workloads/mismatches](docs/workloads/mismatches.md) |
| run on GCP | [gcp-deployment](docs/guides/gcp-deployment.md) → [deploy/terraform/README.md](deploy/terraform/README.md) |
| add a platform | [adding-platform](docs/guides/adding-platform.md) → [architecture/overview](docs/architecture/overview.md#the-adapter-contract) |
| fix a failure | [Troubleshooting](#troubleshooting) → [docs/README.md#troubleshooting](docs/README.md#troubleshooting) |

### Architecture and methodology

| Document | What it covers |
| -------- | -------------- |
| [architecture/overview.md](docs/architecture/overview.md) | layers, adapter contract, a run start to finish, what lives where |
| [architecture/metrics-methodology.md](docs/architecture/metrics-methodology.md) | T1/T2/T3, coordinated omission, windows, HDR histograms, probe-and-sweep, when a step holds, the invariant |
| [architecture/fairness-guarantees.md](docs/architecture/fairness-guarantees.md) | comparable vs native metrics, levers held identical, where T3 is stamped, caveats, isolation, run rejection rules |
| [architecture/normalization-status.md](docs/architecture/normalization-status.md) | how far the code meets the fairness contract today, lever by lever, and what can be quoted |

### Workloads

| Document | What it covers |
| -------- | -------------- |
| [workloads/normalized.md](docs/workloads/normalized.md) | `kv-write`, `kv-read`, `kv-mixed`, `transfer`; keys, values, distributions, knobs |
| [workloads/platform-native.md](docs/workloads/platform-native.md) | per-platform best-case workloads (design only) |
| [workloads/mismatches.md](docs/workloads/mismatches.md) | where a workload maps awkwardly onto a platform, and the caveat it carries |

### Platforms

| Document | What it covers |
| -------- | -------------- |
| [platforms/fabric.md](docs/platforms/fabric.md) | Fabric CFT + BFT: lifecycle, adapter, fairness levers, deploy, gotchas |
| [platforms/drunix.md](docs/platforms/drunix.md) | Drunix architecture, adapter, YugabyteDB state, JSON write-path fix |
| [platforms/fabric-x.md](docs/platforms/fabric-x.md) | Fabric-X architecture, Arma, adapter, deployment |
| [platforms/fabricx-integration.md](docs/platforms/fabricx-integration.md) | how the Fabric-X integration was established from source, and every bring-up gotcha |
| [platforms/neuchain.md](docs/platforms/neuchain.md) | NeuChain architecture, adapter, deploy status |
| [platforms/neuchain-client-implementation.md](docs/platforms/neuchain-client-implementation.md) | NeuChain wire protocol as reimplemented in Go |
| [neuchain/build-and-portability-guide.md](docs/neuchain/build-and-portability-guide.md) | building the NeuChain images, manual setup, exporting to another machine |

### Guides and references

| Document | What it covers |
| -------- | -------------- |
| [guides/quickstart.md](docs/guides/quickstart.md) | Linux host from zero to a comparison in five steps |
| [COMMANDS.md](COMMANDS.md) | exact up / run / down commands per platform, and a Grafana walkthrough |
| [CONFIGS.md](CONFIGS.md) | every run config: what it does, compatibility matrix, commands |
| [guides/running-benchmarks.md](docs/guides/running-benchmarks.md) | the run loop, lifecycle `make` targets, output files, campaign logs and failure captures |
| [guides/interpreting-results.md](docs/guides/interpreting-results.md) | reading `summary.txt`, red flags, plotting, what may be compared |
| [guides/gcp-deployment.md](docs/guides/gcp-deployment.md) | GCP topology, one-time setup, IAM, running, cost, fairness on GCP |
| [deploy/terraform/README.md](deploy/terraform/README.md) | the Terraform stack itself |
| [guides/adding-platform.md](docs/guides/adding-platform.md) | implementing, registering and testing a new adapter |
| [HARDWARE-REQUIREMENTS.md](docs/HARDWARE-REQUIREMENTS.md) | host specs per profile, monitoring footprint, why under-provisioned hosts OOM, scaling behaviour |
| [REMAINING-WORK.md](docs/REMAINING-WORK.md) | project status and open issues |
| [docs/README.md](docs/README.md) | troubleshooting and error lookup (the CLI links here) |
| [docs/HTMLS/index.html](docs/HTMLS/index.html) | static HTML rendering of the docs; a snapshot that can lag the markdown |

### Decision records (ADRs)

| ADR | Decision |
| --- | -------- |
| [001](docs/decisions/adr-001-go-harness.md) | Go for the whole harness: one language, gRPC, no cgo |
| [002](docs/decisions/adr-002-neuchain-client-spike.md) | NeuChain client via a time-boxed proto spike (outcome: pure Go) |
| [003](docs/decisions/adr-003-fabricx-fsc-view-and-rest.md) | Fabric-X through FSC views + REST: **superseded by 016** |
| [004](docs/decisions/adr-004-docker-compose.md) | Docker Compose locally, not Kubernetes |
| [005](docs/decisions/adr-005-sequential-runs.md) | runs are sequential, never concurrent |
| [006](docs/decisions/adr-006-hdr-histogram.md) | HDR histograms for latency |
| [007](docs/decisions/adr-007-drunix-cft-only.md) | Drunix joins CFT comparisons only |
| [008](docs/decisions/adr-008-neuchain-ev-only.md) | NeuChain `ev` branch only |
| [009](docs/decisions/adr-009-workload-strategy.md) | both normalized and platform-native workloads |
| [010](docs/decisions/adr-010-mismatch-report.md) | run mismatched workloads anyway, with an explicit caveat |
| [011](docs/decisions/adr-011-orderer-batch-params.md) | pin orderer block-cutting parameters |
| [012](docs/decisions/adr-012-state-db-leveldb.md) | LevelDB for all normalized runs |
| [013](docs/decisions/adr-013-config-parity-policy.md) | identical config for normalized, tuned for native |
| [014](docs/decisions/adr-014-fabric-v3-release-pin.md) | pin Fabric to release tags, not `main` |
| [015](docs/decisions/adr-015-phased-delivery.md) | phased delivery, shippable after each platform |
| [016](docs/decisions/adr-016-fabricx-native-grpc.md) | drive Fabric-X over its native gRPC path |

---

## 4. How the harness works

```
 configs/normalized/*.yaml ─┐
 deploy/profiles/*.yaml ────┼─► benchrunner (cmd/benchrunner)
                            │        │
                            │   harness.Engine (pkg/harness)
                            │   builds workload + phases, writes manifest + results
                            │        │
                            │   ┌────┴──────────────────┐
                            │   loadgen.Generator        metrics.Collector
                            │   open/closed loop,        T1/T2/T3 per tx, HDR histograms,
                            │   key distributions        windows, invariant, container sampler
                            │        │
                            │   adapters.PlatformAdapter  Submit → WaitForFinality → Query
                            │   fabric · drunix · fabricx · neuchain · mock
                            │        │
 deploy/docker/<p>/up.sh ───┴─► the real network (Docker Compose), connection.env
```

**Deploy and measure are separate.** `deploy/docker/<platform>/up.sh <profile>`
brings a network up and writes `connection.env` (endpoints, keys, the actual
state DB, the resource budget applied). `benchrunner` never deploys: it reads
connection material from `${BENCH_ADAPTER_*}` variables, so you must
`set -a; source …/connection.env; set +a` first.

**The adapter contract** ([`pkg/adapters/adapter.go`](pkg/adapters/adapter.go)):

| Method | Contract |
| ------ | -------- |
| `Setup` / `Teardown` | client side only |
| `Submit` | returns at **T2** (platform acknowledged), never blocks to finality |
| `WaitForFinality` | returns at **T3** (seen in a committed block); committed-but-invalid is `Valid=false`, not an error |
| `Query` | state read for verification, off the hot path |
| `MetricsEndpoint` | the platform's own `/metrics`; informational only |

Adapters self-register in `init()`. Only `cmd/benchrunner` imports them.

**A run, start to finish:**
1. Load the run config and the profile.
2. Build a seeded workload and a manifest of every fairness lever.
3. Run adapter `Setup`.
4. Build phases: one load phase, or probe → sweep steps → hold.
5. The generator drives the adapter while the collector records T1/T2/T3.
6. Each phase is aggregated over its window.
7. Results are written to `results/<platform>/<timestamp>/`.

If a platform container exits or is OOM-killed, the run stops and has no headline.

Full detail: [docs/architecture/overview.md](docs/architecture/overview.md).

## 5. Methodology: how numbers are measured

**Timeline.** *scheduled* (when the generator meant to send) → **T1** (just before
`Submit`) → **T2** (platform ack) → **T3** (committed in a block).

| Metric | Definition |
| ------ | ---------- |
| confirmed TPS | committed-and-valid transactions in the window ÷ window seconds (T3-based) |
| end-to-end latency (headline) | **T3 − scheduled**, so a backlog cannot hide overload |
| submit / commit latency | T2 − T1 / T3 − T2 |
| send gap | T1 − scheduled; p99 > 50 ms means the generator, not the platform, was the bottleneck |
| failure rate | (invalid + errored + timed-out) ÷ submitted |

Where each platform stamps T2 and T3:

| Platform | T2 | T3 |
| -------- | -- | -- |
| fabric-cft / fabric-bft | gateway returns after the orderer accepted | commit status (block event) |
| drunix | same as Fabric | Committing Peer filtered-block event |
| fabricx | first reply from the four Arma routers | sidecar deliver stream yields the block |
| neuchain | local call return (ZeroMQ is fire-and-forget: no real T2) | poller decodes the block |

**Mechanics:**
- **Coordinated omission.** Open-loop load follows an absolute schedule, and latency
  counts from the scheduled time.
- **Back-pressure.** In-flight transactions are capped at 8 s of offered load. Past
  that, a transaction is recorded as failed (`not sent: … in flight`) rather than
  silently queued.
- **Windows.** A fixed warmup (30 s) and cooldown (15 s) are discarded, keyed on
  scheduled send time. quick-smoke uses 5 s / 3 s.
- **HDR histograms.** 1 µs … 5 min, p1 … p99.99; full snapshots go in `result.json`.
- **Invariant.** `submitted == committed + invalid + errored + timed_out` in every
  window, else `invariant_ok=false`.

**Probe and sweep** (the primary methodology, `probe-sweep.yaml`):

| Phase | What | Measures |
| ----- | ---- | -------- |
| probe | 10 TPS, 30 s | floor latency: the architectural cost with no load |
| sweep-N | 100 → 10 000 TPS ladder, 60 s each | confirmed TPS and latency at each rate |
| hold | 90% of the highest step that **held**, 5 min | **the headline**: stability just below the knee |

A step **holds** only if all three are true:
- failure rate ≤ 2%
- confirmed TPS ≥ 95% of offered (goodput)
- send-gap p99 ≤ 50 ms

After 2 consecutive steps that do not hold, the ladder stops and the skipped steps
are recorded. If every step held, the top step is a lower bound, not the knee.

Full detail: [docs/architecture/metrics-methodology.md](docs/architecture/metrics-methodology.md).

## 6. Fairness: what is comparable

**Compare:** confirmed TPS, end-to-end latency percentiles, failure rate, and
host/container CPU and memory, all collected by the same code for every platform.
Submit/commit split: Fabric family and Fabric-X only. **Never compare** the
platform-native `/metrics` (`native_scrapes`): they only explain one platform's
own curve.

**Held identical in normalized runs.** One config file per mode serves every
platform, so these are literally the same bytes; `pkg/harness/configparity_test.go`
enforces it:
- workload, key space, distribution, value size, seed
- load shape, sweep ladder, "held" rules
- warmup/cooldown and a 60 s finality timeout
- load-generator count
- **total** hardware budget per profile: CPU split evenly, memory by one
  role-weight table (peer 4, state DB 4, orderer 2, other 1), no swap
- block cutting: 100 messages / 1 s / 2 MB across the Fabric family and Fabric-X

**Requested but not equal (disclosed):**

| Gap | Effect | Surfaced |
| --- | ------ | -------- |
| State DB: LevelDB requested; Drunix runs YugabyteDB, Fabric-X PostgreSQL | write-heavy numbers most exposed | automatic caveat |
| Reads: Fabric/Drunix evaluate on one peer; Fabric-X/NeuChain order and commit | `read-profile` and `multi-client` not comparable across families | documented; do not quote across families |
| Block cutting: Fabric-X ignores byte limits; NeuChain not wired | cross-family throughput carries it | documented |
| Crypto: ECDSA endorsement per tx (Fabric family, Fabric-X) vs RSA, no endorsement (NeuChain) | per-tx verification cost differs | manifest `crypto`, footnoted in reports |

**What can be quoted:**

| Mode | Within the Fabric family | Across families |
| ---- | ------------------------ | --------------- |
| quick-smoke, probe-sweep, throughput-scan, latency-profile, contention | yes | yes, with the state-DB and block-cutting caveats |
| read-profile, multi-client | yes | **no** |

**Reject a run if any of these hold:**
- `invariant_ok=false`
- send-gap p99 > 50 ms
- a fairness lever is missing from the manifest
- the headline committed nothing
- no sweep step held
- `saturation_tps` is the top of the ladder
- `manifest.container_failures` is non-empty

**Isolation.** Platforms run strictly one at a time
([ADR-005](docs/decisions/adr-005-sequential-runs.md)). Each is deployed fresh,
with stopped containers and networks pruned and the page cache dropped between
runs.

Full detail: [fairness-guarantees.md](docs/architecture/fairness-guarantees.md) ·
[normalization-status.md](docs/architecture/normalization-status.md) · ADRs
[011](docs/decisions/adr-011-orderer-batch-params.md),
[012](docs/decisions/adr-012-state-db-leveldb.md),
[013](docs/decisions/adr-013-config-parity-policy.md).

## 7. Workloads

Defined once in [`pkg/workloads/workload.go`](pkg/workloads/workload.go) and
deterministic given `seed`, so every platform sees the same key-access pattern.

| Workload | Transactions | Fabric / Drunix | Fabric-X | NeuChain |
| -------- | ------------ | --------------- | -------- | -------- |
| `kv-write` | `Put(key, value)` | chaincode `Put` ([`chaincodes/kvstore`](chaincodes/kvstore)); Drunix JSON-wraps the value | blind write in namespace `0` | YCSB update |
| `kv-read` | `Get(key)` | Evaluate on one peer, nothing committed | read + unique dummy write, ordered and committed | read-set transaction, committed |
| `kv-mixed` | reads and writes per `read_write_ratio` | as above | as above | as above |
| `transfer` | two-account read-modify-write | chaincode `Transfer`, balance computed | two-key read/write set with the workload's values | two-key read + update |

- **Keys:** `key-%09d` or `acct-%09d`.
- **Distributions:** `uniform` (no contention), `zipfian` (hot keys), `fixed`
  (one key).
- **Values:** 64 bytes by default, deterministic but non-constant.

Full detail: [workloads/normalized.md](docs/workloads/normalized.md) ·
[mismatches.md](docs/workloads/mismatches.md) ·
[platform-native.md](docs/workloads/platform-native.md) ·
[ADR-009](docs/decisions/adr-009-workload-strategy.md) ·
[ADR-010](docs/decisions/adr-010-mismatch-report.md).

## 8. Run configs

All in [`configs/normalized/`](configs/normalized). One file per mode serves every
platform: pass `--platform` (and `--profile`).

| Config | Goal | Workload | Load | Phase length | Quote across families |
| ------ | ---- | -------- | ---- | ------------ | --------------------- |
| [`quick-smoke.yaml`](configs/normalized/quick-smoke.yaml) | does it work | kv-write | 50 TPS open loop, 5k keys | 30 s | sanity only |
| [`probe-sweep.yaml`](configs/normalized/probe-sweep.yaml) | **capacity knee + headline** | kv-write | probe → 9-step ladder → hold | ≤ ~16 min | yes, with caveats |
| [`throughput-scan.yaml`](configs/normalized/throughput-scan.yaml) | rough saturation band, fast | kv-write | ramp 100 → 10 000 TPS over 5 min, hold 2 min | 7 min | yes, with caveats |
| [`latency-profile.yaml`](configs/normalized/latency-profile.yaml) | clean tail latency below saturation | kv-write | 1000 TPS, 500k keys | 10 min | yes, with caveats |
| [`contention.yaml`](configs/normalized/contention.yaml) | MVCC / hot-key behaviour | transfer | 800 TPS, Zipfian 1.2 over 2000 keys | 5 min | yes, with caveats |
| [`read-profile.yaml`](configs/normalized/read-profile.yaml) | read path | kv-read | 1000 TPS, 500k keys | 5 min | **no**; needs a populated ledger |
| [`multi-client.yaml`](configs/normalized/multi-client.yaml) | fixed concurrency capacity | kv-mixed (50% reads) | closed loop, 256 workers | 5 min | **no** |

Config blocks:

| Block | Holds |
| ----- | ----- |
| `load` | workload, mode, rate/ramp/workers, distribution, seed, `sweep` |
| `metrics` | warmup, cooldown, output dir and format |
| `system_metrics` | 1 s sampling, Prometheus/cAdvisor URLs, container name filters |
| `adapter` | union of every platform's keys, filled from `${BENCH_ADAPTER_*}`; each adapter ignores keys it doesn't know |

`${VAR}` expands from the environment. There is no `configs/native/` today.
`latency-profile.yaml` suggests setting `target_tps` to about 60% of the knee you
measured.

Full detail and per-platform commands: [CONFIGS.md](CONFIGS.md).

## 9. Profiles and hardware

A profile ([`deploy/profiles/`](deploy/profiles)) is the resource budget the
platform under test gets. **Runs are comparable within a profile, never across
profiles.**

| Profile | Host | Platform budget | Load generator | Where the generator runs |
| ------- | ---- | --------------- | -------------- | ------------------------ |
| `local-small` | 13–14 GB, ≥ 11 cores | 8 CPU / 8 GB | 2 CPU | same host |
| `local` | 16 GB, 16 cores | 11 CPU / 11 GB | 2–3 CPU | same host |
| `gcp-small` | `n2-standard-16` | 13 CPU / 52 GB | 8 CPU | separate `n2-standard-8` VM |
| `gcp-full` | `n2-standard-32` | 28 CPU / 112 GB | 16 CPU | separate `n2-standard-16` VM |

`apply_budget` in [`deploy/docker/lib.sh`](deploy/docker/lib.sh) enforces the budget
right after `compose up`:
- CPU is split evenly across the platform's real containers.
- Memory is split by role weight, and swap is disabled.

The manifest records each container's limits.

Scale-out designs (Fabric-X, NeuChain) run far below their published ceilings on
the local profiles; full-scale numbers need `gcp-full`.

`scripts/preflight.sh <profile>` checks tooling, cores, total and *available* RAM,
swap, disk and sudo:

| Exit | Meaning |
| ---- | ------- |
| 0 | ready |
| 2 | runnable, with warnings |
| 1 | blocked; it names a profile that fits |

Full detail: [docs/HARDWARE-REQUIREMENTS.md](docs/HARDWARE-REQUIREMENTS.md).

## 10. Install and first run

```bash
git clone <this repo> bench && cd bench
sudo scripts/install-deps.sh        # Docker + compose, Go (from go.mod), git, jq, PyYAML, matplotlib
newgrp docker                       # if you were just added to the docker group
scripts/preflight.sh local-small    # or: local
make all smoke                      # build, unit tests, vet, and a mock benchmark with no network
make monitoring-up                  # optional: Prometheus :9090, Grafana :3000 (admin / bench), cAdvisor :8080
```

- **Supported systems:** Debian, Ubuntu, Fedora, RHEL/Rocky/Alma and Arch.
  `install-deps.sh` is safe to re-run.
- **NeuChain needs one image copied in.** `bench/neuchain:ev` is a patched build that
  no registry has. On a machine that has it: `make images-export` (writes
  `images/bench-neuchain-ev.tar.gz`, ~700 MB, plus `SHA256SUMS`). Copy `images/` over,
  then `make images-import`. `make images-export ALL=1` also saves every pinned public
  image, for machines without internet. Every other image is pulled or built by `up.sh`.
- **Expected smoke result:** about 200 TPS, `invariant_ok=true`.
- **Without monitoring:** runs still work, but you lose the per-run Prometheus charts.

Walkthrough: [docs/guides/quickstart.md](docs/guides/quickstart.md).

## Running every combination

Two rules apply everywhere:
- **One platform at a time.** fabric-cft, fabric-bft and drunix share ports
  7050/7051, so only one Fabric-family network can be up.
- **Use campaigns for comparisons.** `scripts/run-all.sh` (local) and
  `scripts/gcp-run.sh` (GCP) deploy fresh per config, isolate, capture and report.

### One platform, by hand

```bash
bash deploy/docker/<platform>/up.sh <profile>
set -a; source deploy/docker/<platform>/connection.env; set +a
./bin/benchrunner run --config configs/normalized/<config>.yaml --platform <platform> --profile <profile>
bash deploy/docker/<platform>/down.sh <profile>
```

| Platform | Before `up.sh` |
| -------- | -------------- |
| `fabric-cft`, `fabric-bft` | nothing; the first run downloads Fabric binaries and ~1 GB of images |
| `drunix` | nothing; clones `npci/drunix` at a pinned commit (override with `BENCH_DRUNIX_REPO` / `BENCH_DRUNIX_REF`) |
| `fabricx` | nothing; the first run compiles Arma + committer from source (several minutes). `connection.env` lists all four routers |
| `neuchain` | build the images first: `bash deploy/docker/neuchain/build.sh` plus manual setup ([guide](docs/neuchain/build-and-portability-guide.md)) |

Extra CLI flags:
- `--dry-run` prints the phase plan without generating load.
- `--caveat "text"` appends to the manifest.
- `--log-level debug` for more detail.

Per-platform copy-paste blocks: [COMMANDS.md](COMMANDS.md).

### A campaign: several configs × several platforms on one host

```bash
scripts/run-all.sh <config.yaml[,config.yaml...]> <profile> [platform ...]
# or
make bench PROFILE=<profile> PLATFORMS="<platforms>" CONFIGS=<a.yaml,b.yaml>
```

`make bench` defaults are `PROFILE=local`, `PLATFORMS="fabric-cft fabric-bft drunix"`
and `CONFIGS=quick-smoke,probe-sweep`. Fabric-X and NeuChain are opt-in. The
campaign runs preflight, rebuilds `benchrunner`, then loops config × platform:
isolate → deploy → run → capture → teardown. It writes a comparison of only this
campaign's runs and exits non-zero if any step failed.

The standard comparison (about 1.5 h on local-small for four platforms):

```bash
scripts/run-all.sh configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml \
    local-small fabric-cft fabric-bft drunix fabricx
```

### Every config × every platform on a local profile

```bash
scripts/run-all.sh \
configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml,configs/normalized/throughput-scan.yaml,configs/normalized/latency-profile.yaml,configs/normalized/contention.yaml,configs/normalized/multi-client.yaml \
    local-small fabric-cft fabric-bft drunix fabricx          # add neuchain once its images are built
```

About 50 min of load per platform plus ~5 min deploy/teardown per config, so roughly
**6–7 h for four platforms**. Repeat with `local` on a 16 GB host.

`read-profile.yaml` is left out on purpose. Every campaign step deploys an empty
ledger, and on the Fabric family a read of an absent key returns an empty value
that counts as success. Run it by hand on one deployment, right after
`latency-profile.yaml` (same 500k key space):

```bash
bash deploy/docker/<platform>/up.sh <profile>
set -a; source deploy/docker/<platform>/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform <platform> --profile <profile>
./bin/benchrunner run --config configs/normalized/read-profile.yaml    --platform <platform> --profile <profile>
bash deploy/docker/<platform>/down.sh <profile>
```

### The GCP profiles

Each platform gets its own freshly created platform VM plus a load-generator VM
(no public IPs, IAP SSH). The VMs are destroyed afterwards, also on failure or
Ctrl-C.

```bash
cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars
cp deploy/terraform/backend.hcl.example      deploy/terraform/backend.hcl
make gcp-plan GCP_PROFILE=gcp-full PLATFORMS="fabric-cft fabric-bft drunix fabricx"   # prints the plan
make gcp      GCP_PROFILE=gcp-full PLATFORMS="fabric-cft fabric-bft drunix fabricx"

# every config (make gcp runs only the defaults: quick-smoke + probe-sweep)
scripts/gcp-run.sh --project <id> --profile gcp-full \
  --platforms "fabric-cft fabric-bft drunix fabricx" \
  --configs "configs/normalized/quick-smoke.yaml configs/normalized/probe-sweep.yaml configs/normalized/throughput-scan.yaml configs/normalized/latency-profile.yaml configs/normalized/contention.yaml configs/normalized/multi-client.yaml" \
  --results-bucket <bucket>
```

Other options: `--local-state`, `--keep` (leave the last VMs up), `--tf-var k=v`,
`--dry-run`. Results come back into `./results`, in the same layout as local.
Setup, IAM, org policy and cost: [docs/guides/gcp-deployment.md](docs/guides/gcp-deployment.md).

### All profiles

A profile is bound to hardware, so "all profiles" means one campaign per host
class:
- `local-small` and `local`: `run-all.sh` on a machine that passes preflight for
  that profile.
- `gcp-small` and `gcp-full`: `gcp-run.sh`.

Compare runs only within the same profile. `benchrunner report --since` keeps each
campaign separate.

### Other ways to run

| Command | Use |
| ------- | --- |
| `benchrunner suite --configs <dir> --platforms a,b --profile p` | runs every config in a directory against **already running** networks. No deploy or isolation, so not for comparisons |
| `make up-all` / `make down-all` | monitoring + one Fabric-family network (+ fabricx/neuchain if built), for poking around |
| `make clean` / `make clean-images` | full local wipe (prompts) / also removes platform images |
| `make integration` | adapter integration tests against a live network |

## 12. Results: where they are and how to read them

### Where

```
results/
├── <platform>/<timestamp>/          one directory per benchrunner run
└── _campaigns/<UTC-id>-<profile>/   one directory per campaign
Final_runs/<date>-<profile>/         curated archive of a finished campaign (§13)
docs/reports/comparison.html         output of `make report`
```

**Per run**, `results/<platform>/<timestamp>/`:

| File | Contents |
| ---- | -------- |
| `summary.txt` | **start here**: header, per-phase table, errors, caveats, HEADLINE |
| `result.json` | `headline`, `phases[]` (full HDR snapshots, errors, verdicts), `saturation_tps`, `system_samples`, `manifest` |
| `manifest.json` | every fairness lever: platform + version, profile, `state_db` vs `state_db_requested`, `orderer_batch`, `crypto`, seed, key space, windows, per-container CPU/memory, `skipped_steps`, `caveats`, `harness_git_sha` |
| `phases.csv` | one row per phase: offered/confirmed TPS, fail rate, e2e p50/p95/p99/p99.9, submit/commit p50, goodput, send-gap p99, verdict |
| `monitoring-report.html` | CPU/memory charts from Prometheus for the run window |
| `run.log` | full log |
| `container-logs/` | only if a container failed (exit 3) |
| `error.txt` | only if adapter setup failed, with what to check |

**Per campaign**, `results/_campaigns/<id>/`:

| File | Contents |
| ---- | -------- |
| `SUMMARY.tsv` | one row per platform × config: `ok`, `no-measurement`, `deploy-failed`, `run-failed`, `container-failed`, with a reason |
| `comparison.html` | cross-platform report of this campaign only; rejected runs are listed with the reason |
| `run-all.log` (or `gcp-run.log`) | the whole campaign |
| `<platform>/<config>/` | `deploy.log`, `run.log`, `teardown.log`, and `capture/` (docker ps, inspect, container logs, events, host state) taken before teardown |

### How to read `summary.txt`

A real one from this campaign (fabric-bft, probe-sweep, local-small), trimmed:

```
profile:    local-small   state_db=leveldb
batch:      msgcount=100 timeout=1s preferred=2 MB
caveats:
  - sweep aborted early after 2 consecutive failed steps; 4 higher step(s) were never offered

phase           offered     conf_tps  goodput  fail_rate    e2e_p50    e2e_p99    gap_p99    com_p50  verdict
probe                10         10.0     100%     0.0000     608.26    1127.42       1.14     602.62
sweep-250           250        250.0     100%     0.0000     350.46     550.91       1.12     345.09  held
sweep-500           500        500.0     100%     0.0000     250.24     357.38       1.09     206.72  held
sweep-1000         1000        678.9      68%     0.3211   11747.33   11927.55       1.33   11616.26  failure rate 32.11% > 2.00%
hold                450        450.0     100%     0.0000     272.90     532.48       1.10     230.53
detected saturation: ~500 offered TPS
HEADLINE  confirmed_tps=450.0  fail_rate=0.0000  e2e p50/p99=272.90/532.48 ms  invariant_ok=true
```

1. **Header.** Check `profile`, `state_db` and `batch` match what you intend to
   compare, and read every **caveat**.
2. **probe.** This is floor latency. With 1 s block cutting a trickle waits for the
   timeout, so the probe can be slower than a busier step.
3. **sweep rows.** Find where `conf_tps` stops tracking `offered` and `e2e_p99`
   jumps; `verdict` names the rule that failed.
4. **detected saturation.** The highest step that held: the knee.
5. **HEADLINE.** The hold phase at 90% of the knee, sustained. **This is the number
   to quote**, with its caveats.
6. **Red flags** that make the run invalid:
   - `invariant_ok=false`
   - a send-gap warning
   - `FAILED RUN` / `HEADLINE none`
   - `PLATFORM FAILURE` lines

### Run book

Every `benchrunner run` (and `suite`) rebuilds `results/index.html`: an overview of
all runs plus one page per run with status, headline figures, throughput and
latency charts, platform CPU/memory, the phase table, errors, caveats, the full
manifest, and links to the run's files. Failed and aborted runs get pages too.

```bash
xdg-open results/index.html                                                   # open the book
make runbook                                                                  # rebuild by hand
./bin/benchrunner runbook --results-dir results --output /tmp/runs.html       # elsewhere
BENCH_RUNBOOK=0 ./bin/benchrunner run --config ...                            # skip the rebuild
```

### Compare and plot

```bash
column -t -s $'\t' results/_campaigns/<id>/SUMMARY.tsv                       # what ran, what failed
python3 scripts/plot.py results/<platform>/<ts>/phases.csv                    # curve.png: offered vs confirmed TPS + latency
python3 scripts/plot.py results/*/*/phases.csv                                # overlay platforms
./bin/benchrunner report --results-dir results --output docs/reports/comparison.html --since 2026-09-14
make report SINCE=24h
```

The report uses harness metrics only. It keeps normalized and native runs apart,
lists rejected runs with reasons, and reports replicates as median and min–max.
Live runs: Grafana at `http://localhost:3000`, dashboards `overview` and
`per-platform` ([`dashboards/grafana/`](dashboards/grafana)).

Full detail: [interpreting-results.md](docs/guides/interpreting-results.md) ·
[running-benchmarks.md](docs/guides/running-benchmarks.md#output) ·
[logs and failure captures](docs/guides/running-benchmarks.md#logs-and-failure-captures).

## Latest results

Campaign `20260914T173528Z-local-small`: quick-smoke + probe-sweep on fabric-cft,
fabric-bft, drunix and fabricx, 8 CPU / 8 GB per platform. The curated archive is
[`Final_runs/`](Final_runs).

Knee = highest sweep step that held. Headline = 5-min hold at 90% of the knee.
Latencies are end-to-end from scheduled send.

| Platform | Smoke (50 TPS) p50 / p99 | Knee | p50 / p99 at knee | Headline | Run |
| -------- | ------------------------ | ---- | ----------------- | -------- | --- |
| fabric-cft | 592 / 1113 ms | **1000 TPS** | 233 / 335 ms | none: peer panicked on a disk write two steps past the knee | [summary](Final_runs/2026-09-14-local-small/fabric-cft/probe-sweep/summary.txt) |
| fabric-bft | 662 / 1178 ms | **500 TPS** | 250 / 357 ms | **450 TPS, 0% fail, 273 / 532 ms** | [summary](Final_runs/2026-09-14-local-small/fabric-bft/probe-sweep/summary.txt) |
| drunix | 1184 / 2189 ms | **500 TPS** | 1719 / 2080 ms | none: Lite Peer OOM-killed two steps past the knee | [summary](Final_runs/2026-09-14-local-small/drunix/probe-sweep/summary.txt) |
| fabricx | 880 / 1524 ms | **3500 TPS** (p50 ≈ 0.5 s up to 2000) | 5411 / 6386 ms | none: Arma container OOM-killed two steps past the knee | [summary](Final_runs/2026-09-14-local-small/fabricx/probe-sweep/summary.txt) |

- **What can be quoted.** Only fabric-bft completed a headline. The other knees
  are valid measured steps, but those runs are rejected as complete measurements.
  In every case a container died during the second failing step, before the
  ladder's 2-step abort could reach the hold phase.
- **What cannot be claimed.** Drunix and Fabric-X carry the state-DB caveat, and
  Fabric-X is heavily starved at 8 CPU / 8 GB. None of this is a ranking of the
  platforms' real ceilings.

Where to look:
- **Archive:** [Final_runs/2026-09-14-local-small/README.md](Final_runs/2026-09-14-local-small/README.md)
  has the full analysis, caveats, and failure evidence.
- **Plots:** [overlay](Final_runs/2026-09-14-local-small/plots/overlay-probe-sweep.png)
  and per-platform curves.
- **Report:** [comparison.html](Final_runs/2026-09-14-local-small/comparison.html).

Host caveats for this campaign:
- 9.3 GB of the profile's 11 GB was free at start.
- Page cache was not dropped between runs (no passwordless sudo).
- Comparable only to other `local-small` runs.

## Troubleshooting

| Symptom | First move |
| ------- | ---------- |
| `adapter.<key> is required` | `set -a; source deploy/docker/<platform>/connection.env; set +a` in the same shell |
| `benchrunner` exit 3 / `container-failed` | `capture/inspect/<c>.json` (`ExitCode`, `OOMKilled`) and `capture/logs/<c>.log` |
| exit 137 + `OOMKilled=true` | memory cap: bigger profile, or check preflight |
| exit 2 + `panic:` | read the stack trace; an `input/output error` on write is the host disk |
| `no-measurement` / `FAILED RUN` | the platform stopped committing: phase errors in `run.log` |
| `deploy-failed` | tail of `deploy.log`, then `capture/logs/` |
| `port is already allocated` | another Fabric-family network is up: `make down-all` |
| `send-gap p99` warning | load generator starved: more `load_gen_cpus`, or reject |

The complete error lookup, exit codes, `SUMMARY.tsv` statuses and platform-specific
notes are in **[docs/README.md#troubleshooting](docs/README.md#troubleshooting)**.
Known open issues: [REMAINING-WORK.md](docs/REMAINING-WORK.md).

## 15. Repository layout, development, extending

```
cmd/benchrunner/     CLI: run | suite | report | setup | teardown | list
pkg/adapters/        PlatformAdapter interface + fabric, drunix, fabricx, neuchain, mock
pkg/workloads/       normalized workloads
pkg/loadgen/         key distributions, open/closed-loop generator (coordinated-omission safe)
pkg/metrics/         collector, HDR histograms, container sampler + failure detection
pkg/harness/         run config, profiles, engine, manifest, reporter, comparison report
pkg/monitoring/      Prometheus queries + per-run monitoring report
chaincodes/kvstore/  Go chaincode for the Fabric-family workloads
configs/normalized/  the cross-platform run definitions
deploy/docker/       per-platform up.sh/down.sh, shared lib.sh, monitoring stack
deploy/profiles/     resource budgets (and GCP machine types)
deploy/terraform/    GCP stack; shared/ for the results bucket
dashboards/grafana/  provisioned Grafana dashboards
scripts/             install-deps, preflight, run-all, gcp-run, capture, plot, up/down-all, clean
docs/                architecture, workloads, platforms, guides, decisions, REMAINING-WORK
results/             run and campaign output, plus the run book index.html
Final_runs/          curated archives of finished campaigns
```

```bash
make all       # build + test + vet
make lint      # shellcheck + terraform fmt/validate (falls back to their Docker images)
make help      # every target
```

CI (`.github/workflows/ci.yml`) runs:
- the Go checks
- a mock benchmark
- shellcheck and Terraform validation
- the installer in clean Debian, Ubuntu, Fedora, Rocky and Arch containers

To add a platform, implement and register an adapter, map the workloads, write
`up.sh`/`down.sh` that apply the budget and emit `connection.env`, then add
profile entries and docs: [docs/guides/adding-platform.md](docs/guides/adding-platform.md).
