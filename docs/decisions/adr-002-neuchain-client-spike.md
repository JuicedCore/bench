# ADR-002: NeuChain client via a time-boxed proto spike, then decide

## Context

NeuChain (C++, VLDB 2022) ships a `user` client binary. Its transaction format,
signing, and epoch/batch assignment live in C++. The harness is Go
([adr-001](adr-001-go-harness.md)). Reimplementing tx construction in Go risks
measuring a buggy reimplementation instead of NeuChain.

## Decision

Before committing to an adapter design, run a **time-boxed spike**:

1. Extract `.proto` files (record upstream commit in `proto/ORIGIN`), generate Go
   stubs.
2. Submit one transaction end to end from Go.
3. If tx format + signing + epoch/batch semantics port cleanly →
   **pure-Go adapter**.
4. If not → **wrap the native `user` binary** (exec or a thin control socket)
   that emits T1/T2/T3, consistent with using each platform's own SDK elsewhere.

Either outcome is documented in full in
[../platforms/neuchain-client-implementation.md](../platforms/neuchain-client-implementation.md):
what was reimplemented, which Go file holds it, which C++ source it mirrors,
proto origin, deviations.

## Rationale

- A pure-Go client is the cleanest adapter *if* the port is faithful; the spike
  is the cheapest way to learn whether it is.
- The native-binary fallback removes the correctness risk entirely at the cost of
  an extra process boundary — acceptable, and no worse than treating the Fabric
  Gateway SDK as a black box.
- Deferring the choice avoids sinking days into a Go port that then has to be
  abandoned.

## Alternatives considered

- **Commit to pure-Go now** — highest risk, no new information.
- **Commit to the native wrapper now** — safe, but forfeits a cleaner adapter and
  a Go-native understanding of the tx format that helps debugging.
- **cgo** — see [adr-001](adr-001-go-harness.md).

## Consequences

- Phase 4 starts with a spike task, not adapter code.
- `neuchain-client-implementation.md` is a required deliverable regardless of
  outcome.
- Any port simplification becomes a manifest caveat on NeuChain runs.
