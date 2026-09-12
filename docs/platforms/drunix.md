# Drunix (NPCI fork of Hyperledger Fabric)

## What it is

An enhanced fork of Hyperledger Fabric 2.5.x, open-sourced by NPCI (National
Payments Corporation of India) in mid-2026, targeting tokenisation and
multi-org enterprise networks. It keeps the Fabric Gateway SDK API surface, so
existing clients work unchanged.

## Architectural changes vs stock Fabric

| Change | Effect |
| ------ | ------ |
| **Peer split** — Lite Peer (endorsement only) + Committing Peer (validation + commit) | endorsement and commit scale independently |
| **Stateless Validation Service** | validation work offloaded from the committing peer |
| **YugabyteDB (SQL) state option** | alongside LevelDB and CouchDB; relational queries |

## Consensus — CFT only

Drunix inherits Fabric 2.5.x's Raft orderer. There is **no BFT path**. Drunix
therefore appears only in **CFT comparisons** (vs `fabric-cft`). It is excluded
from the stronger-ordering group (Fabric SmartBFT / Fabric-X Arma / NeuChain).
See [adr-007](../decisions/adr-007-drunix-cft-only.md).

## Adapter

`pkg/adapters/drunix` — a thin wrapper over `pkg/adapters/fabric`:

- Same Gateway SDK flow (`Endorse → Submit → Commit.Status`), same T1/T2/T3.
- `endorse_endpoint` points at the **Lite Peer**; `commit_endpoint` documents the
  **Committing Peer**. The Gateway SDK opens one connection to the Lite Peer's
  gateway, which federates commit-status from the Committing Peer.
- `CryptoInfo` is reported as Fabric's (ECDSA P-256 / SHA-256, per-tx endorsement
  verification) with a Drunix note.

## Fairness levers

Orderer batch params (shared anchor in `deploy/profiles/*.yaml`) and crypto are
identical to Fabric for normalized runs. **State DB is not**: the shipped
test-network's LevelDB path is experimental and known to break the VSCC path
(gated behind `BENCH_DRUNIX_FORCE_LEVELDB=1`, off by default), so `up.sh`
**always** deploys on YugabyteDB regardless of `normalized`. Normalized runs
disclose this as a manifest caveat rather than silently claiming LevelDB
parity with `fabric-cft`.

The LP/CP split and Validation Service are *architectural*, not tuning knobs —
they stay on for every Drunix run and are what the comparison is measuring.

## Deploy

Source: `github.com/npci/drunix` (public). `up.sh` clones it by default; override
with `BENCH_DRUNIX_REPO` (git URL or local checkout) / `BENCH_DRUNIX_REF`.

```
bash deploy/docker/drunix/up.sh local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform drunix
bash deploy/docker/drunix/down.sh
```

Verified layout (`npci/drunix @ main`):

| Role | Container | Endpoint | Operations/metrics |
| ---- | --------- | -------- | ------------------ |
| Lite Peer org1 (endorse + gateway) | `lp1.org1` | `localhost:7051` | `localhost:9444` |
| Committing Peer org1 | `cp.org1` | `localhost:7061` | `localhost:9454` |
| VSCC validation service org1 | `vs1.org1` | — | — |
| Orderer (Raft, 1 node) | `orderer` | `localhost:7050` | `localhost:9443` |

org2 mirrors on 9051 / 9061.

### Finality: the adapter watches the Committing Peer

The Gateway runs on the **Lite Peer**, which endorses and broadcasts but never
commits, so the Gateway SDK's `Commit.Status()` never fires. The drunix adapter
sets `UseCommitPeerEvents=true` (`pkg/adapters/fabric/commitpeer.go`): it opens a
second Gateway to the **Committing Peer** (`:7061`, shared TLS CA, SNI
`peer1.org1.example.com`) and reads tx validation from its
`FilteredBlockEvents` stream.

### Write-path bug — YugabyteDB requires JSON values (fixed, verified)

Application **write** transactions used to panic the Committing Peer on commit.
The original diagnosis blamed the orderer's `aggregateOrgEnvelope -> LeanEnv is
nil` WARN (Drunix's sparse-block optimisation expecting a lean envelope the
vanilla Fabric SDK doesn't produce) — that WARN is actually benign, logged on
every vanilla-format tx whether or not the block ends up committing. The real,
sole cause: Drunix's YugabyteDB statedb writer force-casts every non-lifecycle
write value into a `JSONB` column and panics the Committing Peer if the value
isn't valid JSON. The `kvstore` chaincode's `Put` writes a raw string, which
tripped this on every call. Full root cause, A/B evidence (upstream `basic`
sample chaincode survives; `kvstore`'s JSON-writing `Transfer` also survives)
in [../REMAINING-WORK.md](../REMAINING-WORK.md).

**Fixed client-side**, scoped to this adapter only:
`pkg/adapters/drunix/valuecodec.go` JSON-wraps `TxWrite` values before they
reach the chaincode, and `adapter.go`'s `Submit`/`Query` wrap/unwrap
transparently. The shared `kvstore` chaincode and the shared workload generator
are untouched — every other platform's payload is unaffected. Disclosed as a
manifest caveat (`cmd/benchrunner/main.go`) since Drunix's on-wire payload no
longer matches the shared workload's raw bytes for this reason.

**Verified**: fresh `up.sh local` bring-up,
`go test -tags integration -run Integration ./pkg/adapters/drunix/` passes,
both Committing Peers stay up, `LeanEnv is nil` still logs repeatedly as
harmless noise. Drunix write-benchmarks are unblocked.

`network.sh` interface (fabric-samples style): `prereq`,
`up createChannel -c <ch> -s <db>`, `deployCC -c <ch> -ccn <n> -ccp <path> -ccl go`.

### State DB — Yugabyte-only by default

The shipped `compose/compose-test-net.yaml` **hardcodes**
`CORE_LEDGER_STATE_STATEDATABASE=sqldb` + YugabyteDB connection env on the
peers; there is no LevelDB/CouchDB compose variant. `up.sh` **always** deploys
on YugabyteDB, normalized or not — the LevelDB patch (rewriting `sqldb` →
`goleveldb`, stripping `CORE_LEDGER_STATE_SQLDBCONFIG_*`) exists but also broke
the Committing Peer's VSCC path, so it's opt-in only via the experimental
`BENCH_DRUNIX_FORCE_LEVELDB=1` (see `up.sh:66-75`). Normalized runs disclose
the resulting state-DB mismatch vs `fabric-cft` as a manifest caveat instead of
claiming parity.
