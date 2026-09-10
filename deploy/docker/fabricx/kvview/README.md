# Fabric-X custom "kv-write" FSC view + REST façade

**Phase 3 deliverable.** A small Go program that:

1. Runs an **FSC view** implementing the normalized KV operations — `write(key,
   value)` and `read(key)` — as a Fabric-X transaction (no chaincode). This is
   the closest analog to the other platforms' KV path; it is deliberately *not*
   a Token SDK `Issue` (see
   [../../../../docs/decisions/adr-003-fabricx-fsc-view-and-rest.md](../../../../docs/decisions/adr-003-fabricx-fsc-view-and-rest.md)).
2. Runs the **Token SDK** transfer view for the native workload.
3. Exposes the **REST routes** the `fabricx` adapter expects (see
   `pkg/adapters/fabricx/fsc_client.go` — `KVRoute`, `TransferRoute`,
   `TxStatusRoute`, `TxWaitRoute`). Keep the JSON shapes in sync with that file,
   or override the routes/shapes there.

## Assumed REST contract (adjust to reality)

```
POST /api/v1/kv                 {"op":"write","key":"k","value":"<base64>"}  -> {"txID":"...","accepted":true}
POST /api/v1/kv                 {"op":"read","key":"k"}                      -> {"txID":"...","value":"<base64>","found":true}
POST /api/v1/tokens/transfer    {"tokenType":"BENCH","from":"a","to":"b","amount":1} -> {"txID":"..."}
GET  /api/v1/tx/{txID}                                                       -> {"txID":"...","status":"pending|committed|invalid","blockNum":N}
GET  /api/v1/tx/{txID}/wait     (long-poll to finality)                      -> same as above once terminal
```

## Build

```
docker build -t bench/fabricx-rest:latest deploy/docker/fabricx/kvview
```

Wired as the `rest-facade` service in `../docker-compose.yml`.

## TODO (Phase 3)

- [ ] Pin `hyperledger/fabric-x` + `fabric-x-orderer` tags in `../up.sh`
- [ ] Implement the FSC KV view against the pinned Fabric-X FSC libraries
- [ ] Confirm the commit-event / status API the committer exposes; map it to
      `TxStatusRoute` / `TxWaitRoute`
- [ ] Decide read semantics: does a "read" produce a ledger tx (finality path) or
      a local FSC query (immediate)? The adapter currently treats reads as
      immediate — change `Adapter.WaitForFinality` if reads must finalize.
- [ ] Record the exact contract here and in `fsc_client.go`
