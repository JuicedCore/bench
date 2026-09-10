# Metrics methodology

## The T1/T2/T3 timeline

```
scheduled      T1            T2                         T3
   │  send gap  │  submit     │   ordering + validation  │
   │◄──────────►│◄───────────►│◄────────────────────────►│
   │            │             │                          │
   │            │   submit latency (T2−T1)               │
   │            │◄───────────────────────────────────────┤  commit latency (T3−T2)
   │                                                     │
   │◄────────────────  end-to-end latency  ──────────────►│   (T3 − scheduled)
```

- **scheduled** — when the load generator *intended* to send this transaction.
- **T1** — captured by the generator immediately before `adapter.Submit`. Measured
  identically for every platform.
- **T2** — the platform acknowledges receipt: Fabric/Drunix after the orderer
  accepts the broadcast; Fabric-X after the FSC view accepts; NeuChain after the
  submit RPC returns.
- **T3** — the adapter observes the transaction in a validated/committed block.

Reporting only T1→T2 (as some blockchain benchmarks do) understates real latency
by the entire ordering + validation + commit cost. This harness always reports
**T3 − scheduled** as the headline latency and **T3-based** throughput.

## Throughput

```
confirmed TPS = committed-and-valid transactions in window / wall-clock seconds of window
```

- Uses **T3** timestamps, never T1.
- **Invalid** transactions (MVCC read conflict, endorsement-policy failure,
  double-spend) are failures, not throughput.
- `offered TPS = submitted / wall-clock` is reported alongside so saturation is
  visible (offered climbs, confirmed plateaus).

## Coordinated omission

Open-loop load is scheduled against an absolute timeline: transaction *k* is due
at `start + k / rate`. Latency is measured from that **scheduled** time, not from
the moment the generator actually managed to call `Submit`. If the generator
falls behind (its own CPU limit, a slow adapter call), the queueing delay shows
up in the latency distribution instead of being hidden.

The generator records the **send gap** (`T1 − scheduled`) as its own HDR
histogram. If `send_gap p99 > 50 ms` the `summary.txt` prints a warning: the load
generator, not the platform, was the bottleneck and the run should be rejected or
re-run with more load-generator cores (see `load_gen_cpus` in the profile).

Closed-loop load has no offered-rate schedule, so `scheduled == T1` there by
construction.

## Windowing — fixed durations, not percentages

Every run discards a fixed **warmup** (default 30 s) at the start and a fixed
**cooldown** (default 15 s) at the end of each phase. Fixed absolute windows —
not "first 10% / last 5%" — so a 2-minute run and a 10-minute run discard the
same slice and stay comparable. Records are included when their **scheduled send
time** falls in `[phaseStart + warmup, phaseEnd − cooldown)`.

If a phase is shorter than warmup + cooldown (e.g. the probe), the whole phase is
measured.

## Latency aggregation — HDR histogram

Every latency series (end-to-end, submit, commit, send-gap) is an HDR histogram
covering 1 µs … 5 min at 3 significant figures. Reported spectrum:

```
p1 p5 p10 p25 p50 p75 p90 p95 p99 p99.9 p99.99   + min max mean stddev
```

Per-worker histograms are merged, not locked on the hot path. The full histogram
is in `result.json` for post-hoc analysis.

## Probe-and-sweep

Set `load.sweep.enabled: true`.

| Phase | Default | Measures |
| ----- | ------- | -------- |
| **probe** | 10 TPS for 30 s | Floor latency — the architectural cost with no load. |
| **sweep-N** | 100, 500, 1 000, 2 000, 5 000, 10 000, 20 000 TPS, 60 s each | Confirmed TPS + full percentile latency at each offered rate. |
| **hold** | 90% of the highest sweep step whose failure rate stayed ≤ `max_fail_rate` (default 0.02), for 5 min | Stability under sustained near-peak load. |

`result.json` carries every phase; `saturation_tps` is the detected knee; the
`hold` phase is the headline. Plot offered TPS (x) against confirmed p50/p99
latency (y) to get the hockey-stick curve.

## The correctness invariant

For every aggregation window:

```
submitted == committed + invalid + errored + timed_out
```

Any gap means a submitted transaction never reached a terminal state — the
collector never saw its finality. `result.json` sets `invariant_ok: false` and
`summary.txt` flags it. A run with a broken invariant is not a valid measurement.

## Host and platform metrics

- **Host resources** — Prometheus + node_exporter + cAdvisor at 1 s (the
  monitoring Compose stack), plus a built-in `docker stats` sampler so a run
  still has a coarse CPU/memory trace when the stack is down.
- **Platform-native** — one `/metrics` scrape at end of run, stored under
  `native_scrapes`. **Never** used in cross-platform comparison — see
  [fairness-guarantees.md](fairness-guarantees.md).
