# Fabric-X

## What it is

A ground-up re-architecture of Hyperledger Fabric for ultra-scalability
(LF Decentralized Trust). It decomposes the monolithic peer into independently
scalable microservices — **endorser, validator, committer** — and replaces the
Raft/SmartBFT orderer with **Arma**, a sharded BFT ordering service. Published
evaluation: **>200,000 TPS** on a large cluster (CBDC benchmark).

Repos: `github.com/hyperledger/fabric-x`, `github.com/hyperledger/fabric-x-orderer`.

## No chaincode

Fabric-X replaces the chaincode execution model with **peer-to-peer transaction
negotiation** built on **Fabric-Smart-Client (FSC)** views/sessions and the
**Fabric-Token-SDK** (UTXO model). There is no `PutState`/`GetState` chaincode to
deploy. This is confirmed by the LF Fabric-X roadmap, not an assumption.

Consequence: the *same* smart contract cannot be deployed across all four
platforms. Instead the harness defines **functionally equivalent workloads** and
implements them natively per platform (see
[adr-009](../decisions/adr-009-workload-strategy.md)).

## Arma ordering

Four server roles: **routers** (accept + dispatch), **batchers** (form batches),
**consenters** (BFT-order compact *digests*, not payloads), **assemblers**
(reconstruct full blocks). Ordering digests instead of payloads is where the
throughput comes from.

## Adapter (Phase 3)

`pkg/adapters/fabricx` — **cannot** use the Fabric Gateway SDK. Two entry points:

- **normalized `kv-write`** → a **custom minimal FSC view** that performs a plain
  key/value write. Chosen over Token SDK `Issue` because it is a closer analog to
  the other platforms' KV path (see
  [adr-003](../decisions/adr-003-fabricx-fsc-view-and-rest.md) and
  [workloads/mismatches.md](../workloads/mismatches.md)).
- **native `token-transfer`** → Token SDK `Issue` / `Transfer` / `Redeem` via the
  REST API the tokens sample exposes.

The harness interface is unchanged: `Submit` (view accepts the request → T2) →
`WaitForFinality` (committer commit event → T3).

### Disclosed asymmetry

The FSC client node (and the REST server fronting it) sits **in the measured
path** and has no equivalent on the other platforms. This is recorded in the
manifest and footnoted in every comparison — it is disclosed, not "corrected".

## Local caveat

Arma and the committer stack are scale-out designs. On the `local` profile
(≈8 cores across all Fabric-X services) throughput is far below the published
ceiling. Every `local` Fabric-X run carries an automatic manifest caveat:
absolute TPS is **not** comparable to the ~200k figure. The *shape* of the
latency curve and relative behaviour under contention remain informative.
Full-scale numbers need `gcp-full`.

## Adapter status (Phase 3 — functional against an assumed contract)

`pkg/adapters/fabricx` is implemented against the REST contract in
`fsc_client.go` (routes + JSON shapes, all overridable via the run config's
`adapter:` block): `POST {KVRoute}` write/read, `POST {TransferRoute}` native
transfer, `GET {TxStatusRoute}` / `GET {TxWaitRoute}` for finality. `Submit`
returns at REST-accept (T2); `WaitForFinality` polls or long-polls
(`finality_mode: poll|longpoll`) to T3; reads finalise immediately.
Unit-tested with an `httptest` fake. **The contract is assumed** — verify it
against the real tokens sample and adjust `fsc_client.go` before quoting numbers.

## Deploy (Phase 3 — scaffold)

- `deploy/docker/fabricx/up.sh` — clones `hyperledger/fabric-x` +
  `hyperledger/fabric-x-orderer`, prefers an upstream sample compose if present,
  else brings up `docker-compose.yml` (Arma router/batchers/consenters/assembler
  + endorser/validator/committer + `rest-facade`). Emits `connection.env`.
- `deploy/docker/fabricx/docker-compose.yml` — reference topology; **image
  names, ports, config mounts are placeholders**.
- `deploy/docker/fabricx/kvview/` — where the custom FSC "kv-write" view + REST
  façade program lives (Phase 3 TODO list in its README).
