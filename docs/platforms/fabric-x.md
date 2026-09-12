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

## Adapter (`pkg/adapters/fabricx`)

HTTP client for the token routes above + one custom `/kv` route
(`deploy/docker/fabricx/kvview/`). Config keys: `owner_url`, `issuer_url`,
`kv_url`, `sender_account`, `counterparty_node`, `token_code`, `metrics_endpoint`.

- `TxTransfer` → `POST /owner/accounts/{sender}/transfer` (native).
- `TxWrite` → `POST /kv` (custom view) if `kv_url` set, else `POST /issuer/issue`.
- `TxRead` → `POST /kv` read, or `GET /owner/accounts/{id}`.
- The POST is synchronous, so `Submit` fires it in a **background goroutine** and
  returns immediately with `AckTime = now` (advisory); `WaitForFinality` returns
  when that POST completes (T3). Submit latency is reported **N/A** for Fabric-X
  ([adr-003](../decisions/adr-003-fabricx-fsc-view-and-rest.md),
  [fairness-guarantees](../architecture/fairness-guarantees.md)).

`httptest`-tested against the real route shapes (transfer, kv write/read,
unreachable, Submit-returns-before-finality).

## The custom KV view (normalized workloads)

Since the sample API has no KV route, `deploy/docker/fabricx/kvview/` adds one:
an FSC view doing plain key/value writes/reads, exposed at `POST /kv`, written to
the same synchronous-to-finality contract. **Currently a stub** (`POST /kv` →
501); the implementation spec (register FSC Write/Read views, run
ordering+finality, mirror the owner service) is in
`deploy/docker/fabricx/kvview/README.md`. Token workloads
(`configs/native/quick-smoke-fabricx.yaml`, `workload: transfer`) run today; `kv-write`
needs the stub finished.

## Deploy (Phase 3)

`deploy/docker/fabricx/up.sh`:

1. clones `hyperledger/fabric-x-samples`;
2. `tokens/` `make setup && make start` — devnet (Arma + committer) + issuer /
   endorser1 / owner1 / owner2 services (ports 9100 / 9300 / 9500 / 9600);
3. `POST /endorser/init` to commit token parameters;
4. builds + starts the `kvview` service (`bench/fabricx-rest`, :9700) on the
   shared `fabric_test` network;
5. emits `connection.env` (`BENCH_ADAPTER_OWNER_URL` etc.).

`down.sh` runs `tokens/` `make teardown` + `docker compose down`.

## Local caveat

Arma and the committer stack are scale-out designs. On `local` (≈8 cores across
all Fabric-X services) throughput is far below the published ceiling. Every
`local` Fabric-X run carries an automatic manifest caveat: absolute TPS is not
comparable to the ~200k figure. The *shape* of the latency curve and relative
behaviour under contention remain informative. Full-scale numbers need
`gcp-full`.
