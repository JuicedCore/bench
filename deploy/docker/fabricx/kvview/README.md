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

## Implementation spec (Phase 3 build — needs a running Fabric-X devnet)

Confirmed against `hyperledger-labs/fabric-smart-client@v0.20.0` +
`fabric-x-samples/tokens`.

### 1. Deps (`go.mod`)

```
github.com/hyperledger-labs/fabric-smart-client   v0.20.0   // node, endorser, view registry
github.com/hyperledger/fabric-samples/token-sdk           // the sample SDK wiring: common.NewSDK / StartFSC / WithAnyCORS (vendor or replace-directive to the cloned fabric-x-samples/tokens)
```

No Panurus/token-sdk needed — a plain KV write uses the **fabric endorser
service**, not tokens.

### 2. Node bringup (`main.go`)

```go
fsc, err := common.StartFSC(confDir, filepath.Join(confDir, "data")) // confDir = --conf mount = devnet FSC config
common.BindEndorsingIdentities(fsc, "default", map[string]string{ "endorser1-endorsing":"endorser1", "endorser2-endorsing":"endorser2" })
reg := viewregistry.GetRegistry(fsc)
reg.RegisterFactory("kv-write", &service.WriteViewFactory{})
reg.RegisterFactory("kv-read",  &service.ReadViewFactory{})
h := common.WithAnyCORS(mux)   // mux still serves /healthz + /kv
```

### 3. Views (`service/kvview.go`)

```go
// WriteView{Key string; Value []byte}
func (v *WriteView) Call(ctx view.Context) (any, error) {
    _, tx, err := endorser.NewTransaction(ctx, fabric.WithChannel("mychannel"))     // FNS default network
    tx.SetProposal(v.Namespace, "", "put", v.Key)                                    // v.Namespace defaults to "benchkv"
    rws, _ := tx.RWSet()
    rws.SetState(v.Namespace, v.Key, v.Value)
    if err := tx.Endorse(); err != nil { return nil, err }                           // self-endorse; or endorser.NewCollectEndorsementsView for multi-org
    if _, err := ctx.RunView(endorser.NewOrderingAndFinalityView(tx)); err != nil { return nil, err }
    return tx.ID(), nil
}
// ReadView{Key string} -> local query (fabric.GetDefaultFNS(ctx).Ledger()...GetState), returns {value, found}; NOT a ledger tx.
```

### 4. HTTP handler (`handleKV`)

decode `kvRequest` → base64-decode `value` →
`viewManager.InitiateView(ctx, &service.WriteView{Namespace: ns, Key: k, Value: v})`
→ the returned string is the txID → marshal `kvResponse`. Read op → `ReadView`,
mark it finalized immediately (the adapter already treats `TxRead` as immediate).

### 5. Namespace registration (the one external step)

`benchkv` must be a namespace the Fabric-X **validator/committer** accepts. In
`fabric-x-samples/devnet` add it to the committer's namespace/policy config (same
place the token namespace is declared) and to `fabric-x-samples/tokens/configtx.yaml`
if it gates namespaces. Pass the name via the adapter (`adapter.namespace`) so it
lands in the run manifest.

Until this is done, `POST /kv` stays a 501 stub and only Fabric-X `transfer`
(token) workloads run.

## Build (Phase 3)

```
docker build -t bench/fabricx-rest:latest deploy/docker/fabricx/kvview
```

Wired as the `kvview` service in `../docker-compose.yml`, port 9700, `--conf`
pointing at the Fabric-X network config mount.
