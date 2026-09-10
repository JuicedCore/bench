# ADR-014: Pin Fabric to release tags, not `main`

## Context

The original plan referenced Fabric `main` for the BFT variant. Fabric v3.0 GA'd
in 2024; the v3.1.x line made SmartBFT production-ready (v3.1.4, Feb 2026).
`main` moves continuously.

## Decision

- **`fabric-cft`** → `release-2.5`, pinned to a concrete tag (`v2.5.11` at time
  of writing), Raft orderer, LTS.
- **`fabric-bft`** → a concrete `v3.1.x` tag (`v3.1.1` at time of writing),
  SmartBFT orderer.
- `fabric-samples` is cloned at the matching tag; Fabric binaries + Docker images
  installed at the matching version.
- The concrete version is written into `manifest.platform_version` (via
  `BENCH_PLATFORM_VERSION` from the deploy script).

## Rationale

- Reproducibility: a benchmark against `main` cannot be re-run to the same code.
- `main` may carry unreleased, unstable BFT changes.
- `release-2.5` is the right CFT baseline (LTS, what production uses); v3.1.x is
  the right BFT baseline (first stable SmartBFT).

## Alternatives considered

- **`main` for BFT** — as originally planned; not reproducible.
- **v3.x for both variants** — loses the LTS/CFT baseline that most deployments
  actually run.

## Consequences

- Bump the pinned tags deliberately, as its own change, with a report version
  note — never silently.
- `deploy/docker/fabric-cft/up.sh` and `fabric-bft/up.sh` carry
  `FABRIC_VERSION` / `SAMPLES_TAG` at the top.
