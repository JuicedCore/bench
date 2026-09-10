# Interpreting results

## summary.txt at a glance

```
run:        probe-sweep
platform:   fabric-cft  (version 2.5.11)
workload:   kv-write   normalized=true
profile:    local   state_db=leveldb
batch:      msgcount=100 timeout=1s preferred=2 MB
crypto:     sig=ECDSA-P256 hash=SHA-256 per_tx_endorse_verify=true

phase        offered     conf_tps    fail_rate    e2e_p50    e2e_p99    sub_p50    com_p50
probe             10          9.9       0.0000      14.20      22.10       3.10      10.90
sweep-100        100         99.6       0.0000      15.80      28.40       3.20      12.10
sweep-1000      1000        980.2       0.0010      24.10      70.30       4.10      19.5
sweep-5000      5000       3120.4       0.1800     240.00    1450.00      9.80     220.0
...
detected saturation: ~2000 offered TPS
HEADLINE  confirmed_tps=1810.5  fail_rate=0.0090  e2e p50/p99=41.2/180.0 ms  invariant_ok=true
```

- **probe** = floor latency (architectural cost with no load). Compare this
  number across platforms first — it is the cleanest comparison.
- **sweep** rows: watch where `conf_tps` stops tracking `offered` and where
  `e2e_p99` turns the corner. That knee is the real capacity.
- **detected saturation** = highest sweep step with `fail_rate ≤ max_fail_rate`.
- **HEADLINE** = the hold phase (90% of the knee, sustained). This is the number
  to quote — with its caveats.

## Red flags

| Sign | Meaning | Action |
| ---- | ------- | ------ |
| `invariant_ok=false` | a submitted tx never reached a terminal state | discard the run; check finality detection / timeouts |
| `WARNING ... send-gap p99` | load generator was the bottleneck | more `load_gen_cpus`, or lower the rate |
| `fail_rate` high on a low-rate uniform run | deploy problem, not the platform | re-deploy |
| `conf_tps` ≫ `offered` | clock / windowing bug or double-count | inspect `result.json` phase windows |

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
for Fabric-X / NeuChain on `local`, and any Fabric-X `kv-*` run.

## Comparing two runs of the same platform

Diff the manifests first. If `state_db`, `orderer_batch`, `seed`, resource limits
or `platform_version` differ, the throughput difference may be entirely that.
