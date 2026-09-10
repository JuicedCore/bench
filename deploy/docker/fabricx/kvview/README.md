# Fabric-X custom `kv-write` view service

**Why this exists.** The fabric-x-samples "tokens" REST API
(`hyperledger/fabric-x-samples/tokens/swagger.yaml`, verified) is token-only —
`/issuer/issue`, `/owner/accounts/{id}/transfer`, `/owner/accounts/{id}/redeem`,
`GET /owner/accounts/{id}`. There is **no key/value route**. The normalized
`kv-write` / `kv-read` workloads need one, so this service adds a custom FSC view
and a `/kv` route (decision: adr-003, confirmed after the API check).

## Current state — Phase 3 stub

`main.go` builds with the Go stdlib only and serves:

| route | behaviour |
| ----- | --------- |
| `GET /healthz` | `200 {"message":"ok"}` — lets the topology + adapter health check pass |
| `POST /kv` | `501` with a "not wired yet" body |

The container runs; the `fabricx` adapter treats `/kv` `501` as a failed tx.

## Contract the adapter expects (`pkg/adapters/fabricx/fsc_client.go`)

```
POST /kv   {"op":"write","key":"k","value":"<base64>"}  -> 200 {"txID":"<id>"}
POST /kv   {"op":"read","key":"k"}                       -> 200 {"txID":"<id>","value":"<base64>","found":true}
```

Both are **synchronous to finality** (the view runs ordering + finality before
responding), matching the token routes — so the adapter's background-POST model
and the T2-collapse hold uniformly for Fabric-X.

## Implementation spec (Phase 3 build, needs a running Fabric-X network)

Mirror `fabric-x-samples/tokens/owner`:

1. **Deps** (add to `go.mod`):
   - `github.com/hyperledger-labs/fabric-smart-client` — FSC node + view registry
   - `github.com/LFDT-Panurus/panurus/token/...` — token/ttx services (the
     fabric-x token SDK; used here only for the `ttx.NewOrderingAndFinalityView`
     pattern, not for tokens)
   - or, simpler: use the **fabric3 platform state API** directly for a plain
     `PutState` / `GetState` inside a view — no token SDK needed for KV.

2. **`common.StartFSC(confDir, dataDir)`** to bring up the FSC node against the
   Fabric-X network's `core.yaml` (mounted at `--conf`).

3. **Views** (`service/kvview.go`):
   - `WriteView{Key string; Value []byte}` — `RunView`:
     build a fabric3 transaction, `tx.PutState(ns, key, value)`, then
     `ttx.NewCollectEndorsementsView(tx)` (or the fabric3 endorsement collector),
     then `NewOrderingAndFinalityView(tx)`; return `tx.ID()`.
   - `ReadView{Key string}` — `RunView`: `GetState(ns, key)` via a query view;
     return `{value, found}`. (Decide: does a read produce a ledger tx? If it is
     a local query, mark it finalized immediately in the adapter — the adapter
     already does for `TxRead`.)
   - Register both with `viewregistry.GetRegistry(fsc).RegisterFactory("kv-write", …)`
     and `"kv-read"`.

4. **HTTP handler** (`handleKV` in `main.go`): decode `kvRequest`, base64-decode
   `value`, `viewManager.InitiateView(ctx, &WriteView{...})`, marshal `kvResponse`.

5. **Namespace**: use a dedicated chaincode/namespace id (e.g. `benchkv`) that the
   Fabric-X committer is configured to accept. Document it in the run manifest
   via the adapter (`adapter.namespace`).

## Build (Phase 3)

```
docker build -t bench/fabricx-rest:latest deploy/docker/fabricx/kvview
```

Wired as the `kvview` service in `../docker-compose.yml`, port 9700, `--conf`
pointing at the Fabric-X network config mount.
