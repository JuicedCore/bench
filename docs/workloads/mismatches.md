# Workload mismatches

Where a normalized workload maps awkwardly onto a platform's model. Per
[adr-010](../decisions/adr-010-mismatch-report.md) these runs are **executed
anyway** and reported with an explicit caveat, never silently dropped — a missing
data point is itself misleading.

## Fabric-X + `kv-write` / `kv-read` / `kv-mixed`

Fabric-X has no `PutState`/`GetState`. The harness uses a **custom minimal FSC
view** that performs a plain key/value write/read. This is closer to the other
platforms' KV path than Token SDK `Issue` would be, but it is still:

- a view/session round-trip, not a chaincode simulation;
- carrying FSC negotiation overhead the EOV platforms do not have;
- routed through an FSC client node + REST server that sit in the measured path.

**Caveat attached to these runs:** "Fabric-X KV via FSC view — not a native
Fabric-X primitive; submit-side cost includes FSC negotiation and the REST/FSC
node, which have no equivalent on Fabric/Drunix/NeuChain. Compare shapes and
trends, not absolute submit latency."

## Fabric-X + `transfer` (normalized)

`transfer` maps to a Token SDK `Transfer` — which *is* native — but the
normalized run pins it to a value-transfer of amount 1 between two fixed-identity
accounts, not a realistic multi-denomination UTXO flow. Native token behaviour
(coin selection, change outputs) shows only in the platform-native run.

## NeuChain + `transfer`

If the `ev` branch's native transaction model does not express a two-account
read-modify-write directly, the adapter composes it from two KV operations in one
deterministic transaction. Any such composition is recorded in
[neuchain-client-implementation.md](../platforms/neuchain-client-implementation.md)
§4 and becomes a caveat.

## NeuChain / Fabric-X on the `local` profile

Not a workload mismatch but a resource mismatch — see
[fairness-guarantees.md](../architecture/fairness-guarantees.md). Automatic
manifest caveat on every `local` run of these two platforms.

## How caveats surface

`manifest.caveats[]` → printed in `summary.txt` → rendered as a highlighted row
under the platform in `benchrunner report` output.
