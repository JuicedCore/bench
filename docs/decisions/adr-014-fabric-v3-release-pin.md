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

- Bump the pinned versions deliberately, as its own change, with a report
  version note — never silently.
- `deploy/docker/fabric-cft/up.sh` and `fabric-bft/up.sh` carry
  `FABRIC_VERSION` / `FABRIC_CA_VERSION` / `SAMPLES_REF` at the top.

## Update — fabric-samples has no per-version tags

`hyperledger/fabric-samples` stopped publishing `vX.Y.Z` tags after `v2.4.9`
(branches: `main`, `release-2.2`). Its `test-network` on `main` supports Fabric
2.5.x *and* 3.x (`network.sh up ... -bft` selects SmartBFT). So:

- We take **fabric-samples at `main`** (`SAMPLES_REF`, overridable).
- We pin only the Fabric **binary/image** version, which the samples'
  `install-fabric.sh` fetches independently:
  - `fabric-cft`: `FABRIC_VERSION=2.5.16`, `FABRIC_CA_VERSION=1.5.22` (LTS 2.5.x, Raft).
  - `fabric-bft`: `FABRIC_VERSION=3.1.5`, `FABRIC_CA_VERSION=1.5.22` (v3.1.x, SmartBFT).
- `manifest.platform_version` records the concrete `FABRIC_VERSION`; the samples
  commit is not pinned (test-network is stable across `main`).
- Also: `network.sh`'s `-o` flag is the orderer *type* (`etcdraft`/`BFT`), not
  batch config — the orderer batch params (adr-011) are patched into
  `configtx/configtx.yaml` before `network.sh up`.
