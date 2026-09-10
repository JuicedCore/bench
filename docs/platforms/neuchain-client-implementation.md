# NeuChain client implementation (F1)

> Status: **PENDING — Phase 4.** This file is the mandatory deliverable of the
> proto spike ([adr-002](../decisions/adr-002-neuchain-client-spike.md)). It is
> filled in as the adapter is built. The structure below is the template the
> implementer must complete; do not delete headings, mark them "N/A" if they do
> not apply.

## 1. Decision outcome

- [ ] **Pure-Go adapter** — Go builds NeuChain transactions and signs them.
- [ ] **Native `user`-binary wrapper** — load is driven through NeuChain's own
      client; Go only orchestrates and times.

Decision date: ____   Decided by: ____   Spike duration: ____

Reason (2–4 sentences): _____

## 2. Proto files

| File | Copied from (repo path) | Upstream commit | Notes |
| ---- | ----------------------- | --------------- | ----- |
| `pkg/adapters/neuchain/proto/…` | `iDC-NEU/NeuChain@ev:…` | `<sha>` | |

`pkg/adapters/neuchain/proto/ORIGIN` records the clone URL, branch, commit SHA
and the date pulled. Regeneration command:

```
# protoc ... --go_out ... --go-grpc_out ...   (exact invocation here)
```

## 3. What was reimplemented in Go (pure-Go path only)

| Concern | Go file / func | Mirrors NeuChain C++ source | Deviations |
| ------- | -------------- | --------------------------- | ---------- |
| Transaction struct + wire format | `pkg/adapters/neuchain/txbuild.go` | `src/…` | |
| Signing (alg, digest, encoding) | `pkg/adapters/neuchain/sign.go` | `src/…` | |
| Epoch / batch assignment | `…` | `src/…` | |
| Read/write set encoding | `…` | `src/…` | |
| Submit RPC + response parse | `pkg/adapters/neuchain/grpc_client.go` | `src/…` | |
| Finality detection | `…` (poll height / stream) | `src/…` | |

For each row, link the exact C++ file + line range reviewed so a later reader can
re-verify the port.

## 4. What was NOT reimplemented / assumed

List every simplification (e.g. "single-org, no client-side crypto config
negotiation", "fixed epoch size", "no retry on NACK"). Each becomes a manifest
caveat for NeuChain runs.

## 5. CryptoInfo reported

```
signature_alg:            ____
hash_alg:                 ____
per_tx_endorsement_verify: false
msp_note:                 "NeuChain EV: deterministic execution, no per-tx endorsement signatures"
```

## 6. Validation

- [ ] 10-tx integration test passes: every submitted tx has a finality result,
      T1 ≤ T2 ≤ T3.
- [ ] Harness-reported TPS cross-checked against NeuChain's own block height /
      logs over a run window (within tolerance: ____).
- [ ] A run of the native `user` binary at the same offered rate produces
      throughput within ____ % of the adapter (sanity that the port is faithful).

## 7. Known divergences from the VLDB paper setup

Anything about our deployment (node count, hardware, batch size, network) that
differs from the paper, so readers do not compare our `local` numbers to the
paper's directly.
