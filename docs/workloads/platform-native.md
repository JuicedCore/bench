# Platform-native workloads

> **Status:** runnable. Per-platform configs live in `configs/native/` (kv-write
> ceiling) and `configs/native-kv-mixed/` (same levers, 50% reads). Deploy
> honours `normalized: false` (`BENCH_NORMALIZED=false`): native orderer batch,
> native state DB, per-platform generator count. Token SDK on Fabric-X is still
> not implemented; those native files use the gRPC path instead.

Each platform also runs the workload it is *built for*, tuned to its best
configuration (`normalized: false`). These numbers show ceiling behaviour. They
are **never** placed in the same comparison table as normalized numbers, and
native runs may use a non-LevelDB state backend and per-platform-tuned orderer
batch params — all recorded in the manifest
([adr-013](../decisions/adr-013-config-parity-policy.md)).

| Platform | Native workload | Why it is the best case |
| -------- | --------------- | ----------------------- |
| Fabric CFT | `kv-write` on LevelDB, single-org endorsement policy, batch tuned for throughput | minimal endorsement + validation overhead |
| Fabric BFT | same, SmartBFT tuned (batch size / timeout) | isolates BFT ordering cost |
| Drunix | `kv-write` on YugabyteDB, LP/CP scaled out, Validation Service replicas | same Put; native levers are SQL state + disaggregated peer |
| Fabric-X | Token SDK `Issue` + `Transfer` + `Redeem` (UTXO), Arma sharded — would need a token client on the native gRPC path ([adr-016](../decisions/adr-016-fabricx-native-grpc.md)); the old REST route was removed | the workload the 200k-TPS benchmark used |
| NeuChain | native KV + transfer at high concurrency, epoch/batch tuned per paper | deterministic-execution pipeline at full width |

Native probe-sweep in `configs/native/` is **write-only** on purpose. `kv-mixed`
mixes in reads, and on Fabric / Drunix / Fabric-X a read is Evaluate /
QueryService — it never reaches the orderer or the commit path. The headline
would blend two machines. That mix still has a native preset:
`configs/native-kv-mixed/` (same platform levers, `read_write_ratio: 0.5`).
Treat it as a separate campaign. Closed-loop mixed capacity is
`configs/normalized/multi-client.yaml`. `transfer` belongs to `contention.yaml`
(RMW / MVCC).

## Tuning record

Every native run's manifest captures:

- `state_db` actually used
- `orderer_batch` actually used (Fabric family)
- resource limits per container
- any platform-specific knobs (in `manifest.caveats` as free text, e.g.
  "Fabric-X: 4 batchers, digest batch 500", "NeuChain: epoch 2000 tx")

## Reporting

Native results get their own section in the generated report, labelled
`normalized=false`, with the tuning summary inline. The intent is "here is what
each platform can do when you play to its strengths", separate from "here is how
they compare on identical work".
