# ADR-012: LevelDB for all normalized runs

## Context

Fabric/Drunix support LevelDB, CouchDB and (Drunix) YugabyteDB for world state.
CouchDB vs LevelDB alone swings Fabric throughput ~3–5× and latency more.
Fabric-X has its own Fabric-compatible state layer.

## Decision

- **Normalized runs**: LevelDB everywhere. `PlatformTopo.EffectiveStateDB(true)`
  in the harness returns `leveldb` unconditionally; deploy scripts bring the
  network up with `-s leveldb`.
- **Native runs**: the profile's `state_db` value is honoured (CouchDB, or
  YugabyteDB for Drunix), recorded in the manifest.

## Rationale

- The state backend is a deployment choice, not part of a platform's consensus or
  transaction model. Holding it constant isolates what the comparison is about.
- LevelDB is the lowest-overhead common option, so the normalized numbers reflect
  the protocol path, not the database.
- Drunix's YugabyteDB is a genuine differentiator — but it belongs in the native
  run, flagged as a variable, not smuggled into the parity comparison.

## Alternatives considered

- **CouchDB everywhere** — rich-query capability nobody's normalized workload
  uses, plus a large and uneven performance tax.
- **Each platform's default** — defaults differ; not comparable.

## Consequences

- Drunix normalized runs do not exercise its SQL state path — that is the point;
  see the native run for it.
- The manifest's `state_db` field must be checked when comparing two runs.
