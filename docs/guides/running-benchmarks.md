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
| `make clean-images` (or `scripts/clean.sh --images`) | `clean` + removes the pulled platform images too (Fabric, `npcioss/drunix-*`, `yugabytedb/yugabyte`, `eqalpha/keydb`, `bench/neuchain*`, `bench/fabricx` — ~4–6 GB) |

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
| read path | `read-profile.yaml` (kv-read) — needs a populated ledger; see below |

To run every config on every platform, or on the GCP profiles, see the top-level
[README → Running every combination](../../README.md#running-every-combination).
`run-all.sh` deploys each platform fresh for **each** config, so `read-profile`
there reads an empty ledger: absent keys return an empty value and count as
successful reads. Do not quote `read-profile` from such a campaign; run it by hand
right after a write mode on the same deployment.

## Output

`results/<platform>/<timestamp>/`:

| File | Contents |
| ---- | -------- |
| `manifest.json` | every fairness lever + platform version + git SHA + caveats |
| `result.json` | all phases, headline, full HDR snapshots, system samples, native scrape |
| `summary.txt` | human-readable table + warnings |
| `phases.csv` | one row per phase (when `output_format: csv`) — feed to `scripts/plot.py` |
| `run.log` | the full run log |
| `monitoring-report.html` | per-run charts from Prometheus (needs the monitoring stack up) |
| `container-logs/` | only on exit 3: tail of each platform container's log |
| `error.txt` | only when adapter setup failed: the error plus what to check |

## Validating a run

Reject the run if `summary.txt` shows any of:

- `invariant_ok=false` — a submitted tx never reached a terminal state.
- `WARNING ... send-gap p99` — the load generator was the bottleneck; give it
  more cores (`load_gen_cpus` in the profile) or reduce `target_tps`.
- failure rate unexpectedly high on a `uniform` low-rate run — likely a deploy
  problem, not the platform.

Cross-check headline TPS against the platform's own block height / logs over the
run window.

## Logs and failure captures

Every campaign (`scripts/run-all.sh` local, `scripts/gcp-run.sh` GCP) writes,
under `results/_campaigns/<id>/`:

```
results/_campaigns/<id>/
├── run-all.log                        # or gcp-run.log — full interleaved log
├── SUMMARY.tsv                        # one row per platform x config
├── drunix/
│   └── probe-sweep/
│       ├── deploy.log
│       ├── run.log
│       ├── teardown.log
│       └── capture/                   # taken right before teardown, every run
│           ├── ps.txt                 # docker ps -a
│           ├── inspect/<container>.json
│           ├── logs/<container>.log   # last 5000 lines, timestamps
│           ├── events.txt             # docker die/oom/kill/restart since deploy start
│           └── host.txt               # free, df, docker system df, dmesg OOM/segfault
└── ...                                # one dir per platform/config-stem
```

`SUMMARY.tsv` columns: `platform`, `config`, `stage`, `status`, `reason`, `dir`.
`status` is one of:

| status | meaning |
| ------ | ------- |
| `ok` | run completed and produced a headline |
| `no-measurement` | benchrunner exited 0 but logged `FAILED RUN`: the headline committed nothing, so the run is not a measurement |
| `deploy-failed` | the platform never came up |
| `run-failed` | benchrunner exited non-zero for a reason other than a container failure |
| `container-failed` | a platform container exited or was OOM-killed mid-run |

`reason` is a one-line extract from the step log (last `error:` line, etc).

`benchrunner run` exit codes:

| Code | Meaning |
| ---- | ------- |
| 0 | ok |
| 1 | harness error |
| 3 | a platform container exited or was OOM-killed mid-run — results are still written, just no headline |

On exit 3, the result dir (`results/<platform>/<ts>/`) gains
`container-logs/<container>.log` (tail 5000). If the run instead aborted during
adapter setup, the result dir gets `error.txt` instead.

On GCP, `capture/` is taken on the platform VM and pulled back before the VMs
are destroyed. If a VM fails provisioning, its `/var/log/bench-install.log` is
saved as `results/_campaigns/<id>/<vm>-install.log`.

`scripts/capture.sh <out-dir> [since]` can also be run by hand against a live
network — same layout as the per-step `capture/` above.

Triage recipe:

```
cd results/_campaigns/<id>
column -t -s $'\t' SUMMARY.tsv                  # what failed, where, one-line reason
less drunix/probe-sweep/run.log                 # full step output
grep -rlE 'panic|fatal|FATAL|OOM' */*/capture/logs/   # which container logged the crash
jq '.state | {ExitCode, OOMKilled, Error, FinishedAt}' drunix/probe-sweep/capture/inspect/cp.org2.json
cat drunix/probe-sweep/capture/events.txt       # order of die/oom events
grep -A50 dmesg drunix/probe-sweep/capture/host.txt   # kernel OOM kills
```

- exit code 137 + `OOMKilled=true` → memory cap (see `apply_budget` weights in
  `deploy/docker/lib.sh`), not a bug in the platform.
- exit code 2 with `panic:` in the log → a Go panic in the platform; read the
  stack trace in `capture/logs/<container>.log`.
- exit code 0/143 during the run → something else stopped the container; check
  `capture/events.txt` for a `kill`.
- `deploy-failed` → read the tail of `deploy.log`, then `capture/logs/` for any
  container that never became healthy.

The NeuChain image build logs separately, to
`deploy/docker/neuchain/.cache/build-<UTC timestamp>.log`.

## Comparing

```
./bin/benchrunner report --results-dir ./results --output docs/reports/comparison.html --since 2026-09-13
# --since scopes the report to one campaign. Runs that fail the rejection rules in
# docs/architecture/fairness-guarantees.md are listed with the reason, never averaged in;
# normalized and native runs are separate sections. Replicates report median and min-max.
```

Only harness metrics appear. `normalized=true` and `normalized=false` rows are
never mixed in one ranking. Caveat rows appear under their platform.
