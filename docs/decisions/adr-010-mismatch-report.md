# ADR-010: Run architecturally mismatched workloads anyway, with explicit caveats

## Context

Some normalized workloads fit some platforms badly — most sharply, KV workloads
on Fabric-X (no chaincode; a custom FSC view stands in). Dropping those cells
leaves gaps in the comparison matrix.

## Decision

Run every normalized workload on every platform. Where the mapping is a poor
architectural fit, attach a **caveat string** to `manifest.caveats[]` describing
the mismatch and why the reading may be skewed. Never silently omit the run.

## Rationale

- A blank cell is *more* misleading than a caveated number — readers assume "not
  measured" or "failed", or fill it in with a guess.
- The caveat makes the limitation explicit and located next to the data.
- Trends and curve shapes across load levels are still informative even when
  absolute values are not directly comparable.

## Alternatives considered

- **Omit mismatched cells** — gaps invite worse inferences than caveats.
- **Only report matched workloads per platform** — no common ground to compare on.

## Consequences

- `summary.txt` prints caveats; `benchrunner report` renders them as a
  highlighted sub-row under the platform.
- [../workloads/mismatches.md](../workloads/mismatches.md) enumerates the known
  mismatches and the exact caveat wording.
- Reviewers must read the caveats before quoting any mismatched number.
