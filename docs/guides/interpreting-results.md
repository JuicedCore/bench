# Interpreting results

## summary.txt at a glance

A real one: fabric-bft probe-sweep on `local-small`, 2026-09-14 (resource lines
trimmed).

```
run:        probe-sweep
platform:   fabric-bft  (version 3.1.5)
workload:   kv-write   normalized=true
profile:    local-small   state_db=leveldb
resources:  8 CPU / 8 GB total over 8 containers (1.00 CPU each; memory by role weight peer=4 statedb=4 orderer=2 other=1)
batch:      msgcount=100 timeout=1s preferred=2 MB
crypto:     sig=ECDSA-P256 hash=SHA-256 per_tx_endorse_verify=true
caveats:
  - sweep aborted early after 2 consecutive failed steps; 4 higher step(s) were never offered

phase           offered     conf_tps  goodput  fail_rate    e2e_p50    e2e_p99    gap_p99    com_p50  verdict
probe                10         10.0     100%     0.0000     608.26    1127.42       1.14     602.62
sweep-100           100        100.0     100%     0.0000     642.05    1131.52       1.12     637.95  held
sweep-250           250        250.0     100%     0.0000     350.46     550.91       1.12     345.09  held
sweep-500           500        500.0     100%     0.0000     250.24     357.38       1.09     206.72  held
sweep-1000         1000        678.9      68%     0.3211   11747.33   11927.55       1.33   11616.26  failure rate 32.11% > 2.00%
sweep-2000         2000        520.5      26%     0.7398   27901.95   31539.20       1.80   27639.81  failure rate 73.98% > 2.00%
hold                450        450.0     100%     0.0000     272.90     532.48       1.10     230.53

errors sweep-1000       4815 x not sent: N transactions already in flight (platform is not finalizing)
sweep aborted early; steps never offered: [3500 5000 7500 10000]
detected saturation: ~500 offered TPS
HEADLINE  confirmed_tps=450.0  fail_rate=0.0000  e2e p50/p99=272.90/532.48 ms  invariant_ok=true
```

Header:

- **resources** is the budget actually applied, per container. **batch** is the
  block-cutting setting, which must match across the Fabric family. **crypto** is
  disclosed, not equalized.
- **caveats** must be read before quoting anything from the run.

Columns (latencies in ms):

| Column | Meaning |
| ------ | ------- |
| `offered` | target rate for the phase |
| `conf_tps` | committed-and-valid transactions per second in the window (T3) |
| `goodput` | `conf_tps / offered` |
| `fail_rate` | (invalid + errored + timed-out) / submitted |
| `e2e_p50`, `e2e_p99` | T3 − *scheduled* send |
| `gap_p99` | send gap (T1 − scheduled); above 50 ms the generator was the bottleneck |
| `com_p50` | commit latency, T3 − T2 |
| `verdict` | `held`, or the rule that rejected the step |

Rows:

- **probe** = floor latency (architectural cost with no load). With 1 s block
  cutting, a trickle of load waits for the timeout, so the probe can be *slower*
  than a moderate sweep step that fills blocks sooner.
- **sweep** rows: a step **holds** only if failure rate ≤ 2%, goodput ≥ 95% and
  send-gap p99 ≤ 50 ms. After 2 consecutive steps that do not hold, the ladder
  stops (`steps never offered`).
- **errors** lines group each phase's failures by message.
- **detected saturation** = the highest step that held. If every step held, the
  top step is only a lower bound and the summary says so.
- **HEADLINE** = the hold phase at 90% of the measured knee, sustained 5 min.
  Quote this, with its caveats.

## Red flags

| Sign | Meaning | Action |
| ---- | ------- | ------ |
| `invariant_ok=false` | a submitted tx never reached a terminal state | discard the run; check finality detection / timeouts |
| `WARNING ... send-gap p99` | load generator was the bottleneck | more `load_gen_cpus`, or lower the rate |
| `fail_rate` high on a low-rate uniform run | deploy problem, not the platform | re-deploy |
| `FAILED RUN` / `HEADLINE none` | the headline phase committed nothing | not a 0 TPS measurement; see [troubleshooting](../README.md#troubleshooting) |
| `conf_tps` ≫ `offered` | clock / windowing bug or double-count | inspect `result.json` phase windows |
| manifest `container_failures` non-empty / `benchrunner run` exited 3 | a container exited or was OOM-killed mid-run — no headline | read `container-logs/` in the result dir and the campaign's `capture/` |

## The throughput-latency curve

`scripts/plot.py results/<p>/<ts>/phases.csv` → `curve.png`. X = offered TPS,
left Y = confirmed TPS, right Y = e2e p50 / p99. Graceful degradation = latency
climbs roughly linearly past the knee; a cliff = latency spikes vertically at
saturation. Overlay multiple platforms:
`scripts/plot.py results/*/*/phases.csv`.

## What you may and may not compare

**May** (harness metrics, identical collection): confirmed TPS, e2e/submit/commit
latency percentiles, failure rate, host CPU/mem/disk.

**May not**: anything under `native_scrapes` in `result.json` (endorsement time,
ordering time, validation breakdown) — different meaning per platform. Use them
only to explain a platform's own curve.

**Read the caveats** in `manifest.caveats` before quoting any number — especially
for Fabric-X / NeuChain on the `local` profiles, and any `kv-read` run.

## Comparing two runs of the same platform

Diff the manifests first. If `state_db`, `orderer_batch`, `seed`, resource limits
or `platform_version` differ, the throughput difference may be entirely that.
