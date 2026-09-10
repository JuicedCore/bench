# ADR-013: Config parity policy — identical for normalized, tuned for native

## Context

Two defensible stances: run every platform on its recommended/best config
("best-effort" comparison) or hold config identical ("apples-to-apples"). Each has
a failure mode.

## Decision

- **Normalized runs** (`normalized: true`): identical config across platforms —
  LevelDB ([adr-012](adr-012-state-db-leveldb.md)), identical Fabric-family
  orderer batch params ([adr-011](adr-011-orderer-batch-params.md)), same crypto
  family, same load block, same seed, same windows.
- **Native runs** (`normalized: false`): each platform tuned to its best; the
  tuning is recorded in the manifest (`state_db`, `orderer_batch`, and free-text
  knobs in `caveats`).
- Normalized and native numbers are **never** mixed in one comparison table.

## Rationale

- Identical-config normalized runs are reproducible and easy to defend against
  "you tuned one and not the other".
- Per-platform-tuned native runs answer "what can it actually do" without letting
  that leak into the parity comparison.
- Splitting the two keeps each honest.

## Alternatives considered

- **Per-platform tuned everywhere** — harder to defend as apples-to-apples; more
  setup; every result needs a tuning disclaimer.
- **Identical config everywhere (native included)** — denies each platform its
  real ceiling; native numbers become meaningless.

## Consequences

- The run matrix roughly doubles ([adr-009](adr-009-workload-strategy.md)).
- `RunConfig.Normalized` drives engine behaviour (state-db override) and manifest
  labelling; the report keys on it.
