# Campaign 2026-09-14 · `local-small`

| | |
| --- | --- |
| Campaign id | `20260914T173528Z-local-small` (raw output: `results/_campaigns/20260914T173528Z-local-small/`) |
| Command | `scripts/run-all.sh configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml local-small fabric-cft fabric-bft drunix fabricx` |
| Profile | `local-small`: 8 CPU / 8 GB total per platform, load generator 2 CPU on the same host, no swap |
| Block cutting | 100 messages / 1 s / 2 MB (Fabric family; Fabric-X applies count + timeout) |
| Workload | `kv-write`, uniform over 200k keys (probe-sweep) / 5k keys (smoke), 64-byte values, seed 1 |
| Host | 16 cores, 13.3 GB RAM, btrfs root, Docker 29.7.2, Linux 7.1.8 |
| Harness | manifest `harness_git_sha` = `63ab885`; the working tree also carried uncommitted changes (the Fabric-X fixes of 2026-09-14), which that field does not record |

## Results

### quick-smoke (50 TPS, 30 s): all four pass

| Platform | Confirmed TPS | Fail rate | E2E p50 | E2E p99 | Invariant |
| -------- | ------------- | --------- | ------- | ------- | --------- |
| fabric-cft | 50.0 | 0.0000 | 592 ms | 1113 ms | ok |
| fabric-bft | 50.0 | 0.0000 | 662 ms | 1178 ms | ok |
| drunix | 50.0 | 0.0000 | 1184 ms | 2189 ms | ok |
| fabricx | 50.0 | 0.0000 | 880 ms | 1524 ms | ok |

### probe-sweep: knee per platform

The knee is the highest step that **held**:
- failure rate ≤ 2%
- confirmed ≥ 95% of offered
- send-gap p99 ≤ 50 ms

Latencies are end-to-end, measured from the scheduled send time.

| Platform | Probe p50 (10 TPS) | Knee | Latency at knee p50 / p99 | First failing step | Headline (hold at 90% of knee) | Run status |
| -------- | ------------------ | ---- | ------------------------- | ------------------ | ------------------------------ | ---------- |
| fabric-cft | 656 ms | **1000 TPS** | 233 / 335 ms | 2000: 1069.5 confirmed, 46.5% failed | none: `peer0.org2` panicked during 3500 | `container-failed` |
| fabric-bft | 608 ms | **500 TPS** | 250 / 357 ms | 1000: 678.9 confirmed, 32.1% failed | **450 TPS, 0% fail, p50/p99 273 / 532 ms** | `ok` |
| drunix | 1291 ms | **500 TPS** | 1719 / 2080 ms | 1000: 533.5 confirmed, 46.7% failed | none: `lp1.org1` OOM-killed during 2000 | `container-failed` |
| fabricx | 887 ms | **3500 TPS** | 5411 / 6386 ms | 5000: 2403.5 confirmed, 51.9% failed | none: `fabricx-arma` OOM-killed during 7500 | `container-failed` |

Full per-step tables: each platform's `probe-sweep/summary.txt`.

Plots (`plots/`):
- one curve per platform: `<platform>-probe-sweep.png`
- `overlay-probe-sweep.png`: all platforms on one chart

Points at 0 TPS / 0 ms are steps where the platform had already died, not
measurements.

Cross-platform report for this campaign: [`comparison.html`](comparison.html).
It excludes runs without a headline and gives the reason.

### How to read these numbers

- **Only fabric-bft has a quotable headline.** For the other three, the knee is
  a measured, valid step. The headline (a 5-minute hold at 90% of the knee) never
  ran, because a platform container died two steps past the knee before the ladder
  could stop. By the rejection rules, those runs are not complete measurements.
  Quote their knees as "highest held step", not as sustained throughput.
- **Fabric-X's knee is a throughput knee, not a latency knee.** At 3500 TPS
  every transaction committed (so the step held), but p50 rose to 5.4 s. Up to
  2000 TPS, p50 stayed ≈ 0.5 s. The "held" rules do not include a latency
  ceiling, so read the latency column next to the knee.
