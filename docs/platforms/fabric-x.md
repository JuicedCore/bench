# Fabric-X

## What it is

A ground-up re-architecture of Hyperledger Fabric for ultra-scalability
(LF Decentralized Trust). It decomposes the monolithic peer into independently
scalable microservices — **endorser, validator, committer** — and replaces the
Raft/SmartBFT orderer with **Arma**, a sharded BFT ordering service. Published
evaluation: **>200,000 TPS** on a large cluster (CBDC benchmark).

Repos: `github.com/hyperledger/fabric-x`, `github.com/hyperledger/fabric-x-orderer`,
samples: `github.com/hyperledger/fabric-x-samples`.

## No chaincode

Fabric-X replaces the chaincode execution model with **peer-to-peer transaction
negotiation** built on **Fabric-Smart-Client (FSC)** views/sessions and the
**Fabric-Token-SDK** (UTXO model). There is no `PutState`/`GetState` chaincode to
deploy. Confirmed by the LF Fabric-X roadmap.

Consequence: the *same* smart contract cannot be deployed across all four
platforms. The harness defines **functionally equivalent workloads** implemented
natively per platform ([adr-009](../decisions/adr-009-workload-strategy.md)).

## Arma ordering

Four server roles: **routers** (accept + dispatch), **batchers** (form batches),
**consenters** (BFT-order compact *digests*, not payloads), **assemblers**
(reconstruct full blocks). Ordering digests instead of payloads is where the
throughput comes from.

## Real REST API (verified — `fabric-x-samples/tokens/swagger.yaml`)

Token-only. **No KV route.**

| Route | Port | Body | Reply | Synchronous? |
| ----- | ---- | ---- | ----- | ------------ |
| `POST /issuer/issue` | 9100 | `TransferRequest{amount{code,value},counterparty{node,account},message?}` | `{message,payload:"<txid>"}` | **to finality** |
| `POST /owner/accounts/{id}/transfer` | 9500 / 9600 | same | same | **to finality** |
| `POST /owner/accounts/{id}/redeem` | 9500 | `RedeemRequest` | `{message,payload:"<txid>"}` | to finality |
| `GET /owner/accounts/{id}?code=<type>` | 9500 | — | `{message,payload:Account{id,balance[]}}` | — |
| `POST /endorser/init` | 9300 | — | health | one-time network init |
| `GET /healthz` `/readyz` | all | — | `{message}` | — |

`tokens/owner/service/fsc.go` runs `ttx.NewOrderingAndFinalityView(tx)` before
the POST returns → the call blocks to finality. **Fabric-X has no separable
submit-ack (T2).**

## Adapter

Native gRPC — see [fabricx-integration.md](fabricx-integration.md) for how this
was established and [adr-016](../decisions/adr-016-fabricx-native-grpc.md) for
the decision.

| | |
| --- | --- |
| Submit | broadcast a signed `common.Envelope` to the Arma router (`:6022`) and wait for its reply; a pool of streams, one unacknowledged envelope each |
| Finality | sidecar deliver stream (`:4001`); per-transaction validation codes from the block's `TRANSACTIONS_FILTER` metadata |
| Signing | ECDSA-P256 over the namespace's ASN.1 marshalling, sha256-digested — upstream's own encoding via `fabric-x-common` |
| Config keys | `broadcast_endpoint`, `deliver_endpoint`, `channel_id`, `namespace`, `signing_key_path`, `metrics_endpoint` |

Workload mapping: `kv-write` → a blind write; `transfer` → a two-key
read-modify-write; `kv-read` → a read plus a **unique dummy blind write**,
because the validator rejects read-only transactions (`MALFORMED_NO_WRITES`).
That last one is disclosed in [../workloads/mismatches.md](../workloads/mismatches.md).

Submit and commit are separate operations, so unlike the previous REST
integration Fabric-X reports a genuine submit latency: T2 is the Arma router's
reply to each envelope, matched one-per-stream because replies carry no ID.

## Deployment

`deploy/docker/fabricx/up.sh <profile>` clones `fabric-x-committer` and
`fabric-x-orderer` at pinned tags, builds one image serving four roles, starts
them in dependency order, registers the application namespace, and writes
`connection.env`.

Namespace creation is a write to the `_meta` namespace, governed by the channel's
`LifecycleEndorsement` policy — MAJORITY of the four Arma orgs. `up.sh` runs
`loadgen --only-namespace` inside the Arma container, whose config declares all
four identities, so it signs with all of them. The namespace's ECDSA verification
key is registered at the same time and the matching private key is exported to
the host for the adapter.

Block-cutting parameters come from the profile's shared `orderer_batch` anchor,
so Fabric-X cuts blocks identically to the Fabric family.
