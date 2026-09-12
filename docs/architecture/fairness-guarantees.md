# Fairness guarantees

What makes a cross-architecture comparison defensible, and where the honest
answer is "these numbers are not directly comparable, here is why".

## Comparable — harness-level metrics

Collected the same way for every platform, from the same code path:

| Metric | Source | Notes |
| ------ | ------ | ----- |
| Confirmed TPS | `metrics.Collector`, T3 timestamps | all platforms |
| End-to-end latency p50…p99.99 | `metrics.Collector`, T3 − scheduled | all platforms |
| Submit latency | T2 − T1 | **NeuChain: N/A** — its ZeroMQ PUB is fire-and-forget, so `AckTime = now` measures a local call return, not a platform acknowledgement. Ignore its submit/commit split. Fabric-X reports a real ack: the Arma router accepts the envelope for ordering strictly before commit. |
| Commit latency | T3 − T2 | same caveat for NeuChain |
| Failure rate | invalid + errored + timed-out over submitted | all platforms, but see the breakdown caveat below |
| Host CPU / memory / disk | node_exporter + cAdvisor, or the built-in `docker stats` sampler | all platforms |

For NeuChain, compare **E2E latency and confirmed TPS only**; its T1/T2/T3
breakdown is not meaningful. Fabric-X's is, since it moved to the native gRPC
path ([adr-016](../decisions/adr-016-fabricx-native-grpc.md)).

### Where T3 is stamped

E2E latency is only comparable if every adapter stamps finality at the same kind
of instant — the moment the platform's own commit signal was *observed*, never
the moment the harness got round to reading it:

| Platform | T3 |
| -------- | -- |
| fabric-cft / fabric-bft | `Commit.StatusWithContext` returns (push-based) |
| drunix | the Committing Peer's filtered-block event is decoded |
| fabricx | the sidecar's deliver stream yields the block carrying the transaction |
| neuchain | the poller decodes the block carrying the tx |

NeuChain's poller stamps once per block fetch and carries that instant through to
the caller. It must not be stamped when `WaitForFinality` returns: that would add
the poll interval, and for a transaction already resolved before the caller waited,
an unbounded amount of caller-side delay.

### The invalid/errored split is not equally observable

The aggregate failure rate is comparable. Its breakdown is not:

- **fabric / drunix** read a real validation code (`GetTxValidationCode`), so an
  MVCC conflict lands in `invalid` and a transport failure in `errored`.
- **neuchain** reads the result frame (COMMIT / ABORT), which is a genuine
  platform verdict.
- **fabricx** reads the per-transaction validation code out of the block's
  `TRANSACTIONS_FILTER` metadata — a real committer verdict, and it reports the
  block number.

The invalid/errored split is now comparable across every platform.

## Not comparable — platform-native metrics

Endorsement time, ordering time, validation-phase breakdown, block-fill ratio,
mempool depth — these come from each platform's own `/metrics` and mean different
things on each. They are stored under `native_scrapes` in `result.json` and used
**only** to explain *why* a platform behaved as it did. `benchrunner report`
excludes them from every comparison table by construction.

## Held identical for normalized runs (`normalized: true`)

See [adr-013](../decisions/adr-013-config-parity-policy.md).

Every normalized mode is **one config file serving all five platforms**
(`configs/normalized/<mode>.yaml`, selected with `--platform`). The `load:` and
`metrics:` blocks are therefore physically the same bytes for every platform, so
the levers below cannot drift; `pkg/harness/configparity_test.go` fails if anyone
reintroduces per-platform copies. Platform-specific settings live only in the
union `adapter:` block, whose irrelevant keys each adapter ignores.

| Lever | Normalized value | Enforced by |
| ----- | ---------------- | ----------- |
| World-state DB | LevelDB requested everywhere; the DB **actually used** is recorded | `PlatformTopo.EffectiveStateDB(true)` sets `state_db_requested`; deploy scripts export `BENCH_ACTUAL_STATE_DB` into `state_db`. When they differ the run carries an automatic caveat — see below |
| Orderer batch params (Fabric family) | identical `max_message_count`, `batch_timeout`, `preferred/absolute_max_bytes` across fabric-cft, fabric-bft, drunix, fabricx | `deploy/profiles/*.yaml` `orderer_batch`; recorded in manifest |
| Workload | same normalized workload, same key space, same distribution, same value size | `workloads.New` from the run config |
| Resource budget | the profile's **total** CPU/memory, split evenly across the platform's real containers (no swap) | `lib.sh apply_budget` in every `up.sh`; manifest records containers, per-container and total; unreported budget is caveated |
| RNG seed | one `seed` drives every KeyGen and the read/write chooser | manifest records it |
| Warmup / cooldown | fixed 30 s / 15 s (configurable, but the same for every platform in a comparison) | `metrics.Window` |
| Load mode + target | identical `load:` block | one shared config file per mode |
| Finality wait | identical (60 s) | shared config; a tighter budget on one platform would manufacture timeouts |
| Sweep ladder | identical `steps` for every platform | shared config; see "One ladder for every platform" below |

