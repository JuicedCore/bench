# Fairness guarantees

What makes a cross-architecture comparison defensible, and where the honest
answer is "these numbers are not directly comparable, here is why".

## Comparable — harness-level metrics

Collected the same way for every platform, from the same code path:

| Metric | Source | Notes |
| ------ | ------ | ----- |
| Confirmed TPS | `metrics.Collector`, T3 timestamps | all platforms |
| End-to-end latency p50…p99.99 | `metrics.Collector`, T3 − scheduled | all platforms |
| Submit latency | T2 − T1 | **Fabric-X: N/A** — the tokens REST POST is synchronous to finality, so there is no separable ack. The adapter reports `AckTime = now`; ignore Fabric-X submit/commit split. |
| Commit latency | T3 − T2 | same caveat for Fabric-X |
| Failure rate | invalid + errored + timed-out over submitted | all platforms |
| Host CPU / memory / disk | node_exporter + cAdvisor, or the built-in `docker stats` sampler | all platforms |

For Fabric-X, compare **E2E latency and confirmed TPS only**; the T1/T2/T3
breakdown is not meaningful (see adr-003).

## Not comparable — platform-native metrics

Endorsement time, ordering time, validation-phase breakdown, block-fill ratio,
mempool depth — these come from each platform's own `/metrics` and mean different
things on each. They are stored under `native_scrapes` in `result.json` and used
**only** to explain *why* a platform behaved as it did. `benchrunner report`
excludes them from every comparison table by construction.

## Held identical for normalized runs (`normalized: true`)

See [adr-013](../decisions/adr-013-config-parity-policy.md).

| Lever | Normalized value | Enforced by |
| ----- | ---------------- | ----------- |
| World-state DB | LevelDB everywhere | `PlatformTopo.EffectiveStateDB(true)` forces `leveldb`; deploy scripts read it |
| Orderer batch params (Fabric family) | identical `max_message_count`, `batch_timeout`, `preferred/absolute_max_bytes` across fabric-cft, fabric-bft, drunix, fabricx | `deploy/profiles/*.yaml` `orderer_batch`; recorded in manifest |
| Workload | same normalized workload, same key space, same distribution, same value size | `workloads.New` from the run config |
| RNG seed | one `seed` drives every KeyGen and the read/write chooser | manifest records it |
| Warmup / cooldown | fixed 30 s / 15 s (configurable, but the same for every platform in a comparison) | `metrics.Window` |
| Load mode + target | identical `load:` block | run config |

For **platform-native runs** (`normalized: false`) each platform is tuned to its
best and the tuning is recorded in the manifest. Native and normalized numbers
are never mixed in one comparison.

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
- the manifest is missing a fairness lever (state DB, batch params, seed)
