# Harness architecture

## The problem

Fabric, Fabric-X, NeuChain and Drunix have three transaction models:

| Model | Platforms | Shape |
| ----- | --------- | ----- |
| EOV with chaincode | Fabric (CFT + BFT), Drunix | Client → endorse → order → validate → commit |
| EOV without chaincode | Fabric-X | FSC view/session negotiation + Token SDK, Arma ordering |
| Ordering-free EV | NeuChain | Deterministic execution, no dedicated ordering service |

A benchmark that submits a "chaincode invoke" everywhere cannot run on Fabric-X or
NeuChain. A benchmark that measures each platform's own client tool cannot compare
them. The harness solves this with **one adapter interface** and **normalized
workloads** implemented natively on each platform.

## Layers

```
                 ┌──────────────────────────────────────────────┐
 configs/*.yaml  │  benchrunner (cmd/benchrunner)                │
 profiles/*.yaml │    parse run config + resource profile        │
                 └───────────────┬──────────────────────────────┘
                                 │
                 ┌───────────────▼──────────────────────────────┐
                 │  harness.Engine (pkg/harness)                 │
                 │    build workload, build phases, write        │
                 │    manifest + results                         │
                 └───────┬───────────────────────┬──────────────┘
                         │                       │
          ┌──────────────▼────────┐   ┌──────────▼───────────────┐
          │ loadgen.Generator     │   │ metrics.Collector        │
          │  open / closed loop   │   │  T1/T2/T3 per tx,        │
          │  key distributions    │   │  HDR histograms,         │
          │  coordinated-omission │   │  window + invariant      │
          │  safe scheduling      │   │  check                   │
          └──────────┬────────────┘   └──────────────────────────┘
                     │
        ┌────────────▼─────────────┐
        │ adapters.PlatformAdapter │  Submit → WaitForFinality → Query
        ├──────────────────────────┤
        │ fabric  drunix  fabricx  │
        │ neuchain   mock          │
        └──────────────────────────┘
```

## The adapter contract

`pkg/adapters/adapter.go`. Every platform implements:

- `Setup(ctx, AdapterConfig)` / `Teardown(ctx)` — client-side only; never deploys
  or tears down the network.
- `Submit(ctx, *Transaction) (*SubmitResult, error)` — returns at **T2**
  (platform acknowledged receipt), never blocks to finality. Adapters MUST use
  the platform's async path (e.g. Fabric Gateway `Endorse → Submit → Commit`) so
  T2 and T3 stay distinct.
- `WaitForFinality(ctx, txID, timeout) (*FinalityResult, error)` — blocks to
  **T3** (committed in a validated block). A committed-but-invalid tx returns
  `Valid=false` and no error.
- `Query(ctx, key)` — state read for workload verification, off the hot path.
- `MetricsEndpoint()` — the platform's own Prometheus URL, or `""`. Informational
  only.

Optional capabilities (`VersionReporter`, `CryptoReporter`) feed the run manifest.

Adapters self-register in `init()` via `adapters.Register(name, factory)`, so the
harness never imports a platform SDK — only `cmd/benchrunner` blank-imports the
adapter packages.

## A run, start to finish

1. `LoadRunConfig` parses `configs/<x>.yaml`; `LoadProfile` parses
   `deploy/profiles/<profile>.yaml` and resolves the platform topology.
2. The engine builds a `workloads.Workload` (deterministic, seeded) and a
   `Manifest` capturing every fairness lever (state DB, orderer batch params,
   crypto config, seed, key space, windows, resource limits, git SHA).
3. `adapters.New(platform)` → `adapter.Setup`.
4. `buildPhases` produces either a single load phase or, for probe-and-sweep,
   `probe → sweep-N… → hold`.
5. For each phase the `loadgen.Generator` drives the adapter; the
   `metrics.Collector` records T1/T2/T3 per transaction.
6. Each phase is aggregated over `[phaseStart+warmup, phaseEnd-cooldown)`,
   windowed on **scheduled send time**.
7. Results are written to `results/<platform>/<timestamp>/`:
   `manifest.json`, `result.json`, `summary.txt`, and `phases.csv` when
   `output_format: csv`.
8. `benchrunner report` scans a results tree and emits an HTML comparison using
   **harness metrics only**.

## What lives where

| Path | Responsibility |
| ---- | -------------- |
| `pkg/adapters/` | the interface, the registry, per-platform adapters, `mock` |
| `pkg/workloads/` | normalized workloads (`kv-write`, `kv-read`, `kv-mixed`, `transfer`) |
| `pkg/loadgen/` | key distributions, read/write mix, open/closed-loop generator |
| `pkg/metrics/` | per-tx collector, HDR histograms, native scrape, `docker stats` sampler |
| `pkg/harness/` | run config, resource profile, engine, manifest, reporter |
| `cmd/benchrunner/` | CLI: `run`, `suite`, `report`, `setup`, `teardown`, `list` |
| `deploy/docker/` | per-platform Compose topologies + `up.sh`/`down.sh` |
| `deploy/profiles/` | resource budgets and topologies (`local`, `gcp-small`, `gcp-full`) |
| `chaincodes/kvstore/` | the Go chaincode for Fabric / Drunix normalized workloads |