For **platform-native runs** (`normalized: false`) each platform is tuned to its
best and the tuning is recorded in the manifest. Native and normalized numbers
are never mixed in one comparison. Native configs live in `configs/native/`.

### State DB: requested vs actual

`state_db_requested` is what the normalized contract asked for (always LevelDB).
`state_db` is what the platform came up on, taken from `BENCH_ACTUAL_STATE_DB` in
each `up.sh`'s `connection.env`. They differ on Drunix, whose shipped
test-network only wires the full node set for YugabyteDB. Recording only the
request would have the manifest assert a parity the run does not have, so the
engine records both and appends a caveat whenever they disagree.

## One ladder for every platform

Normalized sweeps offer every platform the **same** `steps`. A platform is not
given a gentler ladder to flatter it. Two rules make that practical and honest:

- **The hold phase follows the measured knee.** The headline result is the hold
  phase, offered at `hold_fraction` × the highest step that stayed under
  `max_fail_rate` — *not* × the top of the configured ladder. A platform that
  saturates early therefore has its headline measured just below its own knee,
  not deep in overload. If no step held at all, hold falls back to the probe rate
  and the run is caveated as a floor reading, not a saturation figure.
- **Early abort.** After `abort_after_failed_steps` (default 2) consecutive steps
  over `max_fail_rate`, the remaining steps are skipped and recorded in
  `manifest.skipped_steps`, with a caveat. A platform that saturates at step 2
  stops there instead of spending the rest of the run failing steps 3..N. Set it
  to `0` to force the whole ladder.

## Disclosed, not equalized — cryptography

The EOV platforms verify an endorsement signature on **every** transaction during
validation. NeuChain's EV path does not have per-transaction endorsement
signatures at all. This is an inherent architectural difference, not something to
"fix". Each adapter reports its `CryptoInfo` and the manifest records:

```
signature_alg, hash_alg, per_tx_endorsement_verify, msp_note
```

Every comparison table and report footnotes this next to the numbers.

## Run-specific caveats

Some runs carry a caveat string in `manifest.caveats`, surfaced in `summary.txt`
and the HTML report:

- **fabricx / neuchain on the `local` profile** — Arma and NeuChain are
  scale-out designs. Constrained to 8 / 6 cores on a 16 GB host they run far
  below their published ceilings; absolute throughput is **not** comparable to
  the ~200 k TPS (Fabric-X) or VLDB (NeuChain) figures. The *shape* of the
  latency curve and the relative behaviour under contention are still
  informative. Full-scale numbers require the `gcp-full` profile.
- **Mismatched workloads on Fabric-X** (`kv-write` mapped onto a token mint) —
  see [workloads/mismatches.md](../workloads/mismatches.md) and
  [adr-010](../decisions/adr-010-mismatch-report.md).
- **State-DB parity not held** — emitted automatically whenever `state_db`
  differs from `state_db_requested` (Drunix on YugabyteDB).
- **Sweep aborted early** / **no sweep step held** — emitted automatically by the
  ladder rules above; the second means the headline is a floor, not a saturation
  figure.
- **Drunix write payloads are JSON-wrapped** — `value_size_bytes` is not the
  on-wire size for Drunix (`pkg/adapters/drunix/valuecodec.go`).

## Inter-run isolation

`scripts/run-all.sh` between every platform run: down the previous topology,
`docker system prune -f`, drop the page cache, recreate volumes, start from an
empty ledger. Sequential execution on identical hardware
([adr-005](../decisions/adr-005-sequential-runs.md)).

## Rejecting a run

A run is invalid if any of:

- `invariant_ok == false` (a submitted tx never reached a terminal state)
- `send_gap p99 > 50 ms` (the load generator was the bottleneck)
- the measurement window is shorter than warmup + cooldown for a non-probe phase
- the manifest is missing a fairness lever (state DB, batch params, seed, resource budget)
- the headline came from a hold phase with no passing sweep step (a floor
  reading quoted as a saturation figure)
- a probe-sweep's `saturation_tps` is the top of the ladder (the platform never
  saturated; that figure is a lower bound, not a knee)
