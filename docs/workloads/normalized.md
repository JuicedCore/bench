# Normalized workloads

The cross-platform comparison set. Each is defined once as a stream of
`adapters.Transaction` values and implemented natively on every platform. The
generator is deterministic given `seed`, so every platform sees the identical
key-access pattern and contention profile.

Source: `pkg/workloads/workload.go`.

| Name | Transaction kind(s) | Fabric / Drunix | Fabric-X | NeuChain |
| ---- | ------------------- | --------------- | -------- | -------- |
| `kv-write` | all `TxWrite` | chaincode `Put(key,value)` | custom FSC KV-write view | native KV write RPC |
| `kv-read` | all `TxRead` | chaincode `Get(key)` (Evaluate) | FSC read view | native KV read RPC |
| `kv-mixed` | `TxRead`/`TxWrite` per `read_write_ratio` | `Get` / `Put` | read / write view | read / write RPC |
| `transfer` | all `TxTransfer` | chaincode `Transfer(from,to,amount)` (RMW two accounts) | Token SDK `Transfer` (native) *or* FSC transfer view (normalized) | native transfer RPC |

## Keys

- `kv-*` keys: `key-%09d` over `[0, key_space)`.
- `transfer` accounts: `acct-%09d`; source and destination drawn from the same
  distribution with different sub-seeds; destination bumped by 1 if it collides
  with the source.
- Distribution: `uniform` (no contention), `zipfian` (`zipfian_constant`, hot
  keys), `fixed` (single key, max contention).

## Value payload

`value_size_bytes` (default 64). Deterministic but non-constant per transaction
so world-state size grows realistically rather than deduplicating.

## What "equivalent" means here

Not "identical bytes on the wire" — that is impossible across three transaction
models. It means: **the same logical state effect** (set one key; move value
between two accounts), driven by **the same key-selection process**, measured at
**the same two points** (T2 acknowledge, T3 finality). Where a platform's native
primitive is a poor fit (Fabric-X KV), that is documented in
[mismatches.md](mismatches.md) and the run is caveated, not silently dropped
([adr-010](../decisions/adr-010-mismatch-report.md)).

## Config knobs (run config `load:` block)

```
workload:            kv-write | kv-read | kv-mixed | transfer
key_distribution:    uniform | zipfian | fixed
key_space:           integer
zipfian_constant:    float > 1.0 (higher = hotter)
read_write_ratio:    0.0 .. 1.0   (kv-mixed only; 1.0 = all reads)
value_size_bytes:    integer
seed:                integer (drives every generator; recorded in the manifest)
```
