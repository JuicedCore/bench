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

Fabric-X has no chaincode to deploy and no `PutState`/`GetState`. A transaction
carries **client-signed read/write sets per namespace**. The committer's verifier
checks each namespace's signature against the policy registered in the `_meta`
namespace, then validates (MVCC) and commits to its state store (PostgreSQL).

Consequence: the *same* smart contract cannot be deployed across all platforms.
The harness defines **functionally equivalent workloads** implemented natively per
platform ([adr-009](../decisions/adr-009-workload-strategy.md)).

## Arma ordering

Four server roles: **routers** (accept + dispatch), **batchers** (form batches),
**consenters** (BFT-order compact *digests*, not payloads), **assemblers**
(reconstruct full blocks). Ordering digests instead of payloads is where the
throughput comes from.

## Why not the REST / Token SDK samples

An earlier integration drove `fabric-x-samples/tokens` over REST. Those calls block
to finality (no separable T2) and spend most of their time in Fabric Smart Client
and ZKP generation, so they measure the demo app rather than Fabric-X. It never
produced a number and was removed: post-mortem in
[adr-003](../decisions/adr-003-fabricx-fsc-view-and-rest.md) (superseded), the
replacement in [adr-016](../decisions/adr-016-fabricx-native-grpc.md).

## Adapter

Native gRPC — see [fabricx-integration.md](fabricx-integration.md) for how this
was established and [adr-016](../decisions/adr-016-fabricx-native-grpc.md) for
the decision.

| | |
| --- | --- |
| Submit | broadcast a signed `common.Envelope` to every party's Arma router (`:6022`, `:6122`, `:6222`, `:6322`) and take the first reply as T2; a pool of streams per router, one unacknowledged envelope each. A single router is ~10s slower: only the primary batcher cuts batches, and a secondary forwards requests to it only after `FirstStrikeThreshold` |
| Finality | sidecar deliver stream (`:4001`); per-transaction validation codes from the block's `TRANSACTIONS_FILTER` metadata |
| Signing | ECDSA-P256 over the namespace's ASN.1 marshalling, sha256-digested — upstream's own encoding via `fabric-x-common` |
| Config keys | `broadcast_endpoint`, `deliver_endpoint`, `channel_id`, `namespace`, `signing_key_path`, `metrics_endpoint` |

Workload mapping: `kv-write` → a blind write; `transfer` → a two-key
read-modify-write; `kv-read` → a read plus a **unique dummy blind write**,
because the validator rejects read-only transactions (`MALFORMED_NO_WRITES`).
That last one is disclosed in [../workloads/mismatches.md](../workloads/mismatches.md).

Submit and commit are separate operations, so Fabric-X reports a genuine submit
latency: T2 is the first reply from the four Arma routers the envelope was sent
to, matched one-per-stream because replies carry no ID.

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

Block-cutting parameters (`BatchCreationTimeout`, `MaxMessageCount`) come from the
profile's shared `orderer_batch` anchor, so Arma's batcher cuts blocks like the
Fabric family's orderer. The byte limits are not applied, and SmartBFT's
`requestbatchmaxinterval` stays at the upstream default: it batches batch
attestations into consensus proposals and has no Raft counterpart.

| | |
| --- | --- |
| Versions | `fabric-x-committer` v1.0.5, `fabric-x-orderer` v1.0.6 (`COMMITTER_REF` / `ORDERER_REF` in `up.sh`) |
| Containers | `fabricx-arma` (4 parties × router, batcher, consenter, assembler), `fabricx-db` (PostgreSQL), `fabricx-pipeline` (sidecar, verifier, coordinator), `fabricx-committer` (validator-committer, query) |
| Host ports | routers 6022 / 6122 / 6222 / 6322, assembler 6023, sidecar deliver 4001, committer metrics 9643 |
| First deploy | compiles Arma and the committer from source: several minutes and several GB |
| Namespace check | `up.sh` confirms the `ns__meta` row holds the exported key; it does not trust loadgen's exit code (upstream exits 1 on success) |

The deployment gotchas found on the way to the first live run (TLS modes, the
sidecar's peer Deliver service, committer status codes, single-router latency) are
written up in [fabricx-integration.md](fabricx-integration.md).
