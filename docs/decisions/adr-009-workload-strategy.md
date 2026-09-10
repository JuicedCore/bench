# ADR-009: Both platform-native and normalized workloads (Option C)

## Context

The four platforms cannot run one shared smart contract. Two ways to benchmark:
normalize the work (fair, but may not play to any platform's strengths) or run
each platform's native workload (shows ceilings, not comparable).

## Decision

Do **both**, kept strictly separate:

- **Normalized** (`normalized: true`) — `kv-write`, `kv-read`, `kv-mixed`,
  `transfer`, implemented natively per platform, identical config
  ([adr-013](adr-013-config-parity-policy.md)). This is the cross-platform
  comparison.
- **Platform-native** (`normalized: false`) — each platform's best-fit workload,
  tuned to its best config. This is the ceiling reference, reported separately.

Every methodology choice is documented under `docs/`.

## Rationale

- Normalized-only invites "you crippled platform X by not using its native
  primitive".
- Native-only invites "you compared apples to oranges".
- Together they answer both "how do they compare on the same work" and "what can
  each one do at its best", without conflating them.

## Alternatives considered

- **Normalized only** — simpler, but weaker against the strengths critique.
- **Native only** — not a comparison.

## Consequences

- Roughly 2× the run matrix.
- Report has two sections; the HTML report tags rows `normalized=true|false` and
  never mixes them in one ranking.
- Mismatched normalized workloads (Fabric-X KV) still run, with caveats
  ([adr-010](adr-010-mismatch-report.md)).
