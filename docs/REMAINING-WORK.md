# Remaining work

Status as of 2026-09-14. The harness, four platforms and the GCP tooling are
done and run live on `local-small`; what is left needs a long build, cloud
credentials, or an overload failure explained. The latest campaign's results are
in `Final_runs/` (linked from the top-level README).

| Area | State |
| ---- | ----- |
| Core harness (`pkg/…`), CLI, workloads, load gen, metrics, manifest, reporters | done; `go test ./...` green, `go vet` clean |
| Platform-failure detection (container exit / OOM kill, per-phase errors, report exclusion) | done; verified live on Fabric and Drunix |
| `fabric-cft`, `fabric-bft` | **benchmarked live** on local-small: smoke + probe-sweep, knee ~1000 / ~500 TPS. 2026-09-14: fabric-cft's peer panicked on a host disk write during overload ([§5](#5-fabric-cft-peer-panic-on-a-failed-disk-write)) |
| `drunix` | smoke live; probe-sweep held to 500 TPS, then platform containers died under overload: a Committing Peer on 2026-09-13, a Lite Peer OOM-killed on 2026-09-14 (see [§3](#3-drunix-committing-peer-exit-under-overload)) |
| `fabricx` | **live-verified** on local-small: smoke + probe-sweep on the native gRPC path ([§4](#4-fabric-x--live-on-the-native-grpc-path)) |
| `neuchain` | live on local-small with a **patched** image: smoke + probe-sweep to 10k TPS; every node segfaults at epoch 10 000 (upstream bug), so runs longer than ~10 min die ([§1](#1-neuchain--live-patched-image-10-000-epoch-crash)) |
| Portability | `scripts/install-deps.sh` tested in clean Debian 12, Ubuntu 24.04, Fedora 42, Rocky 9, Arch; upstream sources pinned by commit/tag |
| GCP | Terraform + `scripts/gcp-run.sh` written and validated; **not yet applied to a real project** ([§2](#2-gcp-campaign--credential-gated)) |
| CI | `.github/workflows/ci.yml`; no git remote configured yet, so it has not run |

---

## 1. NeuChain — live (patched image), 10 000-epoch crash

### State

`bench/neuchain:ev` is a retag of a patched build from a separate NeuChain harness
([deploy/docker/neuchain/patched/README.md](../deploy/docker/neuchain/patched/README.md)).
Deploy, crypto-init, adapter and harness work end to end on local-small
(2026-09-15, `results/neuchain/20260915-123252`):

| phase | confirmed TPS | fail | e2e p99 |
| ----- | ------------- | ---- | ------- |
| sweep-1000 | 999.9 | 0.01 % | 188 ms |
| sweep-2000 | 1999.3 | 0.03 % | 186 ms |
| sweep-5000 | 4996.9 | 0.06 % | 190 ms |
| sweep-7500 | 7492.5 | 0.10 % | 193 ms |
| sweep-10000 | 9982.1 | 0.18 % | 366 ms |
| hold (9000) | 0 | 100 % | all 4 nodes exited 139 at hold start |

Adapter fixes made to get there: YCSB chaincode encoding (`write`/`read` headers,
`[key, record]` args; the old `ycsb` header aborted every tx), legacy
`DES-EDE3-OFB` key decryption, poller starting at tip+1, and a heartbeat (NeuChain
emits a block only when later epochs carry traffic).

### Blocker: nodes crash at epoch 10 000

`src/block_server/database/block_broadcaster.cpp` sizes `validatedBlockNumber` to
10 000 and indexes it by epoch number without a bounds check. Every node
segfaults together once the chain reaches epoch ~10 000, whatever the load.
Epochs run ~15/s under load (fewer when idle), so a network lives ~10-30 min
from `up.sh`. The paper's 150 s runs never reach it; `probe-sweep` (~17 min) does.
The patched image has the same bug.

Options:

1. Patch it (e.g. grow the vector or use a map) and rebuild. The build is
   45-90 min, ~25-30 GB disk (34 GB free on the dev host). This is one more
   patch over upstream, to disclose like the others.
2. No rebuild: bring the network up fresh (`down.sh` + `up.sh`) right before
   each run, and keep runs under ~10 min (drop or shorten the `hold` phase for
   NeuChain). The sweep phases up to 10k TPS fit.

### Decided (2026-09-15)

No rebuild for now. NeuChain runs must finish within ~10 minutes of `up.sh`
(bring the network up fresh before each run; `quick-smoke` and sweep steps fit,
`probe-sweep`'s 5-minute hold does not). Other machines get the image with
`make images-export` here and `make images-import` there.

### Still pending

- Clean upstream build (`deploy/docker/neuchain/build.sh`) to replace the
  patched image; unpatched upstream crashed in the other harness.
- Cross-check harness TPS against NeuChain's `transaction_manager_impl.cpp`
  commit counts in `node-logs/`.

## 2. GCP campaign — CREDENTIAL-GATED

### What exists

`scripts/gcp-run.sh` with the Terraform stack in `deploy/terraform/`: per platform
a fresh platform VM and load-generator VM, private networking with IAP and Cloud
NAT, OS Login, Shielded VM, a least-privilege service account, GCS state, remote
Docker over mutual TLS, results pulled back and optionally archived to GCS, and
destroy on exit. Setup and usage: [guides/gcp-deployment.md](guides/gcp-deployment.md).

Verified without a project: `terraform validate` on both stacks, `fmt` clean,
provider lock for four OS/arch pairs, shellcheck clean, `--dry-run` plans, and
the startup installer in five distro containers.

### Unverified until the first real campaign

- The startup script completing on the Debian 12 image under a real metadata
  server, and the `/var/lib/bench/ready` wait.
- `gcloud compute ssh --tunnel-through-iap` with OS Login and `usermod -aG docker`
  taking effect on the next session.
- The mutual-TLS Docker API: dockerd restarting with the drop-in, and
  `docker stats` / `docker events` from the load generator.
- Crypto material copied to the load generator, and the Fabric TLS server-name
  override working against the platform VM's private IP.

The first campaign should be one platform with quick-smoke only:

```bash
scripts/gcp-run.sh --project "$PROJECT" --profile gcp-small --platforms fabric-cft \
  --configs configs/normalized/quick-smoke.yaml --local-state --keep
```

### Known limit: one platform VM

Each platform runs on one VM, because the deploy scripts are single-Docker-host.
That is fair across platforms but understates scale-out designs (Fabric-X,
NeuChain) relative to their published multi-host figures. Multi-host deploys need
per-platform work: overlay networking, crypto distribution, and node placement.

---

## 3. Drunix Committing Peer exit under overload

On local-small (2026-09-13, `results/drunix/20260913-141848`) Drunix held 100–500
TPS, failed 1000 and 2000, and `cp.org2` exited with code 2 during sweep-2000. The
kernel logged no OOM kill, so it is not the memory limit; exit code 2 is typically
a Go panic. The harness stopped the run as designed. Its logs were removed at
teardown, so the cause is unknown.

Next step: capture the last log lines of a failed container into `result.json`
before teardown, then rerun the Drunix probe-sweep. On GCP, `--keep` leaves the
VM up with the container for inspection.

Campaigns now capture container logs before teardown, so the next
reproduction's cause will be in
`results/_campaigns/<id>/drunix/<config>/capture/logs/cp.org2.log` and
`results/drunix/<ts>/container-logs/cp.org2.log`.

**2026-09-14 reproduction, different container.** Same shape: 100–500 held, 1000
failed (47% failures, `not sent ... in flight`), then during sweep-2000 `lp1.org1`
(a Lite Peer) was **OOM-killed at its memory limit** (exit 137) and its chaincode
container exited. Capture:
`results/_campaigns/20260914T173528Z-local-small/drunix/probe-sweep/capture/`.
Past the knee the in-flight backlog grows until the Lite Peer exceeds its share of
the 8 GB budget (`memory_role` weights in `deploy/docker/lib.sh`). Below the knee
Drunix is stable, and the knee and hold figures are unaffected, but the run is
recorded as `container-failed` and has no headline. Open question: whether the
Lite Peer role deserves the peer weight (4) rather than being treated as the
same role as a Committing Peer.

## 3a. Drunix write path — resolved (history)

This is a **third category** — not compute, not GCP, but a genuine upstream
gap in `github.com/npci/drunix`.

### What works

The full Drunix deploy was brought up end to end (`deploy/docker/drunix/up.sh`,
all fixes committed): 7 `npcioss/drunix-*` containers (Lite Peer / Committing
Peer / stateless VSCC × 2 orgs + Raft orderer), 2 KeyDB containers (the
`drunix-peer` image mandates a KeyDB KVStore; upstream only bundles it with the
Yugabyte compose, so `up.sh` starts it separately), YugabyteDB × 2, channel
`mychannel` joined, blocks committing, `kvstore` chaincode **packaged, installed,
approved and committed on both Committing Peers** (VALID). The chaincode
containers run. The Fabric-family lifecycle path (vanilla envelopes) is fully
functional through Drunix.

### What was blocked (now fixed — see below)

Application **write** transactions submitted through the stock
`hyperledger/fabric-gateway` SDK (which the drunix adapter reuses from the fabric
adapter) reached the orderer but the **Committing Peer panicked on commit**:

```
[orderer] WARN [common.sparseblock] aggregateOrgEnvelope -> txnEnv.LeanEnv is nil it could be vanilla-format txn   (x hundreds)
[cp.org1] panic  github.com/npci/drunix/core/ledger/kvledger.(*kvLedger).commit
                 github.com/npci/drunix/gossip/privdata.(*coordinator).StoreBlock
                 github.com/npci/drunix/gossip/state.(*GossipStateProviderImpl).commitBlock
```

Every tx: `submitted 1101, errored 0, timed_out 1101` (Submit/endorse/broadcast
succeed; the CP crashed before the block committed, so finality never arrived).

**This diagnosis was wrong.** The `aggregateOrgEnvelope -> LeanEnv is nil` WARN
is benign — it fires on *every* vanilla-format transaction (confirmed: 13 hits
in a single passing run below) and never by itself stops a block from
committing. The identical `kvLedger.commit -> StoreBlock -> commitBlock` stack
was misattributed to it. The real, sole cause was the statedb bug documented
next — once that's fixed, writes through the stock Gateway SDK commit cleanly,
`LeanEnv is nil` WARNs and all. There is no separate lean-envelope blocker.

### Root cause — YugabyteDB statedb requires JSON values

Manually reproduced writes through raw `network.sh`/`peer` CLI (bypassing the
harness) to confirm the blocker, and got a clearer panic than what was
originally documented above:

```
ERROR: invalid input syntax for type json (SQLSTATE 22P02)
[sqldb] func1 -> Error in batch sql write. err:ERROR: invalid input syntax for type json (SQLSTATE 22P02)
panic: error during commit to txmgr: ERROR: invalid input syntax for type json (SQLSTATE 22P02)
        .../core/ledger/kvledger.(*kvLedger).commit
        .../gossip/privdata.(*coordinator).StoreBlock
```

`core/ledger/kvledger/txmgmt/statedb/statesqldb/sql_client.go` in the drunix
source (`Set`, ~lines 216-223) force-casts every non-lifecycle write value into
a `JSONB` column, regardless of what the chaincode actually wrote:

```go
data["db_value"] = value.Value
if !strings.HasSuffix(table, "_lifecycle") && !strings.Contains(table, "$$h") {
    err := json.Unmarshal(value.Value, &data)
    if err != nil {
        // logger.Warningf("failed to unmarshal data, %v", err)   <- swallowed
    }
    data["db_value"] = datatypes.JSON(value.Value)   // <- forced regardless
}
```

The unmarshal error is swallowed, so a non-JSON value still gets forced into a
`JSON`-typed column, YugabyteDB rejects the `INSERT` at the SQL layer, and the
panic is uncaught.

**Confirmed via a controlled A/B on the same live network:** the upstream
`asset-transfer-basic` ("basic") sample chaincode's `InitLedger` — whose values
are JSON-marshaled `Asset` structs — commits fine. Our `kvstore` chaincode's
`Put(key, value)` — a raw string, also perfectly valid Fabric usage — panicked
the CP every time. `kvstore`'s `Transfer`/`setBalance` already JSON-marshals
(`acct{Balance: bal}`), so only the plain `Put` path was affected. This hits
every Drunix write, since `up.sh` always deploys on YugabyteDB regardless of
`normalized` (see `up.sh:66-75` — LevelDB is gated behind an experimental,
known-broken `BENCH_DRUNIX_FORCE_LEVELDB=1`).

### Fix (committed) — client-side JSON-wrap, scoped to the drunix adapter

`pkg/adapters/drunix/valuecodec.go` JSON-wraps `TxWrite` values client-side
before they reach the chaincode's `Put`, and `pkg/adapters/drunix/adapter.go`'s
`Submit`/`Query` wrap/unwrap transparently. Scoped to the drunix adapter only —
the shared `kvstore` chaincode and the shared workload generator
(`pkg/workloads`) are untouched, so every other platform's payload is
unaffected. Disclosed as a manifest caveat in `cmd/benchrunner/main.go` since
the on-wire payload no longer matches the shared workload's raw bytes for
Drunix specifically.

**Verified end-to-end through the real Go adapter** (Gateway SDK, not the raw
`peer` CLI used for the initial repro): fresh `up.sh local` bring-up,
`go test -tags integration -run Integration ./pkg/adapters/drunix/` —
`PASS (2.74s)`, both Committing Peers stayed `Up` afterward, no panic in either
CP's logs. `aggregateOrgEnvelope -> LeanEnv is nil` still logs 13 times during
the run (confirming it's harmless noise), and the write reaches full finality.
Drunix write-benchmarks are unblocked.

### What is in place

`pkg/adapters/fabric/commitpeer.go` already reads finality from the Committing
Peer's `FilteredBlockEvents` stream (the drunix adapter sets
`UseCommitPeerEvents=true`) — needed because the LP-hosted Gateway's
`Commit.Status()` never fires (the LP does not commit). Combined with the
value-codec fix above, the full write → finality path now works.

### Verify

```
bash deploy/docker/drunix/down.sh
bash deploy/docker/drunix/up.sh local        # brings up on YugabyteDB (shipped default)
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform drunix
go test -tags integration -run Integration ./pkg/adapters/drunix/
```

### Also fixed along the way (committed, no external dependency)

- `deploy/docker/drunix/up.sh`: default to YugabyteDB (Drunix's tested config);
  the LevelDB compose patch also broke the CP's VSCC hostname wiring and is now
  opt-in via `BENCH_DRUNIX_FORCE_LEVELDB=1`. Manifest caveat on normalized runs.
- KeyDB started separately; `npcioss/drunix-{ccenv,baseos}` pulled;
  `CORE_PEER_CLIENT_CONNTIMEOUT=120s` + `deployCC -r 10 -d 10` for the slower
  LP→orderer→CP→VSCC lifecycle; cert-path globbing.

---

## 4. Fabric-X — live on the native gRPC path

The REST/token integration is gone. It was built on `fabric-x-samples/tokens`, a
Token-SDK demo app, so it would have measured Fabric Smart Client and ZKP
generation rather than Fabric-X. Post-mortem in
[adr-003](decisions/adr-003-fabricx-fsc-view-and-rest.md); the replacement is
[adr-016](decisions/adr-016-fabricx-native-grpc.md), with the research trail and
every bring-up gotcha in
[platforms/fabricx-integration.md](platforms/fabricx-integration.md).

**Verified 2026-09-14 on local-small:**
- `up.sh` builds Arma + committer from pinned tags (committer v1.0.5, orderer
  v1.0.6), registers the namespace, and checks the key in the state DB.
- The adapter broadcasts to all four routers and reads finality and
  `committerpb.Status` codes from the sidecar.
- quick-smoke: 50 TPS, 0% failures, e2e p50 ~0.9 s.
- probe-sweep: held every step up to 3500 TPS (p50 ≈ 0.5 s up to 2000, 5.4 s at
  3500), failed 5000, then the `fabricx-arma` container, holding all 16 Arma
  processes, was OOM-killed at its 2 GB share during 7500. No headline.
  `Final_runs/2026-09-14-local-small/`.

Fabric-X results recorded before 2026-09-14 are invalid: single-router submits
added ~10 s to every transaction.

### What is left

- **Scale.** `local-small` starves a scale-out design; meaningful Fabric-X
  ceilings need `gcp-full` (§2).
- **Block byte limits.** They are not applied to Arma (normalization-status.md).
- **Native workload.** There is none yet: Token SDK issue/transfer would need a
  token client on the gRPC path.

### Out of scope

`kv-*` through an FSC view. The normalized KV workloads map onto namespace
read/write sets directly, which is both simpler and closer to what the platform
actually does.

## 5. fabric-cft peer panic on a failed disk write

2026-09-14, probe-sweep on local-small. The sweep held 100–1000 TPS and saturated
at ~1070 TPS at offered 2000. Then `peer0.org2` panicked:
`Cannot commit block to the ledger due to write .../blockfile_000011: input/output error`.

This was not the memory limit (it used ~374 MB of 2.73 GB, no OOM kill), not disk
space (34 GB free), and nothing was logged by the kernel (`journalctl -k`). Root
filesystem is btrfs with Docker's overlay snapshotter. Cause unknown. The knee
measured before it is unaffected; the run is `container-failed`. Capture:
`results/_campaigns/20260914T173528Z-local-small/fabric-cft/probe-sweep/capture/`.
Rerun to see whether it reproduces.

## Local lifecycle scripts

| Command | Effect |
| ------- | ------ |
| `make up-all` / `scripts/up-all.sh local [fabric-variant]` | monitoring + one Fabric-family net (they share ports) + `fabricx`/`neuchain` if their images exist |
| `make down-all` / `scripts/down-all.sh local` | stop + remove every platform + monitoring; keeps images, caches, results |
| `make clean` / `scripts/clean.sh` | down-all + generated chaincode images, dangling volumes, `.cache/` clones, `connection.env`, `results/*`, `docs/reports/*` (prompts; `-y` to skip) |
| `make clean-images` / `scripts/clean.sh --images` | clean + pulled platform images (Fabric, `npcioss/drunix-*`, yugabyte, keydb, `bench/neuchain*`, `bench/fabricx`) |

Never touches containers / images / volumes the harness did not create.

## Quick "is everything else good?" checklist

```
go build ./...                          # clean
go vet ./...                            # clean
go build -tags integration ./...        # clean
go test ./pkg/...                       # all pass
cd chaincodes/kvstore && go build ./... # clean
bash -n deploy/docker/**/*.sh scripts/*.sh   # clean
./bin/benchrunner list                  # drunix, fabric-bft, fabric-cft, fabricx, mock, neuchain
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform mock   # end-to-end, no network
```

Live, reproducible today with only Docker + this repo:

```
bash deploy/docker/fabric-cft/up.sh local
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft
```
