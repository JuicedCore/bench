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

## Adapter (Phase 4) — spike complete, pure-Go

The proto spike ([adr-002](../decisions/adr-002-neuchain-client-spike.md)) is
done. Full findings + wire formats:
[neuchain-client-implementation.md](neuchain-client-implementation.md).

The client path is **ZeroMQ + protobuf + RSA-1024/SHA-256**, not gRPC/brpc:

- **submit** → ZMQ PUB to `<block-server>:5001`, message
  `comm.UserRequest{payload = marshal(TransactionPayload{header=funcName,
  payload=marshal(YCSB_PAYLOAD{table,reads,update}), nonce}),
  digest = RSA_sign_PKCS1v15_SHA256(payload)}`. The signature is the tx id.
- **finality** → ZMQ REQ to `<block-server>:7003`:
  `UserQueryRequest{type:"tip_query"}` for height, `{type:"block_query",
  payload:str(n)}` for block `n`; each block entry is a hand-rolled varint frame
  `tid,epoch,digestLen,digest,result` (`result` 0=COMMIT, 2/3=ABORT).

`pkg/adapters/neuchain` therefore needs: generated protobuf stubs
(`scripts/neuchain-proto-spike.sh`), `github.com/go-zeromq/zmq4` (pure Go),
`crypto/rsa`, `encoding/binary`. No cgo, no native `user` binary.

## Local caveat

NeuChain is built for high core counts. On `local` (4 block servers + 1 epoch
server in ≈6 cores) it runs well below its paper numbers. Every `local` NeuChain
run carries an automatic manifest caveat; quotable numbers need `gcp-full`.

## Deploy

```
bash deploy/docker/neuchain/up.sh local-small
set -a; source deploy/docker/neuchain/connection.env; set +a
go test -tags integration -run Integration -v ./pkg/adapters/neuchain/
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform neuchain --profile local-small
bash deploy/docker/neuchain/down.sh local-small
```

- **Image**: `bench/neuchain:ev`. Currently a **patched** build
  ([patched/README.md](../../deploy/docker/neuchain/patched/README.md)); runs
  record `platform_version: neuchain-ev-patched` and are provisional. The clean
  upstream build (`deploy/docker/neuchain/build.sh`) is still pending.
- **Topology**: 4 containers on a fixed subnet (`172.30.7.10-13`), each running a
  block server and an epoch server (4-node raft groups for both), as NeuChain's own
  run scripts do. Configs in `deploy/docker/neuchain/conf/`, `cc_type: ycsb`.
- **State**: starts empty, like every other platform (no `db_init` preload);
  `down.sh` removes the containers' volumes.
- **Keys**: `up.sh` runs `crypto-init` once (NeuChain's `user -b 1 1 1` with
  `init_crypto: true`) into `.cache/crypto/`, then copies server 0's user keypair
  to servers 1-3 so one adapter key verifies everywhere.
- **Ports**: submit `5001/5011/5021/5031`, query `7003/7013/7023/7033`.

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
