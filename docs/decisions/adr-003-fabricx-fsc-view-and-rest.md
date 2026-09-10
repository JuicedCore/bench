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

## Update — after verifying the real API (`hyperledger/fabric-x-samples/tokens`)

`tokens/swagger.yaml` confirms the sample REST façade is **token-only**:
`POST /issuer/issue` (:9100), `POST /owner/accounts/{id}/transfer` (:9500/:9600),
`POST /owner/accounts/{id}/redeem`, `GET /owner/accounts/{id}?code=<type>`,
`GET /healthz`. **There is no KV route.**

Two facts this settles:

1. **Custom KV view is required, not optional.** A `kv-write` workload cannot be
   expressed through the sample API at all. The custom FSC view + a `/kv` route
   are built as `deploy/docker/fabricx/kvview/` (its own service, `bench/fabricx-rest`
   image), not a fork of an existing route. Decision from the reopened F3
   question: **build the custom view** (keeps the normalized bucket meaningful).

2. **Submit latency (T2) is N/A for Fabric-X.** `tokens/owner/service/fsc.go`
   runs `ttx.NewOrderingAndFinalityView(tx)` before the POST returns — the call
   is synchronous to finality. The adapter therefore fires the POST in the
   background and reports `AckTime = now` (advisory); `WaitForFinality` returns
   when the POST completes (T3). The manifest and the fairness table mark
   Fabric-X submit latency **N/A**; only E2E (T3 − scheduled) and confirmed TPS
   are comparable. The custom KV view is written to the same
   synchronous-to-finality contract so this is uniform for Fabric-X.

Deploy: `deploy/docker/fabricx/up.sh` runs `fabric-x-samples/tokens`
`make setup && make start` (devnet Arma+committer + token services), then builds
and starts the kvview service on the shared `fabric_test` network.
