# Fabric-X integration: how it actually works

Written from primary sources (upstream v1.0.5/v1.0.6 checkouts, and a working
reference deployment on this machine) after the first integration attempt failed.
It replaces `fabricx-comparability.md`, whose conclusions were built on the wrong
integration path.

## The short version

Fabric-X has no REST benchmarking interface. Clients speak **gRPC**: submit by
broadcasting an `Envelope` to an **Arma router**, and learn the outcome by reading
blocks from the **sidecar's deliver stream**. Submit and commit are separate
operations on separate connections, so Fabric-X has a genuine submit ack — a real
T2 — which the previous REST-based integration could not observe.

## What the first attempt got wrong

It was built on `fabric-x-samples/tokens`, a Token-SDK/FSC **demo application**
with a REST facade. Consequences:

- Every request went through FSC session setup and ZKP-backed UTXO token logic,
  so the numbers would have measured FSC and zero-knowledge proof generation more
  than Fabric-X. `tokens` ships no metrics endpoint and no load driver.
- The REST call is synchronous to finality, collapsing T2 into T3 and forcing the
  adapter to fake an ack. Submit/commit latency was permanently N/A.
- `kv-write` had no route at all, so a custom FSC view service (`kvview`) was
  invented to add one. It was never finished and answered every request with 501.

## Why namespace bootstrap failed

Namespace creation is a write to the `_meta` namespace, which
`service/verifier/policy/policy.go` maps to the channel's
`/Channel/Application/LifecycleEndorsement` policy — NOT to any namespace policy.
`--policy=...` on `fxconfig namespace create` sets the *new namespace's* policy and
has no bearing on how many signatures the creation transaction itself needs.

For a 4-party Arma deployment, `config/generate/config_block_gen.go` names one
Application org per party (`org1..org4`), and the profile's `LifecycleEndorsement`
is `MAJORITY`, so the creation transaction needs **3 signatures**. Every attempt
submitted one, then blamed the identity and cycled through `org1`, `OrdererOrg1`,
`SampleOrg`. `org1` was correct from the start.

But multi-signing an `fxconfig` transaction is not how the working deployment does
it either. There, the **client creates the namespace as its own first transaction**,
idempotently and non-fatally:

```go
nsTx, err := workload.CreateLoadGenNamespacesTX(policy)
if err := submitLoadGen(b, pend, nsTx, nil); err != nil {
    logger.Infof("namespace tx: %v (ok if namespace already exists)", err)
}
```

That works because its `loadgen.yaml` declares a `_meta` namespace policy listing
all four orderer-org MSP identities. `loadgen/workload/sign.go` turns a list of N
MSP identities into `NOutOf(N, all)`, so the client signs with all four and
satisfies MAJORITY. The first attempt never declared a `_meta` policy at all — it
patched only `sidecar.yaml`.

Two hard prerequisites that fail silently if missed:

- `configtx.yaml` must define `SnapshotEndorsement` and `CheckpointEndorsement`
  alongside `LifecycleEndorsement`. `ValidateConfigTx` rejects the config block
  unless all three resolve; Arma's sample `configtx.yaml` only has the first.
- Arma's generated configs bind their advertised IP. Rewrite `ListenAddress` to
  `0.0.0.0`, or the address has to exist at image-build time.

## The supported client paths

| Path | What it is | Use for benchmarking? |
| ---- | ---------- | --------------------- |
| `loadgen` (`cmd/loadgen`) | upstream's own load generator; six adapters; exports Prometheus metrics | Yes — this is the sanctioned tool, and the reference for how to drive load |
| Direct gRPC: Arma router broadcast + sidecar deliver | what `loadgen`'s `orderer-client` adapter does underneath | Yes — this is what our adapter uses, so we drive OUR workload |
| `armageddon submit/load` | orderer-only load; payload never reaches the committer | No — measures Arma alone, not end-to-end |
| `fabric-x-samples/tokens` REST | FSC/Token-SDK demo app | No — measures FSC + ZKP, not Fabric-X |

## Fairness notes for our runs

- **Block parameters.** The reference tunes Arma to 50 ms / 50-tx blocks. Our
  normalized contract pins the shared anchor (100 messages, 1 s, 2 MB) across every
  platform, so we must apply ours instead — see `deploy/profiles/*.yaml`
  `orderer_batch`. The reference's published numbers are therefore a proven recipe,
  not results we can adopt.
- **Reads need a write.** The validator rejects read-only transactions with
  `MALFORMED_NO_WRITES`. `kv-read` must carry a unique dummy blind-write, which is
  overhead no other platform pays; it belongs in the run caveats.
- **Version skew is the recurring trap here.** Keep orderer, committer, tools and
  loadgen on one coherent set; upstream's own sample stacks pin mismatched versions.

## Known-good reference

A complete working Fabric-X deployment exists at
`~/Projects/NeuChain/harness/fabric-x/` (5 containers from one
image, ~69 recorded runs, saturation knee around 1500 offered TPS on this host).
It is the recipe this integration is rebuilt from. Its workload is Blockbench
SmallBank, not our normalized set, so its numbers are not directly comparable to
ours — the value is the deployment and transport, not the results.
