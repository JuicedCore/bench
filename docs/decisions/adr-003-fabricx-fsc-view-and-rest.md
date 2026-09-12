# ADR-003: Drive Fabric-X through an FSC view and its REST facade — SUPERSEDED

**Status: superseded by [adr-016](adr-016-fabricx-native-grpc.md).** Kept because
the reasoning that led here is worth not repeating.

## Context (as understood at the time)

Fabric-X has no chaincode, so the normalized `kv-*` workloads had no obvious
mapping. `fabric-x-samples/tokens` exposed a REST API that worked out of the box,
and a custom FSC view service (`kvview`) could add a `/kv` route to cover the KV
workloads.

## Decision

Drive Fabric-X over the `tokens` REST facade, and write an FSC view service to
provide the missing key/value path.

## Why this was wrong

- `tokens` is a **demo application**, not a benchmarking interface. Every request
  carried FSC session setup and ZKP-backed UTXO token logic, so the numbers would
  have measured FSC and proof generation more than Fabric-X. It ships no metrics
  endpoint and no load driver.
- The REST call is **synchronous to finality**, collapsing T2 into T3. The adapter
  had to report a fabricated ack, and submit/commit latency was permanently N/A —
  a comparability loss that was treated as inherent to the platform when it was
  really an artefact of this choice.
- `kvview` was never finished. It answered every request with 501, and because
  the adapter defaulted `kv_url` to it, the default write path was a guaranteed
  100% failure.
- It obscured the real bootstrap problem. Namespace creation was attempted via
  `fxconfig` with a single signature against a channel policy requiring three,
  and the resulting `ABORTED_SIGNATURE_INVALID` was misread as an identity
  problem for a long time.

Fabric-X does have a supported client path — gRPC broadcast to an Arma router
with outcomes from the sidecar's deliver stream — which upstream's own `loadgen`
uses. It was not found because the sample app was reached for first.

## Consequence

See [adr-016](adr-016-fabricx-native-grpc.md) and
[docs/platforms/fabricx-integration.md](../platforms/fabricx-integration.md).
