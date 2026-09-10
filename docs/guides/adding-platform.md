# Adding a platform adapter

## 1. Implement the interface

`pkg/adapters/<name>/adapter.go` — satisfy `adapters.PlatformAdapter`:

```go
Name() string
Setup(ctx, AdapterConfig) error       // client connections only; never deploys
Teardown(ctx) error
Submit(ctx, *Transaction) (*SubmitResult, error)   // returns at T2, never blocks to finality
WaitForFinality(ctx, txID, timeout) (*FinalityResult, error)  // blocks to T3
Query(ctx, key) (*QueryResult, error)
MetricsEndpoint() string              // "" if none
```

Rules:

- `Submit` MUST use the platform's async path so **T2 (ack)** and **T3 (commit)**
  are distinct. If the only SDK call blocks to commit, that platform needs a
  lower-level API or an event subscription.
- A committed-but-invalid tx → `FinalityResult{Valid:false}`, no error.
- Safe for concurrent use after `Setup`.

Optional: implement `PlatformVersion() string` and `CryptoInfo() adapters.CryptoInfo`
so the manifest records them.

## 2. Register

```go
func init() { adapters.Register("<name>", func() adapters.PlatformAdapter { return &Adapter{} }) }
```

Blank-import the package in `cmd/benchrunner/main.go`.

## 3. Map the normalized workload

Translate `TxWrite` / `TxRead` / `TxTransfer` onto the platform's primitive
(chaincode fn, FSC view, native RPC). If a kind is a poor fit, still implement it
and add a caveat (see [../workloads/mismatches.md](../workloads/mismatches.md)).

## 4. Deploy scripts

`deploy/docker/<name>/up.sh` + `down.sh`:

- `source ../lib.sh` for helpers.
- Bring up the topology with per-container limits from
  `deploy/profiles/<profile>.yaml`.
- For normalized parity: LevelDB (or equivalent), pinned batch params if it has
  an orderer.
- Emit `deploy/docker/<name>/connection.env` exporting `BENCH_ADAPTER_*` (and
  `BENCH_PLATFORM_VERSION`).
- `up.sh` must `drop_caches` before starting load-bearing containers.

## 5. Profile entries

Add `<name>:` under `platforms:` in each `deploy/profiles/*.yaml` with `nodes`,
`per_container`, `state_db`, and `orderer_batch` (or `{}` if none).

## 6. Config + docs

- Add a config or reuse `configs/quick-smoke.yaml` (works if you export the same
  `BENCH_ADAPTER_*` keys).
- Write `docs/platforms/<name>.md`: what it's built for, tx lifecycle, adapter
  notes, fairness levers, deploy, gotchas.
- Add an ADR if a non-obvious decision was made.

## 7. Tests

- Unit: adapter against a mock/fake server.
- Integration (build tag): deploy, submit 10 tx, assert every one has a finality
  result and `T1 ≤ T2 ≤ T3`.
