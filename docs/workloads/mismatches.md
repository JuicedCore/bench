# Workload mismatches

Where a normalized workload maps awkwardly onto a platform's model. Per
[adr-010](../decisions/adr-010-mismatch-report.md) these runs are **executed
anyway** and reported with an explicit caveat, never silently dropped — a missing
data point is itself misleading.

## Fabric-X + `kv-read`

A read-only *transaction* is still rejected by the validator
(`MALFORMED_NO_WRITES`). The adapter does not paper over that with a dummy
write. `TxRead` uses Fabric-X's **QueryService** (`GetRows` on `:7001`): a
point lookup of committed PostgreSQL state, no Arma, no state mutation.
`WaitForFinality` returns immediately `Valid: true`, the same T2/T3 shape as
Fabric Evaluate.

That is the platform's default read path. It is comparable to Fabric/Drunix
Evaluate as a non-committing lookup; the remaining gap is the state engine
(PostgreSQL vs LevelDB/YugabyteDB). The query process batches keys
(`min-batch-keys` / `max-batch-wait` in query.yaml, default 100 ms wait) — that
is upstream behaviour, not something the adapter adds.

Writes still go through Arma. A read-only envelope would still fail if anyone
broadcast one.

**Caveat:** "Fabric-X reads are QueryService.GetRows against PostgreSQL, not
an ordered transaction. Compare as a point-lookup path, with the state-DB
caveat."

## Fabric-X + `transfer`

There is no chaincode to evaluate a predicate, so `transfer` is modelled as a
two-key read-modify-write. The live workload does not set `Value` (it sets
`Amount`), so both keys are written empty rather than as a computed balance.
Contention behaviour — two accounts touched per transaction, hot keys colliding
— is comparable. Token semantics are not. Details:
[payload-and-state.md](payload-and-state.md).

## NeuChain + `transfer`

The YCSB chaincode the deploy runs (`cc_type: ycsb`) touches one key per call.
`pkg/adapters/neuchain/txbuild.go` therefore **rejects** `TxTransfer` at Submit
(`transfers need the small_bank chaincode`). No payload is published; the
collector records a submit error; world state does not change. A unit test
asserts this. `contention.yaml` against `--platform neuchain` cannot produce a
headline until a small-bank mapping exists.

Wire-level detail: [payload-and-state.md](payload-and-state.md).

## kv-read on every platform

`kv-read` is in the normalized set (adr-009), but reads take a different path on
each platform family, so its latency is not comparable to the write modes:

- **fabric / drunix** — `Evaluate` against one peer; `WaitForFinality` returns
  immediately with `Valid: true` and nothing reaches the ledger. The number is a
  client-observed evaluate round-trip.
- **fabricx** — QueryService `GetRows` against committed state. Not ordered,
  not committed, no dummy write. Same T2/T3 shape as Fabric Evaluate.
- **neuchain** — a real submitted transaction carrying a read set, through the
  full commit path (`txbuild.go` maps `TxRead` to a read-set-only YCSB payload).

So NeuChain's read cost includes consensus while Fabric, Drunix and Fabric-X
do not. Read paths are comparable *to each other* as read paths; `read-profile`
numbers must never be set beside `kv-write` numbers as though they measured the
same thing.

**Caveat wording:** "kv-read is an evaluate / query-service round-trip on
Fabric, Drunix and Fabric-X, and a committed transaction on NeuChain; read
latency is not comparable to write latency, and Fabric-X vs Fabric still
carries the state-DB caveat."

## NeuChain / Fabric-X on the `local` profile

Not a workload mismatch but a resource mismatch — see
[fairness-guarantees.md](../architecture/fairness-guarantees.md). Automatic
manifest caveat on every `local` run of these two platforms.

## How caveats surface

`manifest.caveats[]` → printed in `summary.txt` → rendered as a highlighted row
under the platform in `benchrunner report` output.
