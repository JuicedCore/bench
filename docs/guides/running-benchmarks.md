# Running benchmarks

## The loop

```
setup (deploy)  ->  source connection.env  ->  run  ->  inspect  ->  teardown
```

`scripts/run-all.sh` does this for several platforms with isolation between each.

## Bring-up / tear-down / clean (local)

| Command | Effect |
| ------- | ------ |
| `make up-all` (or `bash scripts/up-all.sh local [fabric-cft\|fabric-bft\|drunix]`) | monitoring stack + **one** Fabric-family network (they share :7051/:7050 — mutually exclusive) + `fabricx` and `neuchain` if their images exist |
| `make down-all` (or `bash scripts/down-all.sh local`) | stop + remove every platform's containers/volumes + monitoring. Keeps images, `.cache/` clones, and `results/`. |
| `make clean` (or `bash scripts/clean.sh`) | `down-all` **plus** generated chaincode images, dangling bench volumes, `deploy/docker/**/.cache/`, `connection.env` files, `results/*`, `docs/reports/*.html\|png`. Prompts first (`-y` to skip). |
| `make clean-images` (or `scripts/clean.sh --images`) | `clean` + removes the pulled platform images too (Fabric, `npcioss/drunix-*`, `yugabytedb/yugabyte`, `eqalpha/keydb`, `bench/neuchain*`, `bench/fabricx-rest` — ~4–6 GB) |

None of these touch containers/images/volumes the harness did not create.

For a **fair** cross-platform comparison, still run platforms **sequentially**
with inter-run isolation via `scripts/run-all.sh` — `up-all` is for having
things up to poke at, not for a measured campaign.

## Configs

`configs/` holds reusable run definitions. Override `platform` / `profile` on the
CLI. Key blocks:

| Block | Purpose |
| ----- | ------- |
| `load` | mode (open/closed loop), rate/ramp/workers, key distribution, `seed` |
| `load.sweep` | probe-and-sweep (set `enabled: true`) |
| `metrics` | fixed `warmup` / `cooldown`, `output_dir`, `output_format` (json/csv) |
| `system_metrics` | 1 s host/container sampling; `container_names` filters `docker stats` |
| `adapter` | connection material — usually `${BENCH_ADAPTER_*}` from `connection.env` |

`${VAR}` in a config is expanded from the environment at load time.

## Choosing a config

| Goal | Config |
| ---- | ------ |
| "does it work" | `quick-smoke.yaml` (30 s) |
| find capacity + latency curve | `probe-sweep.yaml` (the primary methodology) |
| rough saturation band, fast | `throughput-scan.yaml` (ramp) |
| clean tail latency below saturation | `latency-profile.yaml` (set `target_tps` ≈ 60% of the knee) |
| MVCC / conflict behaviour | `contention.yaml` (Zipfian + transfer) |
| many concurrent clients | `multi-client.yaml` (closed-loop, 256 workers) |

## Output

`results/<platform>/<timestamp>/`:

| File | Contents |
| ---- | -------- |
| `manifest.json` | every fairness lever + platform version + git SHA + caveats |
| `result.json` | all phases, headline, full HDR snapshots, system samples, native scrape |
| `summary.txt` | human-readable table + warnings |
| `phases.csv` | one row per phase (when `output_format: csv`) — feed to `scripts/plot.py` |

## Validating a run

Reject the run if `summary.txt` shows any of:

- `invariant_ok=false` — a submitted tx never reached a terminal state.
- `WARNING ... send-gap p99` — the load generator was the bottleneck; give it
  more cores (`load_gen_cpus` in the profile) or reduce `target_tps`.
- failure rate unexpectedly high on a `uniform` low-rate run — likely a deploy
  problem, not the platform.

Cross-check headline TPS against the platform's own block height / logs over the
run window.

## Comparing

```
./bin/benchrunner report --results-dir ./results --output docs/reports/comparison.html --since 2026-09-13
# --since scopes the report to one campaign. Runs that fail the rejection rules in
# docs/architecture/fairness-guarantees.md are listed with the reason, never averaged in;
# normalized and native runs are separate sections. Replicates report median and min-max.
```

Only harness metrics appear. `normalized=true` and `normalized=false` rows are
never mixed in one ranking. Caveat rows appear under their platform.
