# Workload mismatches

Where a normalized workload maps awkwardly onto a platform's model. Per
[adr-010](../decisions/adr-010-mismatch-report.md) these runs are **executed
anyway** and reported with an explicit caveat, never silently dropped — a missing
data point is itself misleading.

## Fabric-X + `kv-read`

The validator rejects read-only transactions outright (`MALFORMED_NO_WRITES`), so
a read must carry a write to be accepted at all. The adapter attaches a unique
blind write per read (`_r/<key>/<seq>`), unique so that concurrent reads of one
key do not write-after-write conflict and abort each other.

That is overhead no other platform pays, and it means a Fabric-X "read" performs
a state mutation. Read throughput is therefore a floor, not a like-for-like
figure.

**Caveat:** "Fabric-X reads carry a unique dummy blind write; the validator
rejects read-only transactions. Read cost includes a write no other platform
performs."

## Fabric-X + `transfer`

There is no chaincode to evaluate a predicate, so `transfer` is modelled as a
two-key read-modify-write carrying the workload's values rather than a computed
balance. Contention behaviour — two accounts touched per transaction, hot keys
colliding — is comparable. Token semantics, coin selection and change outputs are
not exercised.

## NeuChain + `transfer`

If the `ev` branch's native transaction model does not express a two-account
read-modify-write directly, the adapter composes it from two KV operations in one
deterministic transaction. Any such composition is recorded in
[neuchain-client-implementation.md](../platforms/neuchain-client-implementation.md)
§4 and becomes a caveat.

## kv-read on every platform

`kv-read` is in the normalized set (adr-009) but a read is not a commit anywhere,
so its latency is not comparable to the write modes:

- **fabric / drunix** — `Evaluate` against one peer; `WaitForFinality` returns
  immediately with `Valid: true` and nothing reaches the ledger. The number is a
  client-observed evaluate round-trip.
- **fabricx** — the same synchronous FSC-view POST as a write.
- **neuchain** — a real submitted transaction carrying a read set, through the
  full commit path (`txbuild.go` maps `TxRead` to a read-set-only YCSB payload).

So NeuChain's read cost includes consensus while Fabric's does not. Read paths are
comparable *to each other* as read paths; `read-profile` numbers must never be set
beside `kv-write` numbers as though they measured the same thing.

**Caveat wording:** "kv-read is an evaluate round-trip on Fabric/Drunix and a
committed transaction on NeuChain; read latency is not comparable to write
latency, and not uniformly comparable across platforms."

## NeuChain / Fabric-X on the `local` profile

Not a workload mismatch but a resource mismatch — see
[fairness-guarantees.md](../architecture/fairness-guarantees.md). Automatic
manifest caveat on every `local` run of these two platforms.

## How caveats surface

`manifest.caveats[]` → printed in `summary.txt` → rendered as a highlighted row
under the platform in `benchrunner report` output.
