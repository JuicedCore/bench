# Workload mismatches

Where a normalized workload maps awkwardly onto a platform's model. Per
[adr-010](../decisions/adr-010-mismatch-report.md) these runs are **executed
anyway** and reported with an explicit caveat, never silently dropped — a missing
data point is itself misleading.

## Fabric-X + `kv-write` / `kv-read` / `kv-mixed`

Fabric-X has no `PutState`/`GetState`, and — confirmed against
`fabric-x-samples/tokens/swagger.yaml` — the sample REST façade has **no KV route
at all** (token issue/transfer/redeem + account balance only). The harness runs a
**custom FSC view service** (`deploy/docker/fabricx/kvview/`) that adds a `/kv`
route doing a plain key/value write/read. It is still:

- a view/session round-trip, not a chaincode simulation;
- carrying FSC negotiation overhead the EOV platforms do not have;
- routed through an FSC client node + HTTP server in the measured path;
- **synchronous to finality** — like the token routes, the view runs
  ordering + finality before responding, so Fabric-X has no separable submit-ack
  (T2). Only E2E latency and confirmed TPS are comparable for Fabric-X.

**Caveat attached to these runs:** "Fabric-X KV via a custom FSC view — not a
native Fabric-X primitive; the FSC node + HTTP server are in the measured path
and have no equivalent on Fabric/Drunix/NeuChain. Submit latency is N/A (call is
synchronous to finality). Compare E2E latency and TPS trends only."

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
