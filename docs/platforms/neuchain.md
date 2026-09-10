# NeuChain

## What it is

A fast permissioned blockchain from Northeastern University (iDC-NEU), published
at **VLDB 2022** ("NeuChain: a fast permissioned blockchain system with
deterministic ordering", PVLDB Vol. 15). Written in **C++**, gRPC transport.

Repo: `github.com/iDC-NEU/NeuChain`. Branch **`ev`** only
([adr-008](../decisions/adr-008-neuchain-ev-only.md)); other branches
(`eov`, `oe`, `oepv`) are architectural variants and out of scope. A sharded
successor, **NeuChain+** (MDPI 2024), also exists and is out of scope.

## Architecture — ordering-free Execute-Validate

No dedicated ordering service. Ordering is made **implicit** through
**deterministic execution**: every server executes the same transactions in the
same logical order without a consensus round per block. Key techniques:
asynchronous block generation, pipelining, an epoch server for coordination, and
block servers for execution/validation.

Implication for fairness: NeuChain's EV path has **no per-transaction endorsement
signature verification** — the EOV platforms do. This is inherent and is
**disclosed** in the manifest (`per_tx_endorsement_verify: false`), never
"equalised" (see [fairness-guarantees.md](../architecture/fairness-guarantees.md)).

## Build environment

Per the repo build guide: **Ubuntu 20.04**, **cmake 3.16.3**, **gcc 9.4.0**;
`CMakeLists.txt` may need edits. This is isolated in a Docker build container
(Phase 4) producing `block_server*`, `epoch_server`, `user`.

## Adapter (Phase 4)

`pkg/adapters/neuchain` — pure Go, gRPC. Client strategy is decided by a
**time-boxed proto spike** first
([adr-002](../decisions/adr-002-neuchain-client-spike.md)):

1. Copy `.proto` files (with an `ORIGIN` file naming the upstream commit) into
   `pkg/adapters/neuchain/proto/`; generate Go stubs.
2. Submit one transaction end to end.
3. If transaction format + signing + epoch/batch semantics reimplement cleanly
   in Go → **pure-Go adapter** (`txbuild.go`, `sign.go`).
4. If not → **wrap the native `user` binary** (exec / thin control socket) which
   emits T1/T2/T3 — same principle as using each platform's own SDK elsewhere.

Either outcome is written up in full in
[neuchain-client-implementation.md](neuchain-client-implementation.md): what was
reimplemented, which Go file holds each piece, which NeuChain C++ source it
mirrors, and any deviations.

Finality (T3): block-height poll or a gRPC stream of committed blocks — whichever
the `ev` branch exposes.

## Local caveat

NeuChain is built for high core counts. On `local` (4 block servers + 1 epoch
server in ≈6 cores) it runs well below its paper numbers. Every `local` NeuChain
run carries an automatic manifest caveat; quotable numbers need `gcp-full`.

## Deploy (Phase 4 — partial)

Scaffolding is in place; the adapter and the exact run flags are still TODO.

- `deploy/docker/neuchain/Dockerfile.build` — Ubuntu 20.04 + cmake 3.16 + gcc 9.4
  + gRPC from source; clones `iDC-NEU/NeuChain@ev`; applies an optional
  `cmakelists.patch`; builds `block_server*` / `epoch_server` / `user`; stages
  the `.proto` files and the commit SHA.
- `deploy/docker/neuchain/Dockerfile.run` — slim runtime image with just the
  binaries + shared libs.
- `deploy/docker/neuchain/docker-compose.yml` — 4 block servers + 1 epoch
  server, resource limits from the profile. **Ports / config paths / CLI flags
  are placeholders** — fill them from the repo's own run scripts.
- `deploy/docker/neuchain/up.sh` — builds both images if absent, brings the
  topology up, emits `connection.env`.

### The proto spike (do this first — adr-002)

```
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
scripts/neuchain-proto-spike.sh
```

Clones NeuChain, records the exact commit in
`pkg/adapters/neuchain/proto/ORIGIN`, copies every `.proto`, generates Go stubs,
and prints the five questions the spike must answer (submit RPC, tx construction,
signing, epoch/batch assignment, finality detection). Record the answers and the
pure-Go-vs-native-wrapper decision in
[neuchain-client-implementation.md](neuchain-client-implementation.md).
