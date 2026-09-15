# Run-config catalog: what each configs/*.yaml does + exact commands per platform

## Context

`configs/**/*.yaml` are the run definitions `benchrunner run --config <file> --platform <p>`
consumes. Each file fixes a workload + load pattern; `--platform` picks which network it
runs against. Pairs with `COMMANDS.md` (which covers bringing each platform's network
up/down); this file assumes the network for whichever platform you're targeting is
already up and its `connection.env` sourced.

The directory split is the whole point:

- **`configs/normalized/`** — the cross-platform comparison set. **One file per
  mode, serving all five platforms.** The `load:` and `metrics:` blocks are
  literally the same bytes whichever `--platform` you pass, so the fairness
  levers cannot drift apart (adr-013). Platform differences live only in the
  union `adapter:` block, whose irrelevant keys each adapter ignores.
- **`configs/native/`** — reserved for per-platform tuned runs (`normalized:
  false`), which would never be mixed with normalized numbers. **It does not exist
  today**; see "Native set" below.

## Primer: workloads, load modes, `normalized`

**Workload** (`workload:` field, from `pkg/workloads/workload.go`):
- `kv-write` — every tx is a `Put(key, value)`.
- `kv-read` — every tx is a `Get(key)`.
- `kv-mixed` — `Get`/`Put` mixed per `load.read_write_ratio`.
- `transfer` — `Transfer(from, to, amount)` between two keys in the key space (read-modify-write on two accounts; this is what makes MVCC conflicts visible).

Same workload code runs on every platform, deterministic given `seed`, so the
key-access and contention patterns are identical everywhere. Each adapter maps the
logical operation onto its platform's own primitive (chaincode on the Fabric
family, namespace read/write sets on Fabric-X, YCSB payloads on NeuChain); the
mapping and its caveats are in
[docs/architecture/normalization-status.md](docs/architecture/normalization-status.md).

**Load mode** (`load.mode`):
- `open-loop` — fire at a target/ramped TPS regardless of how fast responses come back (`target_tps`, or `ramp_from`/`ramp_to`/`ramp_duration`).
- `closed-loop` — a fixed pool of `workers` each loop submit → wait-for-finality → repeat; measures capacity at fixed concurrency instead of a fixed offered rate.
- `sweep` (under `load.sweep`) — probe-and-sweep methodology: a low-rate probe, then step through `steps` (TPS levels) to find the saturation knee, then hold at `hold_fraction` of it. See `docs/architecture/metrics-methodology.md`.

