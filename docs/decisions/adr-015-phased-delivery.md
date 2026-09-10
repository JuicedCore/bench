# ADR-015: Phased delivery, shippable after each platform

## Context

Four platforms, one a C++ research prototype with a custom client. A single
"big bang" delivery risks the hardest platform (NeuChain) blocking everything.

## Decision

Deliver in phases; each phase is an independently usable deliverable —
adapter(s) + Compose deploy + smoke config + platform doc + relevant ADRs:

1. **Core harness + Fabric CFT + Drunix** — interface, load gen (open + closed
   loop), metrics pipeline, monitoring stack, kvstore chaincode, manifest,
   config-parity plumbing.
2. **Fabric BFT** — v3.1.x SmartBFT, 4 orderers.
3. **Fabric-X** — Arma + committer stack, custom FSC KV view, Token SDK native,
   commit-event finality. Ships with the local resource-starvation caveat doc.
4. **NeuChain** — proto spike first (records the [adr-002](adr-002-neuchain-client-spike.md)
   outcome), then adapter, Dockerised build, 5-node deploy, finality detection.
5. **Full suite + GCP** — all workloads × all platforms, multi-client, Zipfian
   contention, Terraform GCP, full GCP run, HTML report + comparison charts.

## Rationale

- Value lands early: after Phase 1 there is a working Fabric-vs-Drunix benchmark.
- A NeuChain overrun delays only NeuChain, not the Fabric-family results.
- Each phase's scope is small enough to review and validate on its own.

## Alternatives considered

- **All-at-once per the original 5-phase plan with the full suite only at the
  end** — highest risk of a late, total slip.

## Consequences

- `cmd/benchrunner/main.go` registers adapters as their phases land; Phase 3/4
  deploy scripts currently exit with a "not implemented" notice.
- The comparison report is meaningful with a subset of platforms and grows as
  phases complete.
