# Real (non-mock) local runs per platform + Grafana viewing

## Context
Exact commands to bring up each platform's real network, run a benchmark against
it, tear it down, and view the results (including Grafana). For the full guide
(methodology, fairness, every config and profile, reading results), start at
[README.md](README.md). Run definitions are described in [CONFIGS.md](CONFIGS.md).

## Commands per platform

### 1) Fabric CFT (Raft) — fully working
```bash
bash deploy/docker/fabric-cft/up.sh local
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft
bash deploy/docker/fabric-cft/down.sh
```

### 2) Fabric BFT (SmartBFT, 4 orderers) — fully working
Same port range as CFT (mutually exclusive with it — bring one down before the other up):
```bash
bash deploy/docker/fabric-bft/up.sh local
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-bft
bash deploy/docker/fabric-bft/down.sh
```

### 3) Drunix — fully working (write-path bug fixed client-side)
```bash
export BENCH_DRUNIX_REPO=/path/to/drunix   # or leave unset, up.sh clones npci/drunix@main
bash deploy/docker/drunix/up.sh local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform drunix
bash deploy/docker/drunix/down.sh
```
**Was blocked, now fixed:** writes used to panic the Committing Peer. The
original diagnosis (an `aggregateOrgEnvelope -> txnEnv.LeanEnv is nil` WARN,
attributed to Drunix's "sparse block" format needing a lean envelope the
vanilla Fabric SDK doesn't produce) was wrong — that WARN is benign and logs on
every vanilla-format tx regardless of outcome. The real cause: Drunix's
YugabyteDB statedb writer force-casts every write value into a `JSONB` column
and panics on non-JSON values; `kvstore`'s `Put` writes a raw string. Fixed in
`pkg/adapters/drunix/valuecodec.go` (JSON-wraps values client-side, scoped to
this adapter only) and disclosed as a manifest caveat. Verified: `fail_rate:
0.0000, confirmed_tps: 50.0` on `quick-smoke.yaml`, both Committing Peers stay
up. Root cause and fix details in `docs/REMAINING-WORK.md`.

Rebuild `bin/benchrunner` (`go build -o bin/benchrunner ./cmd/benchrunner`)
before relying on this — a stale binary predating the fix will silently
reproduce the old panic.

### 4) Fabric-X (committer v1.0.5 / Arma orderer v1.0.6) — fully working
```bash
go build -o bin/benchrunner ./cmd/benchrunner
bash deploy/docker/fabricx/up.sh local-small
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabricx --profile local-small
bash deploy/docker/fabricx/down.sh
```
Builds from pinned `fabric-x-committer` / `fabric-x-orderer` source (not
`fabric-x-samples`) over the native gRPC path (adr-016), so the first `up.sh`
compiles for several minutes. Uses the shared normalized configs; there is no
`configs/native/` directory, so any `quick-smoke-fabricx.yaml` command is stale.

Verified on `local-small`: `confirmed_tps=50.0`, `fail_rate=0.0000`, e2e
p50/p99 ≈ 0.9 / 1.5 s. Expected caveats in the summary:
`state-db parity not held` (Fabric-X only runs on PostgreSQL) and a failed
native metrics scrape (informational only; harness numbers unaffected).

Things that look like errors but are not:
- The first 1–2 progress lines show `confirmed 0 tps`. A 1s block timeout means
  the first commits land about a second in.
- `up.sh` checks namespace registration in the state DB, not by loadgen's exit
  code. Upstream loadgen exits 1 (`receiver done: context canceled`) even when
  it succeeds.

Re-run `up.sh` after pulling adapter/deploy changes. `connection.env` must list
all four routers (`localhost:6022,6122,6222,6322`). An old one with only
`:6022` still runs, but every transaction then waits ~10s for Arma's
first-strike forward to the primary batcher. Any fabricx results produced
before this fix are invalid. Details: `docs/platforms/fabricx-integration.md`.

### 5) NeuChain — not runnable here yet (compute-gated, not code-gated)
```bash
bash deploy/docker/neuchain/build.sh    # ~45-90 min, ~25-30GB disk, 6-10GB RAM — compiles ~15 C++ libs from source
# then manual steps: extract config templates, init deterministic DB, generate RSA keys, fill docker-compose.yml placeholders
bash deploy/docker/neuchain/up.sh local
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform neuchain
```
**Why gated:** `deploy/docker/neuchain/docker-compose.yml` ships with literal `# PLACEHOLDER` values — `up.sh` prints a warning that you must fill them in from `iDC-NEU/NeuChain@ev` first. No prebuilt server binaries exist; `build.sh` does a from-source C++ build (protobuf/brpc/braft) that just wasn't run in this environment. The Go adapter itself is done and unit-tested — only the upstream server build is missing. Steps documented in `docs/REMAINING-WORK.md` §1.

### Bring up everything at once (optional)
```bash
make up-all FABRIC=fabric-cft     # or FABRIC=fabric-bft / drunix — Fabric variants share ports, only one at a time
make down-all                     # tears every platform + monitoring down
make clean                        # wipes containers/volumes/results (prompts); add --images for pulled images too
```
Note: `up-all` only starts fabricx/neuchain automatically if their images/caches already exist — otherwise call their `up.sh` directly as shown above.

## Viewing results

Every run writes `results/<platform>/<timestamp>/`:
- `summary.txt` — human-readable headline (TPS, p50/p99 latency, fail rate, `invariant_ok`)
- `result.json` — full histograms/phases/system samples
- `manifest.json` — fairness config, versions, git SHA, caveats
- `phases.csv` — per-phase rows (only if `metrics.output_format: csv`), feeds the plotter

```bash
cat results/fabric-cft/<timestamp>/summary.txt
python3 scripts/plot.py results/fabric-cft/<timestamp>/phases.csv     # -> curve.png
./bin/benchrunner runbook --results-dir results                      # -> results/index.html, one page per run (auto after every run)
./bin/benchrunner report --results-dir ./results --output docs/reports/comparison.html --since 2026-09-13
# --since scopes the report to one campaign. Runs that fail the rejection rules in
# docs/architecture/fairness-guarantees.md are listed with the reason, never averaged in;
# normalized and native runs are separate sections. Replicates report median and min-max.   # cross-platform HTML
```

### Grafana walkthrough (beginner)
1. Monitoring stack starts automatically with `up-all`, or standalone: `bash deploy/docker/monitoring/up.sh`
2. Open `http://localhost:3000` in a browser.
3. Log in: user `admin`, password `bench`.
4. Left sidebar → **Dashboards** — two are pre-provisioned:
   - **overview.json** — cross-platform summary (all platforms at once)
   - **per-platform.json** — drill into one platform's containers/metrics
5. Click a dashboard to open it. Top of the page has a time-range picker (top right, e.g. "Last 15 minutes") — widen it if you don't see data, since a benchmark run is usually short.
6. Panels show live container CPU/mem (via cAdvisor `:8080`) and host stats (node_exporter `:9100`); Fabric/Drunix native peer/orderer metrics (`:9443/metrics` or `:9444`) also show here but are informational only, not used for the actual TPS/latency numbers (those come from `benchrunner`'s own `result.json`/`summary.txt`).
7. Prometheus itself (the raw metrics database Grafana queries) is at `http://localhost:9090` if you want to run ad-hoc queries — usually not needed, Grafana dashboards cover it.
8. Tear down when done: `bash deploy/docker/monitoring/down.sh` (or it's included in `make down-all`).

## Verification
The fastest end-to-end check is section 1 (Fabric CFT): `summary.txt` should show
`fail_rate=0.0000` and `invariant_ok=true`. Then open Grafana per the walkthrough.
