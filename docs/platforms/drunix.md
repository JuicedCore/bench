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

Identical to Fabric for normalized runs: **LevelDB** (the engine forces it —
YugabyteDB is permitted only for `normalized: false` native runs), same orderer
batch params (shared anchor in `deploy/profiles/*.yaml`), same crypto.

The LP/CP split and Validation Service are *architectural*, not tuning knobs —
they stay on for every Drunix run and are what the comparison is measuring.

## Deploy

Source: `github.com/npci/drunix` (public). `up.sh` clones it by default; override
with `BENCH_DRUNIX_REPO` (git URL or local checkout) / `BENCH_DRUNIX_REF`.

```
bash deploy/docker/drunix/up.sh local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/quick-smoke.yaml --platform drunix
bash deploy/docker/drunix/down.sh
```

Verified layout (`npci/drunix @ main`):

| Role | Container | Endpoint | Operations/metrics |
| ---- | --------- | -------- | ------------------ |
| Lite Peer org1 (endorse + gateway) | `lp1.org1` | `localhost:7051` | `localhost:9444` |
| Committing Peer org1 | `cp.org1` | `localhost:7061` | `localhost:9454` |
| VSCC validation service org1 | `vs1.org1` | — | — |
| Orderer (Raft, 1 node) | `orderer` | `localhost:7050` | `localhost:9443` |

org2 mirrors on 9051 / 9061. The Lite Peer carries
`CORE_PEER_COMMITTINGPEER_ENDPOINT=dns:///peer1.org1.example.com:7061`, so its
gateway federates commit-status — the adapter connects to `:7051` only, no
second connection needed.

`network.sh` interface (fabric-samples style): `prereq`,
`up createChannel -c <ch> -s <db>`, `deployCC -c <ch> -ccn <n> -ccp <path> -ccl go`.

### State DB — Yugabyte-only by default

The shipped `compose/compose-test-net.yaml` **hardcodes**
`CORE_LEDGER_STATE_STATEDATABASE=sqldb` + YugabyteDB connection env on the peers;
there is no LevelDB/CouchDB compose variant. For normalized runs (LevelDB,
[adr-012](../decisions/adr-012-state-db-leveldb.md)) `up.sh` patches that file:
rewrites `sqldb` → `goleveldb` and strips the `CORE_LEDGER_STATE_SQLDBCONFIG_*`
lines (peer then uses goleveldb from `core.yaml`). `down.sh` restores the
original from a `.bench.bak`. Native Drunix runs keep Yugabyte and record it in
the manifest.
