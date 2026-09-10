# ADR-003: Fabric-X via a custom FSC view (normalized) + REST/Token SDK (native)

## Context

Fabric-X has no chaincode. Transactions are FSC (Fabric-Smart-Client)
view/session negotiations plus the Token SDK (UTXO). The Fabric Gateway SDK does
not apply. The harness still needs a normalized KV workload and a native workload.

## Decision

- **Normalized `kv-*`** → a **custom minimal FSC view** that does a plain
  key/value write/read.
- **Native `token-transfer`** → Token SDK `Issue`/`Transfer`/`Redeem` via the
  **REST API** the tokens sample exposes.
- The FSC client node + REST server are acknowledged to be **in the measured
  path** with no equivalent on other platforms; this is recorded in the manifest
  and footnoted in reports, not "corrected".

## Rationale

- A custom FSC KV view is a closer analog to the other platforms' KV path than
  Token SDK `Issue` (a UTXO mint). It keeps the normalized bucket meaningful.
- REST (vs driving the FSC Go SDK directly from the harness) keeps the harness
  from becoming a smart client itself, which would conflate client-side
  processing with platform performance. The trade-off — an extra HTTP hop — is
  disclosed rather than hidden.
- Token SDK for the native run is exactly the workload Fabric-X's published
  200k-TPS benchmark used.

## Alternatives considered

- **Token SDK `Issue` as the normalized "write"** — wider architectural gap in
  the normalized bucket; kept for native only.
- **Drive the FSC Go SDK in-process** — makes the harness a Fabric-X participant;
  its CPU cost pollutes the measurement.
- **Skip Fabric-X for KV workloads** — a missing data point is misleading
  ([adr-010](adr-010-mismatch-report.md)).

## Consequences

- Phase 3 must build and register a custom FSC view (small Go project against the
  Fabric-X FSC libraries).
- Every Fabric-X `kv-*` run carries the mismatch caveat from
  [../workloads/mismatches.md](../workloads/mismatches.md).
- Submit latency for Fabric-X is not directly comparable; throughput and latency
  *trends* are.
