# What is and is not normalized today

[fairness-guarantees.md](fairness-guarantees.md) states the contract. This page
states how far the code actually meets it right now, lever by lever, so nobody
quotes a number further than it can carry. It was written against the repo on
2026-09-13; each row names the file that makes it true, so it can be re-checked.

Three layers:

1. **Identical** — the same value, from the same file, on every platform.
2. **Same logical operation, native form** — each platform does the equivalent
   thing its own way.
3. **Requested but not equal** — known gaps. Runs go ahead anyway; the numbers
   carry the limitation.

## 1. Identical on every platform

One file per mode in `configs/normalized/` serves all five platforms (`--platform`
selects the adapter), so these values are literally the same bytes for everyone.
`pkg/harness/configparity_test.go` fails if that ever stops being true.

| Lever | Value | Where it comes from |
| ----- | ----- | ------------------- |
| Workload generator | same Go code; same key space and distribution per mode; seed 1; 64-byte values | `pkg/workloads`, `configs/normalized/*.yaml` |
| Load shape | same mode (open/closed loop), same rates | `configs/normalized/*.yaml` |
| Sweep ladder | `[100, 250, 500, 1000, 2000, 3500, 5000, 7500, 10000]` TPS for everyone | `configs/normalized/probe-sweep.yaml` |
| When a step "holds" | failure ≤ 2%, confirmed ≥ 95% of offered, send-gap p99 ≤ 50 ms; stop after 2 steps that do not hold | `stepVerdict` in `pkg/harness/engine.go` |
| Measurement windows | 30 s warmup / 15 s cooldown; **quick-smoke is 5 s / 3 s** (a 30 s phase cannot carry a 30 s warmup) | `configs/normalized/*.yaml` |
| Finality timeout | 60 s | `configs/normalized/*.yaml` |
| Load generators | same count for every platform in a normalized run (generator *i* uses seed + *i*, so a different count would mean a different key sequence) | `generatorCount` in `pkg/harness/engine.go` |
| Hardware | the profile's **total** budget (local-small: 8 CPU / 8 GB), split evenly across each platform's real containers, no swap | `apply_budget` in `deploy/docker/lib.sh`; recorded in the manifest |
| Commit timestamp (T3) | when the block carrying the transaction is observed, never when a caller happens to ask | block-event listeners in each adapter |
| Load generator scheduling | submission never waits on finality observation | `pkg/loadgen/generator.go` |

## 2. Same logical operation, native form

There is no contract all five platforms can run, so each normalized workload is
mapped onto the platform's own primitive.

| Workload | fabric-cft / fabric-bft | Drunix | Fabric-X | NeuChain |
| -------- | ----------------------- | ------ | -------- | -------- |
| `kv-write` | chaincode `Put` | chaincode `Put`, value JSON-wrapped | blind write in namespace `0` | YCSB update |
| `transfer` | chaincode `Transfer`: read-modify-write on two accounts, balance computed | same as Fabric | two-key read/write set carrying the workload's values (no chaincode, no computed balance) | two-key read + update set |
| `kv-read` | **evaluate on one peer** — no ordering, no commit | same as Fabric | full transaction through ordering and commit, plus a dummy write (read-only txs are rejected) | full transaction with a read set, through commit |

Adapters: `pkg/adapters/fabric`, `pkg/adapters/drunix`, `pkg/adapters/fabricx`,
`pkg/adapters/neuchain`.

## 3. Requested but not equal

| Gap | What differs | Effect on the numbers | How it is surfaced |
| --- | ------------ | --------------------- | ------------------ |
| **Reads** | Fabric and Drunix answer a read from one peer without ordering or committing it; Fabric-X and NeuChain order and commit every read. | Fabric-family read throughput and latency are **not comparable** with Fabric-X or NeuChain, and favour Fabric by construction. This affects `read-profile` and also `multi-client`, which is 50% reads. | **Not caveated automatically.** Do not quote those two modes across families. |
| **State DB** | LevelDB is requested for every normalized run. fabric-cft, fabric-bft and NeuChain use it. Drunix runs YugabyteDB (its shipped network cannot run LevelDB). Fabric-X runs PostgreSQL, its only state store. | Different storage engines under the same workload; write-heavy numbers are the most exposed. | Automatic: manifest `state_db` vs `state_db_requested`, plus a caveat on the run and in the report. |
| **Block cutting** | The Fabric family applies the full shared setting: 100 messages, 1 s, 2 MB preferred / 10 MB absolute. Fabric-X applies the message count and timeout but **not the byte limits**. NeuChain applies **nothing** — its batching is not wired to the profile, and its deploy is still placeholder. | Block formation, and so throughput and latency, is only truly matched within the Fabric family. | **Not caveated automatically.** Treat cross-family throughput as carrying this. |
| **Cryptography** | Fabric and Drunix verify an ECDSA-P256 endorsement on every transaction. Fabric-X verifies a client-signed ECDSA-P256 endorsement, with no peer endorsement round-trip. NeuChain signs with RSA-1024 and has no endorsement phase. | Per-transaction verification cost differs by architecture. This is disclosed, not equalized, by design. | Manifest `crypto`; footnoted under every table in the report. |
| **Submit latency** | Fabric family: T2 is the gateway's return after the orderer accepted the transaction. Fabric-X: T2 is the Arma router's reply to that envelope (it accepted it and forwarded it to a batcher, strictly before ordering and commit — the same point the Fabric gateway's Submit returns). NeuChain: ZeroMQ publish is fire-and-forget, so T2 is only a local call return. | Submit and commit latency are meaningful for the Fabric family and Fabric-X; for NeuChain compare end-to-end latency only. | Documented in fairness-guarantees.md; not caveated per run. |
| **Payload size** | Drunix JSON-wraps every write value client-side to survive a YugabyteDB statedb bug. | Drunix's on-wire payload is slightly larger than 64 bytes. | Automatic caveat on Drunix runs. |

## What can be quoted today

| Mode | Within the Fabric family | Across families (vs Fabric-X, NeuChain) |
| ---- | ------------------------ | --------------------------------------- |
| `quick-smoke`, `probe-sweep`, `throughput-scan`, `latency-profile` (kv-write) | yes | yes, with the state-DB and block-cutting caveats |
| `contention` (transfer) | yes | yes, with those caveats — and Fabric-X writes values rather than computing balances |
| `read-profile` (kv-read) | yes | **no** |
| `multi-client` (kv-mixed, 50% reads) | yes | **no** |

Always subject to the per-run rejection rules in fairness-guarantees.md: the
comparison report excludes runs that fail them and says why.

## Not yet verified live

As of this writing no run has exercised any of the following on a real network:
the measurement fixes (saturation rule, generator back-pressure, Fabric T3 from
block events), the resource-budget enforcement, the Fabric-X gRPC rebuild, or a
working NeuChain deployment. Every result recorded before 2026-09-13 predates
them and the report rejects it.