**`normalized: true/false`** — `true` selects the cross-platform-comparable config
(pins state DB to LevelDB where a platform would otherwise default elsewhere, etc. —
see `docs/decisions/adr-012-state-db-leveldb.md`); `false` runs a platform in its own
native mode (e.g. Fabric-X's real token-transfer path), not meant to be compared
head-to-head with the normalized bucket.

## Compatibility matrix

### Normalized set — `configs/normalized/` (the comparison)

> What these configs actually hold equal today — and the known gaps (reads, state DB,
> block cutting, crypto, NeuChain submit latency) that limit which results can be quoted
> across platform families — is in [docs/architecture/normalization-status.md](docs/architecture/normalization-status.md).

One file per mode. Every mode is *intended* to run on every platform (adr-010:
run the workload everywhere and caveat it, never leave a blank cell). What
differs is whether the platform can currently be brought up at all.

| Mode | Workload | fabric-cft | fabric-bft | drunix | fabricx | neuchain |
| --- | --- | --- | --- | --- | --- | --- |
| `quick-smoke.yaml` | kv-write | yes | yes | yes¹ | yes² | blocked³ |
| `probe-sweep.yaml` | kv-write | yes | yes | yes¹ | yes² | blocked³ |
| `throughput-scan.yaml` | kv-write | yes | yes | yes¹ | yes² | blocked³ |
| `latency-profile.yaml` | kv-write | yes | yes | yes¹ | yes² | blocked³ |
| `read-profile.yaml` | kv-read | yes | yes | yes¹ | yes² | blocked³ |
| `contention.yaml` | transfer | yes | yes | yes¹ | yes²⁴ | blocked³ |
| `multi-client.yaml` | kv-mixed | yes | yes | yes¹ | yes² | blocked³ |

¹ Runs, with two automatic manifest caveats — see below.

² **Fabric-X runs on its native gRPC path** and is live-verified (2026-09-14,
local-small, smoke + probe-sweep). The adapter broadcasts each envelope to all
four Arma routers and reads finality from the sidecar's deliver stream, so it
reports a real submit latency. `state_db` is PostgreSQL, the platform's only
store, so every normalized run carries the state-DB caveat. See
[`docs/platforms/fabricx-integration.md`](docs/platforms/fabricx-integration.md)
and [adr-016](docs/decisions/adr-016-fabricx-native-grpc.md).

³ **NeuChain has no server binaries yet** — a 45–90 min C++ build, see
[`docs/neuchain/build-and-portability-guide.md`](docs/neuchain/build-and-portability-guide.md).
The Go adapter and all seven configs are ready; nothing else is missing.

⁴ On Fabric-X, `transfer` is a two-key read/write set carrying the workload's
values: contention (two hot accounts per transaction) is exercised, but no balance
is computed as the Fabric chaincode does. See
[docs/workloads/mismatches.md](docs/workloads/mismatches.md).

The union `adapter:` block is what lets one file serve every platform: each
adapter's `configFromExtra` is a key lookup that ignores keys it does not know,
and an undefined `${VAR}` expands to the empty string. `pkg/harness/configparity_test.go`
asserts the levers stay identical and that every platform's keys are present.

### Native set

There is currently no `configs/native/`. The six Fabric-X files that lived there
targeted the REST token API, which the platform rebuild removed. Fabric-X now
runs the same normalized modes as everything else.

## `quick-smoke.yaml` — 30s low-rate sanity check

Proves the network, adapter, load generator, and metrics pipeline all work end to end
before committing to a longer run. 50 TPS open-loop, 30s hold, 64-byte values, 5000-key
uniform space.

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform neuchain
```

## `probe-sweep.yaml` — primary methodology: find the saturation knee, then hold

Probes at 10 TPS, steps through `[100, 250, 500, 1000, 2000, 3500, 5000, 7500, 10000]`
TPS (60s/step) to find where the platform saturates (fail rate crosses
`max_fail_rate: 0.02`), then holds 5 minutes at 90% of that knee — **the knee it
actually measured**, not 90% of the top step. After 2 consecutive failed steps the
rest of the ladder is skipped and recorded in `manifest.skipped_steps`, which is
what lets every platform share one ladder without a slow one burning the whole run.
Set `abort_after_failed_steps: 0` to force the full ladder. This is the
methodology `docs/architecture/metrics-methodology.md` recommends running first — its
output tells you what `target_tps` to set in `latency-profile.yaml`.

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform neuchain
```

## `throughput-scan.yaml` — fast, crude saturation-band finder

Ramps offered load 100 → 10,000 TPS over 5 minutes, then holds 2 minutes. Cruder than
`probe-sweep.yaml` (no adaptive stepping/fail-rate gate) but faster to run when you just
want a rough sense of where a platform falls over.

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/throughput-scan.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/throughput-scan.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/throughput-scan.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/throughput-scan.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/throughput-scan.yaml --platform neuchain
```

## `latency-profile.yaml` — clean latency percentiles below saturation

Fixed 1000 TPS (edit to ~60% of the knee `probe-sweep.yaml` found for your platform),
10-minute hold, 500,000-key space. Meant to produce clean p50/p95/p99/p99.9
end-to-end latency numbers well clear of saturation effects — run this *after*
`probe-sweep.yaml`, not instead of it.

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/latency-profile.yaml --platform neuchain
```

## `contention.yaml` — MVCC-conflict / hot-key stress test

`transfer` workload (read-modify-write on two accounts) over a small 2000-key space
with a Zipfian distribution (`zipfian_constant: 1.2` — high skew, hot keys collide
often), 800 TPS, 5-minute hold. Expect a non-zero failure rate on Fabric-family
platforms as concurrent transfers on the same hot account conflict at commit time —
that failure rate *is* the measurement, not a bug.

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/contention.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/contention.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/contention.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/contention.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/contention.yaml --platform neuchain
```

## `multi-client.yaml` — fixed-concurrency capacity (closed-loop)

256 workers, each looping submit → wait-for-finality → repeat, `kv-mixed` workload
(50% reads), 5-minute hold. Measures capacity at a fixed number of concurrent clients
rather than a fixed offered rate — a different lens than the open-loop configs above.
Note: this is a single-process stand-in for true multi-client load (separate generator
processes/VMs is a later phase, not yet built).

```bash
# fabric-cft
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/multi-client.yaml --platform fabric-cft

# fabric-bft
set -a; source deploy/docker/fabric-bft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/multi-client.yaml --platform fabric-bft

# drunix
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/multi-client.yaml --platform drunix

# fabricx (see footnote 2)
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/multi-client.yaml --platform fabricx

# neuchain (needs its image built - see footnote 3)
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/multi-client.yaml --platform neuchain
```

## Fabric-X — bringing it up

```bash
bash deploy/docker/fabricx/up.sh local-small     # clones pinned sources, builds, starts, bootstraps the namespace
set -a; source deploy/docker/fabricx/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabricx --profile local-small
bash deploy/docker/fabricx/down.sh local-small
```

Four containers from one image: Arma (4 parties, 16 processes), PostgreSQL, the
sidecar/verifier/coordinator pipeline, and the validator-committer. Ports
6022/6122/6222/6322 (the four routers; the adapter broadcasts to all of them), 6023
(assembler), 4001 (deliver) and 9643 (metrics) — none of which collide with the
Fabric family, NeuChain, or the monitoring stack.

The first `up.sh` compiles Arma and the committer from source: several minutes and
several GB. Later deploys reuse the Docker build cache.

## `read-profile.yaml` — the kv-read leg of the normalized set

adr-009 names four normalized workloads; `kv-read` is the one that previously had
no config on any platform. 1000 TPS open-loop, 5 min, over the same 500k key space
as `latency-profile.yaml` so a populated ledger can be read back — run it after a
write mode or the keys will be absent.

**A read is not a commit, on any platform**, so these numbers are not comparable
to the write modes:

- **fabric / drunix** — chaincode `Evaluate` against one peer. `WaitForFinality`
  returns immediately with `Valid: true`; nothing reaches the ledger.
- **fabricx** — a full ordered + committed transaction carrying a unique dummy
  blind write (the validator rejects read-only transactions).
- **neuchain** — a real submitted transaction carrying a read set, not a point
  query; it goes through the full commit path.

The shape is consistent enough to compare read paths against each other. It is
not a like-for-like latency against `kv-write`. See `docs/workloads/mismatches.md`.

**Run it on a populated ledger.** `scripts/run-all.sh` redeploys the platform for
every config, so a campaign that includes `read-profile.yaml` reads an empty
ledger: on the Fabric family an absent key returns an empty value and counts as a
successful read. For a meaningful read run, deploy once, run `latency-profile.yaml`
(same 500k key space), then run `read-profile.yaml` against the same deployment.

```bash
set -a; source deploy/docker/<platform>/connection.env; set +a
./bin/benchrunner run --config configs/normalized/read-profile.yaml --platform <platform>
```
