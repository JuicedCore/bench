# ADR-007: Drunix participates in CFT comparisons only

## Context

Drunix is a fork of Hyperledger Fabric 2.5.x. Its consensus is inherited Raft
(crash fault tolerant). It has no Byzantine ordering path.

## Decision

- Drunix is compared only against **`fabric-cft`** (Raft vs Raft, plus Drunix's
  LP/CP split and Validation Service).
- The stronger-ordering comparison group is **Fabric SmartBFT vs Fabric-X Arma vs
  NeuChain deterministic**; Drunix is not in it.
- NeuChain sits in that group as "stronger than Raft / deterministic", **not**
  labelled BFT — it is crash-tolerant deterministic, not Byzantine.

## Rationale

- Putting a CFT system in a BFT comparison is a category error; the fault models
  differ and so does the cost.
- Drunix's contribution is the disaggregated peer + SQL state, which is a
  meaningful comparison against stock Raft Fabric.

## Alternatives considered

- **Force a BFT orderer under Drunix** — not supported by the fork; would be a
  bespoke integration that no longer represents Drunix.
- **Drop Drunix from consensus comparisons entirely** — loses the LP/CP insight.

## Consequences

- Report groups: `{fabric-cft, drunix}` and
  `{fabric-bft, fabricx, neuchain}` (the latter with NeuChain footnoted as
  non-Byzantine).
- Confirmed by the original research question resolution (Q1).
