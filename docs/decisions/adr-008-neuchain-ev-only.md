# ADR-008: NeuChain `ev` branch only

## Context

`github.com/iDC-NEU/NeuChain` has branches `ev`, `eov`, `oe`, `oepv`. `ev` is the
Execute-Validate architecture described in the VLDB 2022 paper. The others are
architectural variants (execute-order-validate, order-execute, etc.). A sharded
successor NeuChain+ (2024) also exists.

## Decision

Integrate the **`ev` branch only**. `eov` / `oe` / `oepv` and NeuChain+ are out
of scope.

## Rationale

- `ev` is the paper's contribution and the reason NeuChain is in this comparison
  (ordering-free deterministic EV).
- NeuChain is already the hardest platform to integrate (C++, custom client,
  Ubuntu 20.04 build). Four branches is 4× the integration cost for diminishing
  insight.
- The other branches would mostly re-measure things the Fabric variants already
  cover (EOV, OE).

## Alternatives considered

- **All four branches** — 4× effort on the riskiest platform; rejected unless a
  specific research question demands the intra-NeuChain comparison.
- **NeuChain+ instead of NeuChain** — different (sharded) architecture; would
  change what "NeuChain" means in the comparison and adds sharding as a variable.

## Consequences

- `deploy/docker/neuchain/up.sh` pins `--branch ev`.
- `neuchain-client-implementation.md` §7 records how our `local` setup differs
  from the paper's so numbers are not compared naively.
- Revisit only if the intra-NeuChain branch comparison becomes a goal.
