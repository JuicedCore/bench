# ADR-006: HDR Histogram for latency aggregation

## Context

At thousands of TPS a run produces millions of latency samples. Tail percentiles
(p99, p99.9, p99.99) are the interesting part and are exactly where naive methods
fail.

## Decision

Every latency series (end-to-end, submit, commit, send-gap) is recorded in an HDR
histogram: 1 µs … 5 min range, 3 significant figures. Per-worker histograms are
merged. The full histogram is exported in `result.json`.

## Rationale

- **Bounded memory** regardless of sample count — no keeping every value.
- **Correct tail percentiles** — fixed relative error (3 sig figs) across the
  whole range, unlike reservoir sampling which loses tail fidelity.
- **Mergeable** — per-worker histograms combine exactly, so the hot path takes no
  shared lock.
- Standard in performance engineering; comparable to how other systems report.

## Alternatives considered

- **Sort all samples, index percentiles** — O(n) memory, GC pressure at millions
  of samples, infeasible for long runs.
- **t-digest** — also mergeable and accurate, but HDR's fixed-error guarantee is
  simpler to reason about and the Go library is mature.
- **Prometheus histogram buckets** — bucket boundaries fixed at scrape-config
  time; too coarse for cross-platform latency comparison.

## Consequences

- Latencies below 1 µs clamp to 1 µs (irrelevant here).
- Values above 5 min clamp to the max and are effectively "timed out" anyway.
- `metrics.Snapshot` is the only public shape; the reporter and tests depend on
  its fixed percentile key set (`p1 … p99.99`).
