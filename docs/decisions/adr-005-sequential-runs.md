# ADR-005: Benchmark runs are sequential, never concurrent

## Context

Comparing platforms means each must see the same hardware under the same
conditions.

## Decision

One platform at a time, on identical hardware, with full inter-run isolation
between platforms:

- `docker compose down` the previous topology + `docker system prune -f`
- drop the page cache (`echo 3 > /proc/sys/vm/drop_caches`)
- recreate volumes; start from an empty ledger
- fixed settle delay before load starts

Enforced by `scripts/run-all.sh`.

## Rationale

- Concurrent platforms contend for CPU, memory bandwidth, disk and page cache;
  the result is noise attributable to the scheduler, not the platform.
- Warm page cache from a previous run inflates the next platform's early
  throughput; dropping it makes runs comparable.
- Sequential execution is slower in wall-clock but the only defensible option.

## Alternatives considered

- **Concurrent on one big host** — cross-interference; rejected.
- **One host per platform, run in parallel** — hardware must then be provably
  identical (same SKU, same firmware, same disk); harder to guarantee than reusing
  one host sequentially. Viable on GCP with identical machine types but still run
  sequentially there to keep methodology uniform.

## Consequences

- A full suite across 5 configs × several workloads takes hours; plan for it.
- `run-all.sh` is the canonical entry point; ad-hoc `benchrunner run` loops skip
  isolation and are for development only.
