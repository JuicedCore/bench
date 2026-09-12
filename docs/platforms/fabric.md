# Hyperledger Fabric (CFT + BFT)

## What it is built for

Enterprise consortium ledgers: known participants, modular trust, rich data
model, pluggable ordering. Fabric optimises for **flexibility and auditability**,
not raw TPS. Typical tuned throughput is a few thousand TPS; the architecture
trades latency for endorsement-based programmability.

## Transaction lifecycle (Execute-Order-Validate)

```
client ──proposal──▶ endorsing peers   (simulate chaincode, sign RW-set)   ── T1..T2
       ◀─endorsements─
client ──envelope──▶ orderer (Raft / SmartBFT)  (batch into a block)       ── T2
                     orderer ──block──▶ all peers
                     peers: VSCC (endorsement policy) + MVCC (RW-set) + commit ── T3
```

- **T1→T2** in this harness = proposal build + endorsement round + broadcast
  accept.
- **T2→T3** = ordering + block cut + validation + commit.
- A transaction can be **committed but invalid** (MVCC read conflict, endorsement
  policy failure). The adapter reports `Valid=false`; the harness counts it as a
  failure.

## Variants

| Variant | Branch/tag | Orderer | Nodes (local) |
| ------- | ---------- | ------- | ------------- |
| `fabric-cft` | `release-2.5` (pinned `v2.5.11`) | Raft (etcd/raft) | 1 peer, 1 orderer, 1 CA |
| `fabric-bft` | `v3.1.x` (pinned `v3.1.1`) | SmartBFT (3f+1, min 4) | 1 peer, 4 orderers, 1 CA |

`main` is deliberately **not** used — see
[adr-014](../decisions/adr-014-fabric-v3-release-pin.md). Fabric v3.0 GA'd in
2024; SmartBFT reached production-ready status in the v3.1.x line (v3.1.4,
Feb 2026).

## Adapter

`pkg/adapters/fabric`. Uses the **Fabric Gateway SDK** fine-grained flow —
`NewProposal → Endorse → Submit → Commit.Status` — so T2 (orderer accept) and T3
(commit) are distinct. The one-shot `contract.SubmitTransaction` is **not** used
because it blocks to commit and collapses the two timestamps.

- Writes → `Endorse` + `Submit`; finality via `Commit.StatusWithContext`.
- Reads → `Evaluate`; finality is immediate (`Valid=true`, T3 = now).
- `TxTransfer` → `Transfer(from, to, amount)` on the kvstore chaincode
  (read-modify-write two account keys).

## Normalized workload contract

`chaincodes/kvstore/kvstore.go` — `Put(key,value)`, `Get(key)`,
`Transfer(from,to,amount)`. Deliberately trivial so the measurement reflects the
platform path, not chaincode logic. Account balances are created lazily with a
large default so no seeding phase is needed.

## Fairness levers (held identical for normalized runs)

| Lever | Value | Where |
| ----- | ----- | ----- |
| State DB | LevelDB | `network.sh ... -s leveldb` |
| Orderer batch | `deploy/profiles/<profile>.yaml` `orderer_batch`, patched into `configtx.yaml` by `up.sh` | recorded in manifest |
| Endorsement policy | test-network default (`OR('Org1MSP.peer','Org2MSP.peer')` or single-org) | — |
| TLS | on | test-network default |
| Crypto | ECDSA P-256 / SHA-256, per-tx endorsement signature verification | `CryptoInfo` |

## Deploy

```
bash deploy/docker/fabric-cft/up.sh local     # clones fabric-samples @ v2.5.11, installs bins+images, brings up test-network, deploys kvstore
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft
bash deploy/docker/fabric-cft/down.sh
```

Native metrics: peer operations `:9443/metrics`, orderer `:9444/metrics`
(informational only).

## Known gotchas

- First `up.sh` downloads ~1 GB of Docker images; budget time.
- SmartBFT (`fabric-bft`) needs the 4-orderer set even locally — it is the most
  memory-hungry Fabric variant (`per_container` is tuned down in `local.yaml`).
- If `deployCC` fails on chaincode build, check the chaincode module builds:
  `cd chaincodes/kvstore && go build ./...` (uses `fabric-contract-api-go/v2`).
