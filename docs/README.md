# Documentation index

This harness benchmarks four permissioned blockchains with three different
transaction models behind one measurement layer. Every design decision is
recorded here so the results are defensible.

**[REMAINING-WORK.md](REMAINING-WORK.md)** — what is finished vs. what is left,
and exactly why each remaining item needs a long compute build or GCP
credentials (with the pluggable entry point for each).

## Read in this order

1. [architecture/overview.md](architecture/overview.md) — the harness, the adapter
   model, how a run flows end to end.
2. [architecture/metrics-methodology.md](architecture/metrics-methodology.md) — the
   T1/T2/T3 timeline, coordinated-omission handling, warmup/cooldown, probe-and-sweep.
3. [architecture/fairness-guarantees.md](architecture/fairness-guarantees.md) — what
   is comparable across platforms and what is not, the crypto-parity table, the
   caveats attached to specific runs.
4. Platform notes: [fabric](platforms/fabric.md) · [fabric-x](platforms/fabric-x.md)
   · [neuchain](platforms/neuchain.md) · [drunix](platforms/drunix.md) ·
   [neuchain-client-implementation](platforms/neuchain-client-implementation.md).
5. Workloads: [normalized](workloads/normalized.md) ·
   [platform-native](workloads/platform-native.md) ·
   [mismatches](workloads/mismatches.md).
6. Guides: [quickstart](guides/quickstart.md) ·
   [running-benchmarks](guides/running-benchmarks.md) ·
   [adding-platform](guides/adding-platform.md) ·
   [gcp-deployment](guides/gcp-deployment.md) ·
   [interpreting-results](guides/interpreting-results.md).

## Decision records

| ADR | Title | One line |
| --- | ----- | -------- |
| [001](decisions/adr-001-go-harness.md) | Go for the whole harness | Single language, gRPC everywhere, no cgo. |
| [002](decisions/adr-002-neuchain-client-spike.md) | NeuChain client via proto spike | Time-boxed spike, then pure-Go or native-binary wrapper. |
| [003](decisions/adr-003-fabricx-fsc-view-and-rest.md) | Fabric-X via FSC view + REST | Custom KV view for normalized; Token SDK for native; REST node disclosed. |
| [004](decisions/adr-004-docker-compose.md) | Docker Compose locally | K8s overhead too high for a 16 GB host. |
| [005](decisions/adr-005-sequential-runs.md) | Sequential runs | One platform at a time on identical hardware. |
| [006](decisions/adr-006-hdr-histogram.md) | HDR histogram for latency | Correct percentiles at high throughput. |
| [007](decisions/adr-007-drunix-cft-only.md) | Drunix is CFT-only | Drunix inherits Fabric 2.5.x Raft; no BFT path. |
| [008](decisions/adr-008-neuchain-ev-only.md) | NeuChain `ev` branch only | The VLDB 2022 architecture; other branches out of scope. |
| [009](decisions/adr-009-workload-strategy.md) | Native + normalized workloads | Option C: both, documented. |
| [010](decisions/adr-010-mismatch-report.md) | Run mismatched workloads with caveats | Report the architectural mismatch explicitly. |
| [011](decisions/adr-011-orderer-batch-params.md) | Pin orderer batch params | Identical for normalized runs; tuned + logged for native. |
| [012](decisions/adr-012-state-db-leveldb.md) | LevelDB for normalized runs | CouchDB / YugabyteDB only in native runs. |
| [013](decisions/adr-013-config-parity-policy.md) | Config parity policy | Identical config normalized, per-platform tuned native. |
| [014](decisions/adr-014-fabric-v3-release-pin.md) | Pin a Fabric v3.1.x tag | `main` is a moving target; CFT stays on `release-2.5`. |
| [015](decisions/adr-015-phased-delivery.md) | Phased delivery | Ship a working deliverable after each platform. |

## Generated reports

Run outputs land in `results/<platform>/<timestamp>/` (manifest.json, result.json,
summary.txt, phases.csv). `benchrunner report` rolls them into an HTML comparison
under `docs/reports/`.