- **A trickle is slower than moderate load.** Probe latency (10 TPS) is higher
  than at 250–1000 TPS on every platform. That is the 1 s block timeout: at low
  rates blocks close on the timer, not on the 100-message count.
- **Scale-out platforms are starved here.** Fabric-X runs 16 Arma processes plus
  a committer pipeline inside 8 CPU / 8 GB. Its local-small numbers show the shape
  of its behaviour, not its ceiling.
- **Comparable only to other `local-small` runs.**

## Caveats

- **State DB not equal.** fabric-cft and fabric-bft used LevelDB. Drunix used
  YugabyteDB and Fabric-X used PostgreSQL (each platform's only working store).
  Automatic `state-db parity not held` caveat.
- **Drunix payload.** Write values are JSON-wrapped (slightly larger than 64 bytes
  on the wire).
- **Drunix latency clamping.** 4598 latency observations were out of range and
  clamped (negative, i.e. T3 before T1/T2, or over 5 min). The result files do not
  say which phases they fell in. 4598 is fewer than the 7000 transactions that
  failed at sweep-1000, so the overloaded steps are the likely source, but this is
  unverified. Worth checking in the Drunix adapter's timestamping before quoting
  Drunix latency.
- **Crypto.** ECDSA-P256 per-transaction endorsement verification on all four
  (Fabric-X: client-signed, no peer endorsement round-trip).
- **Native metrics.** Scrapes failed to parse on fabric-cft, fabric-bft and
  fabricx, and were refused on drunix. Informational only; no harness number
  depends on them.

### Host conditions

The preflight check flagged these, so treat absolute numbers with some care:
- 9.3 GB of the 11 GB the profile budgets was free at start (desktop apps
  running). Swap was cleared before the run.
- The page cache was **not** dropped between runs (no passwordless sudo).
- 35 GB disk free.

## Failures, with evidence

| Step | What died | Evidence | Assessment |
| ---- | --------- | -------- | ---------- |
| fabric-cft probe-sweep, sweep-3500 | `peer0.org2` exit 2, then its chaincode | `fabric-cft/probe-sweep/capture/logs/peer0.org2.example.com.log`: `panic: Cannot commit block to the ledger due to write .../blockfile_000011: input/output error` | not OOM (374 MB of 2.73 GB), not disk full, no kernel error. Failed host write under overload on btrfs; cause unknown. [REMAINING-WORK §5](../../docs/REMAINING-WORK.md#5-fabric-cft-peer-panic-on-a-failed-disk-write) |
| drunix probe-sweep, sweep-2000 | `lp1.org1` OOM-killed (exit 137), then its chaincode | `drunix/probe-sweep/capture/inspect/lp1.org1.json` | backlog past the knee outgrows the Lite Peer's memory share. [REMAINING-WORK §3](../../docs/REMAINING-WORK.md#3-drunix-committing-peer-exit-under-overload) |
| fabricx probe-sweep, sweep-7500 | `fabricx-arma` OOM-killed (exit 137) at 2048 MB | `fabricx/probe-sweep/capture/` | all 16 Arma processes share one container's memory share; overload past the knee exhausts it |

Common pattern: the ladder aborts after **2** consecutive failing steps. The
second failing step is far enough into overload that a container dies before the
abort, so the hold phase is never reached.

## Layout

```
2026-09-14-local-small/
├── README.md               this file
├── SUMMARY.tsv             status per platform × config
├── comparison.html         campaign comparison report
├── run-all.log             full campaign log
├── plots/                  <platform>-probe-sweep.png, overlay-probe-sweep.png
└── <platform>/<config>/
    ├── summary.txt         start here
    ├── result.json, manifest.json, phases.csv (probe-sweep), monitoring-report.html
    ├── run.log             benchrunner log
    ├── campaign-run.log, deploy.log, teardown.log
    ├── SOURCE              original results/ directory this was copied from
    ├── container-logs/     (failed runs) tail of each container's log
    └── capture/            (failed runs) docker inspect, logs, events, host state
```

How to read `summary.txt`, `result.json` and `manifest.json`:
[README → Results](../../README.md#12-results-where-they-are-and-how-to-read-them).
