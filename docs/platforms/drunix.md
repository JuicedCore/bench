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

Drunix source is not vendored. Point the deploy at a checkout or clone URL:

```
export BENCH_DRUNIX_REPO=/path/to/drunix        # or a git URL
export BENCH_DRUNIX_REF=main
bash deploy/docker/drunix/up.sh local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/quick-smoke.yaml --platform drunix
bash deploy/docker/drunix/down.sh
```

`up.sh` expects `drunix-network/test-network/network.sh` with fabric-samples-style
flags (`up createChannel -c <ch> -s <db>`, `deployCC ...`). If the Drunix repo
layout or flags differ, adjust `deploy/docker/drunix/up.sh` — the adapter and
config do not change.

Native metrics: Lite Peer `:9543`, Committing Peer `:9544`, Validation Service
`:9545` (informational only).

## Open questions for the implementer

- Confirm the exact `network.sh` flag names in the Drunix repo (endorsement org
  layout, LP/CP ports).
- Confirm whether commit events must be subscribed on the Committing Peer
  directly; if the LP gateway does not federate `Commit.Status`, the adapter
  needs a second connection (a small change isolated to `fabric.Adapter`).
