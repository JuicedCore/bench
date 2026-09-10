# ADR-011: Pin orderer block-cutting parameters

## Context

For the Fabric family (fabric-cft, fabric-bft, drunix, fabricx) the orderer's
block-cutting parameters — `BatchTimeout`, `MaxMessageCount`,
`PreferredMaxBytes`, `AbsoluteMaxBytes` — are the single largest lever on both
throughput and latency. Left at defaults they differ across sample networks; left
unrecorded a run is not reproducible.

## Decision

- The parameters live in `deploy/profiles/<profile>.yaml` under each platform's
  `orderer_batch`, via a shared YAML anchor so the Fabric-family values **cannot
  drift** between platforms.
- **Normalized runs**: identical values across fabric-cft, fabric-bft, drunix,
  fabricx.
- **Native runs**: per-platform tuned; the tuned values are recorded.
- Every run's `manifest.json` records the effective `orderer_batch`.
- Deploy scripts patch the values into the sample network's `configtx.yaml`
  before bringing the network up.

## Rationale

- Without this, a Fabric-vs-Fabric-X throughput comparison mostly measures who
  had the more aggressive batch timeout.
- A shared anchor removes the "someone edited one profile and not the other"
  failure mode.
- Recording in the manifest makes disagreements between runs explainable.

## Alternatives considered

- **Leave at sample defaults** — defaults differ (v2.5 test-network vs v3.x vs
  Fabric-X); not comparable.
- **Tune each platform to its best for *all* runs** — that is the native-run
  policy; applying it to normalized runs breaks parity.

## Consequences

- `deploy/docker/*/up.sh` has a `configtx.yaml` patch step.
- NeuChain has no orderer; its `orderer_batch` is `{}` and the field is absent
  from its manifest.
- Changing the normalized batch values invalidates prior normalized results —
  bump a version note in the report.
