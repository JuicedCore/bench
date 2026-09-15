# Documentation

The guide to this harness, including the full documentation map, reading order
and every ADR, is the top-level **[README.md](../README.md)**. Start there:
[documentation map](../README.md#documentation-map).

This page holds the error lookup that the CLI, the engine and the campaign
scripts point to.

## Troubleshooting

### Where to look first

| You ran | Look at |
| ------- | ------- |
| `benchrunner run` | `results/<platform>/<timestamp>/summary.txt`, then `run.log`; `error.txt` if setup failed; `container-logs/` if exit code 3 |
| `scripts/run-all.sh` / `make bench` | `column -t -s $'\t' results/_campaigns/<id>/SUMMARY.tsv`, then `<platform>/<config>/{deploy,run,teardown}.log` and `capture/` |
| `scripts/gcp-run.sh` / `make gcp` | same as run-all, plus `results/_campaigns/<id>/<vm>-install.log` if a VM failed provisioning |

Full layout of the capture directory and a triage recipe:
[guides/running-benchmarks.md#logs-and-failure-captures](guides/running-benchmarks.md#logs-and-failure-captures).

### `benchrunner` exit codes

| Code | Meaning | Next step |
| ---- | ------- | --------- |
| 0 | run finished | still read `summary.txt`: a run that committed nothing exits 0 and prints `FAILED RUN` |
| 1 | error | message on stderr; for `run`, `error.txt` / `run.log` in the result dir |
| 2 | usage error | `benchrunner <command> --help` |
| 3 | a platform container exited or was OOM-killed mid-run | `container-logs/` in the result dir; the run has no headline |

### Campaign `SUMMARY.tsv` status

| Status | Meaning | Next step |
| ------ | ------- | --------- |
| `ok` | the run produced a headline | read `summary.txt`; check caveats and the rejection rules |
| `no-measurement` | benchrunner exited 0 but logged `FAILED RUN` (headline committed nothing, or no step held) | `run.log` phase errors: usually the platform stopped finalizing |
| `deploy-failed` | the platform never came up, or `connection.env` was not written | tail of `deploy.log`, then `capture/logs/` for containers that never became healthy |
| `run-failed` | benchrunner exited non-zero for a reason other than a container failure | last `error:` line of `run.log`; `error.txt` in the result dir |
| `container-failed` | a platform container exited or was OOM-killed mid-run | `capture/inspect/<c>.json` (`ExitCode`, `OOMKilled`), `capture/logs/<c>.log`, `capture/events.txt` |

### Error lookup

| Symptom | Cause | Fix |
| ------- | ----- | --- |
| `adapter.<key> is required (... is deploy/docker/<p>/connection.env sourced?)` | connection material not in the environment; an unset `${BENCH_ADAPTER_*}` expands to `""` | `set -a; source deploy/docker/<platform>/connection.env; set +a` in the same shell, then rerun |
| setup fails with `unreachable` / `not reachable within the dial timeout` / `connection refused` | network down, or the wrong platform's `connection.env` | `docker ps`; bring it up with `deploy/docker/<p>/up.sh <profile>` |
| TLS / certificate errors on setup | the network was redeployed and regenerated its crypto; your `connection.env` is from the old one | source the freshly written `connection.env` |
| `port is already allocated` on deploy | another Fabric-family network (they share 7050/7051) or a leftover stack is up | `make down-all`, then deploy one platform |
| `container-failed`, exit 137, `OOMKilled=true` | the container hit its memory cap | memory split is `apply_budget` in `deploy/docker/lib.sh`; use a bigger profile, or check that preflight passed |
| `container-failed`, exit 2, `panic:` in the log | a Go panic inside the platform | read the stack trace in `capture/logs/<c>.log`. A `write ... input/output error` panic is a failed host disk write, not a platform bug: check `df`, `journalctl -k`, then rerun |
| `not sent: N transactions already in flight (platform is not finalizing)` | the platform stopped committing; the generator caps in-flight load at 8 s of offered rate | expected past the knee in a sweep; at low rates, a broken deploy |
| every transaction `committed invalid` | platform verdict: wrong signing key, namespace missing, or chaincode mismatch | fabricx: rerun `up.sh` (it verifies the namespace policy holds the exported key) |
| `WARNING ... send-gap p99` in `summary.txt` | the load generator, not the platform, was the bottleneck | more `load_gen_cpus` in the profile, or a lower rate; reject the run |
| `invariant_ok=false` | a submitted transaction never reached a terminal state | reject the run; check finality detection and `finality_wait` |
| `state-db parity not held` caveat | Drunix (YugabyteDB) and Fabric-X (PostgreSQL) cannot run LevelDB | expected and disclosed; see [architecture/normalization-status.md](architecture/normalization-status.md) |
| `native metrics scrape ... failed` caveat | the platform's `/metrics` output did not parse | informational only; harness numbers unaffected |
| preflight exits 1 | host lacks tooling or the profile's CPU/RAM | install with `scripts/install-deps.sh`, or use the smaller profile preflight names |
| preflight exits 2 | runnable, with warnings (free RAM, swap, disk, no passwordless sudo) | fix what you can; numbers from a host under memory pressure measure swap |

### Platform-specific

- **fabricx.** `up.sh` builds Arma and the committer from source (several minutes on first run). Namespace registration is checked against the state DB, not loadgen's exit code: upstream loadgen exits 1 even on success. `connection.env` must list all four routers; with one router, every transaction waits ~10 s for Arma to forward it to the primary batcher. Details: [platforms/fabricx-integration.md](platforms/fabricx-integration.md).
- **drunix.** A Committing Peer has exited under overload in the past: [REMAINING-WORK.md §3](REMAINING-WORK.md#3-drunix-committing-peer-exit-under-overload). Write values are JSON-wrapped: [platforms/drunix.md](platforms/drunix.md).
- **neuchain.** Needs its images built first (45–90 min): [neuchain/build-and-portability-guide.md](neuchain/build-and-portability-guide.md).
- **GCP.** IAM, org policy and leftover-VM cleanup: [guides/gcp-deployment.md](guides/gcp-deployment.md).
