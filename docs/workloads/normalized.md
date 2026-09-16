# Normalized workloads

The cross-platform comparison set. Each is defined once as a stream of
`adapters.Transaction` values and implemented natively on every platform. The
generator is deterministic given `seed`, so every platform sees the identical
key-access pattern and contention profile.

Source: `pkg/workloads/workload.go`.

| Name | Transaction kind(s) | Fabric / Drunix | Fabric-X | NeuChain |
| ---- | ------------------- | --------------- | -------- | -------- |
| `kv-write` | all `TxWrite` | chaincode `Put(key,value)` | blind write in the application namespace, ordered by Arma | YCSB update |
| `kv-read` | all `TxRead` | chaincode `Get(key)` (Evaluate on one peer) | QueryService `GetRows` against committed PostgreSQL (no Arma, no dummy write) | transaction with a read set, through commit |
| `kv-mixed` | `TxRead`/`TxWrite` per `read_write_ratio` | `Get` / `Put` | as the two rows above | as the two rows above |
| `transfer` | all `TxTransfer` | chaincode `Transfer(from,to,amount)` (RMW two accounts, balance computed) | two-key read/write set carrying the workload's values (no computed balance; live workload leaves those values empty) | **not implemented** — YCSB is one key per call; Submit fails |

Exact bytes on the wire and in world state, per platform:
[payload-and-state.md](payload-and-state.md).

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
**the same two points** (T2 acknowledge, T3 finality). The actual payload and
the bytes that land in world state are in
[payload-and-state.md](payload-and-state.md). Where a platform's native
primitive is a poor fit (Fabric-X reads and transfers, NeuChain transfer), that
is documented in [mismatches.md](mismatches.md) and the run is caveated, not
silently dropped ([adr-010](../decisions/adr-010-mismatch-report.md)).

## One config file per mode

The normalized set lives in `configs/normalized/`, **one file per mode serving all
five platforms** (`--platform` selects the network). The `load:` and `metrics:`
blocks are therefore the same bytes for every platform, so "identical config" is
structural rather than a convention someone has to maintain. Platform-specific
settings live only in the union `adapter:` block, whose irrelevant keys each
adapter ignores. `pkg/harness/configparity_test.go` enforces it.

Per-platform tuned (`normalized: false`) runs would never be mixed with these;
there are no native configs in the repo today.

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
