# Real (non-mock) local runs per platform + Grafana viewing

## Context
User wants exact commands to run REAL local networks (not the `platform: mock` no-network smoke test) for five platforms in this repo, run separately, plus how to view results — including a beginner walkthrough of Grafana. Repo already has per-platform deploy scripts (`deploy/docker/<platform>/{up.sh,down.sh}`) and a `benchrunner` CLI. One platform is gated (NeuChain needs a slow local C++ build); Drunix's write path was previously blocked but is now fixed (client-side JSON-wrap workaround for a YugabyteDB statedb bug, disclosed as a manifest caveat) and verified working. This is a reference answer, not a code change — no files will be modified.

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

### 4) Fabric-X — version-skew root cause fixed; blocked on namespace bootstrap
```bash
bash deploy/docker/fabricx/up.sh local
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/native/quick-smoke-fabricx.yaml --platform fabricx
bash deploy/docker/fabricx/down.sh
```
**Currently still fails `endorser/init`** — `up.sh` will run to completion showing
`relation "ns_token_namespace" does not exist`, a *different* error than
before. `kv-write`/`kv-read` remain separately blocked by the `/kv` FSC view
stub (`deploy/docker/fabricx/kvview/`, returns `501`).

Three real bugs were found and fixed in `up.sh` (stale `FXS_REF` never
re-cloning, `endorser/init` silently swallowing failures, `tokens/`'s own
inventory missing base-path variables its roles need) — the devnet now comes
up cleanly and reliably every time. The original blocker
(`unknown service committerpb.QueryService`, a version-skew bug inside
`fabric-x-samples` itself between its bundled endorser app and the
`fabric-x-committer:0.1.7` its Ansible role deploys) is **fixed and confirmed**:
`up.sh` now builds a replacement committer+orderer backend from source
(`deploy/docker/fabricx/backend/`) instead of using the Ansible-deployed one.
What's left is a **new, separate, well-evidenced blocker**: bootstrapping a
namespace on this from-scratch network fails signature validation
(`ABORTED_SIGNATURE_INVALID`) — full repro trail, every identity/command tried,
and exact source pointers for whoever picks this up:
[`docs/platforms/fabricx-comparability.md`](docs/platforms/fabricx-comparability.md)
("Lever C" section).

Five transfer-workload run configs (`configs/native/{contention,probe-sweep,throughput-scan,latency-profile,multi-client}-fabricx.yaml`) are written, matching the pattern of the Fabric-family configs, ready to use once this is fixed — but **none could be live-verified**, and neither could the pre-existing `quick-smoke-fabricx.yaml`.

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
./bin/benchrunner report --results-dir ./results --output docs/reports/comparison.html   # cross-platform HTML
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
No code changes — this is a reference of existing scripts. To confirm accuracy before relying on it, the user can run the Fabric CFT commands (the fully-working path) end to end and check `results/fabric-cft/<timestamp>/summary.txt` gets produced, then open Grafana per the walkthrough.
