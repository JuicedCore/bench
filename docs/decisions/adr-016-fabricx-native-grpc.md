# ADR-016: Drive Fabric-X over its native gRPC path

## Context

[adr-003](adr-003-fabricx-fsc-view-and-rest.md) drove Fabric-X through the
`fabric-x-samples/tokens` REST facade. That never produced a benchmark number,
and the approach was wrong on its own terms — see that ADR for the post-mortem.

Fabric-X's supported client path, and the one upstream's own `loadgen` uses, is
gRPC: broadcast a signed envelope to an Arma router, and read outcomes from the
sidecar's deliver stream.

## Decision

The adapter speaks that path directly.

- **Submit** broadcasts to the Arma router and returns on the router's reply to
  that envelope. Replies carry no transaction ID and the router sends them
  asynchronously, possibly out of order, so the adapter keeps a pool of broadcast
  streams and never lets one carry more than one unacknowledged envelope; a stream
  whose reply times out is replaced, never reused.
- **Finality** comes from the sidecar deliver stream, decoding each block's
  `TRANSACTIONS_FILTER` metadata for per-transaction validation codes.
- **Transactions** are `applicationpb.Tx` read/write sets against a single
  application namespace, signed with ECDSA-P256 over the namespace's ASN.1
  marshalling — upstream's own encoding, via its own protobuf module.
- **Namespace bootstrap is a deploy-time step**, not an adapter concern. `up.sh`
  runs `loadgen --only-namespace` inside the Arma container, which signs with all
  four org identities and so satisfies the channel's MAJORITY
  `LifecycleEndorsement` policy.

We depend on `fabric-x-common` (protobuf definitions and `protoutil`) but **not**
on `fabric-x-committer`. Importing the committer's `loadgen/adapters` for its
broadcast helper would pull 66 extra modules including a Docker client and a test
framework; taking only the protos costs 10, and Bench already had gRPC and the
Fabric protos. The wire format still comes from upstream, so it cannot drift.

## Consequences

- **Fabric-X gains a real T2.** Submit and commit are separate operations, so
  submit latency is meaningful for the first time and Fabric-X stops being
  exempt from that column of the comparison.
- **`kv-read` uses QueryService.** A read-only transaction is still illegal
  (`MALFORMED_NO_WRITES`). The adapter does not attach a dummy write; it calls
  `GetRows` on the query process (`:7001`), which is Fabric-X's default point
  lookup. T2 is the RPC return; WaitForFinality is immediate.
- **`transfer` is a two-key read-modify-write** rather than a token transfer.
  There is no chaincode to evaluate a predicate, so the workload's own values are
  written; contention behaviour is comparable, token semantics are not exercised.
- The `kvview` FSC view service is gone. It existed only to paper over the REST
  facade's missing KV route.
- Block-cutting parameters are taken from the profile's shared `orderer_batch`
  anchor, so Fabric-X cuts blocks identically to the Fabric family.

## Alternatives considered

- **Import `fabric-x-committer` and reuse its broadcast/deliver helpers** — less
  code to write, but a 66-module dependency increase for one adapter.
- **Port upstream's SmallBank client** — proven and fastest to a number, but its
  workload is not our normalized set, so it could never enter the head-to-head
  comparison.
