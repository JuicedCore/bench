# NeuChain client implementation (F1)

> Status: **spike complete — decision: PURE-GO adapter.** Source studied:
> `github.com/iDC-NEU/NeuChain` @ branch `ev`, commit
> `5180d5e85ff152eac9c8773378cce84a205080b2`.

## 1. Decision outcome

- [x] **Pure-Go adapter** — Go builds NeuChain transactions, signs them, submits
      over ZeroMQ, and polls blocks for finality.
- [ ] Native `user`-binary wrapper.

**Decided by:** the spike (this document). **Duration:** ~half a day of source
reading, no build required.

**Reason:** the client → block-server path is **ZeroMQ + protobuf + RSA/SHA-256**,
not gRPC and not brpc. (`brpc` in NeuChain is inter-server only —
`chain.proto`'s `ChainService` is block-server ↔ epoch-server.) Every client-side
concern ports to Go with the standard library plus one pure-Go ZeroMQ package:

| Concern | NeuChain C++ | Go equivalent |
| ------- | ------------ | ------------- |
| transport (submit) | `zmq::socket_type::pub` → `<bs>:5001` | `go-zeromq/zmq4` PUB socket (pure Go, no cgo) |
| transport (query) | `zmq` REQ → `<bs>:7003` | `zmq4` REQ socket |
| messages | protobuf (`comm.proto`, `transaction.proto`, `tpc-c.proto`) | `google.golang.org/protobuf` from the same `.proto` |
| signing | OpenSSL `EVP_SignFinal(EVP_sha256())` with an RSA-1024 PKCS#1 key | `rsa.SignPKCS1v15(rand, key, crypto.SHA256, sum)` |
| key files | PEM `RSA PUBLIC KEY` / `RSA PRIVATE KEY` (PKCS#1), 3DES-encrypted priv | `x509.ParsePKCS1PrivateKey` after PEM decrypt; NeuChain uses an **empty password**, so keys generated with `./user -b 1 1 1` are plain PKCS#1 |
| block result parsing | hand-rolled varint framing (see §3.4) | `encoding/binary.Uvarint` |

## 2. Proto files

Pulled by `scripts/neuchain-proto-spike.sh` into `pkg/adapters/neuchain/proto/`:

| File | Upstream path | Used for |
| ---- | ------------- | -------- |
| `transaction.proto` | `include/proto/transaction.proto` | `TransactionPayload{header,payload,nonce,digest}`, `TxValidationCode` |
| `comm.proto` | `include/proto/comm.proto` | `UserRequest{payload,digest}`, `UserQueryRequest{type,payload,digest}`, `QueryResult` |
| `tpc-c.proto` | `include/proto/tpc-c.proto` | `YCSB_PAYLOAD{reads,update,table}` |
| `block.proto` | `include/proto/block.proto` | `Block{header,data,metadata}` (query reply) |
| `kv_rwset.proto` | `include/proto/kv_rwset.proto` | `KVRWSet` (present in full-tx frames, not needed for result frames) |

`proto/ORIGIN` records the exact commit + regen command. `common.proto` and
`chaincode.proto` are vendored Fabric protos and are **not** on the client path;
skip them.

All NeuChain protos lack `option go_package`; the spike script passes
`--go_opt=Mfile.proto=github.com/juicedcore/bench/pkg/adapters/neuchain/proto`
for each.

## 3. Client protocol (verified against source)

### 3.1 Submit (invoke)

`src/user/block_bench/db_user_base.cpp` → `sendInvokeRequest`:

1. `payloadRaw = TransactionPayload{ header = funcName, payload =
   marshal(YCSB_PAYLOAD{ table, reads, update }), nonce = rand64 | (seq<<32) }`
   serialized. `digest` field left empty (server fills).
2. `sig = RSA_sign_PKCS1v15_SHA256(userPrivKey, payloadRaw)` — up to 128 bytes.
3. `UserRequest{ payload = payloadRaw, digest = sig }` serialized.
4. **`UserRequest.digest` (the signature) is the transaction id** the client uses
   for finality matching (`addPendingTransactionHandle(invokeRequest.digest())`).
5. Send the serialized `UserRequest` on a ZeroMQ **PUB** socket to **every** block
   server at `tcp://<ip>:5001` (deterministic execution — all nodes get all txs).
   Single-node mode uses one plain socket to the local block server.

> T2 (ack) for the harness = the moment the ZMQ `send` returns. NeuChain has no
> submit acknowledgement; PUB is fire-and-forget.

### 3.2 Query / finality

ZeroMQ **REQ** socket to `tcp://<localBlockServerIp>:7003`. Request =
serialized `UserQueryRequest{ type, payload, digest = RSA_sign(payload) }`.

| `type` | `payload` | reply |
| ------ | --------- | ----- |
| `"tip_query"` | `""` | ASCII integer = latest committed block height |
| `"block_query"` | `str(blockNumber)` | serialized `Block.Block` |

Finality algorithm (`StatusThread` + `pollTx`): poll `tip_query`; for each new
block `n`, `block_query n`, then for every entry in `block.data.data` decode the
**result frame** (§3.4) and match its `digest` against our pending signatures.

### 3.3 `YCSB_PAYLOAD` mapping for normalized workloads

`tpc-c.proto`: `YCSB_PAYLOAD { repeated bytes reads = 1; repeated bytes update =
2; bytes table = 3; }`. `getYCSBPayload` fills `reads` from `request.reads` and
`update` from `request.writes`; `table` defaults to `"test_table"`.

| Harness `TxKind` | `funcName` (header) | `reads` | `update` |
| ---------------- | ------------------- | ------- | -------- |
| `TxWrite` | from NeuChain cc config (e.g. `"ycsb"`) | — | `[key]` |
| `TxRead` | same | `[key]` | — |
| `TxTransfer` | same | `[src, dst]` | `[src, dst]` |

The concrete `funcName` and table come from the deployed NeuChain chaincode
config (`config-template.yaml` → `func_name`, `table_name`); the adapter takes
them from the run config's `adapter:` block. This is the one place the normalized
workload leans on NeuChain-side config — recorded as a manifest caveat.

### 3.4 Block result frame (hand-rolled, NOT protobuf)

`MockTransaction::serializeResultToString` — each `block.data.data[i]`:

```
uvarint tid
uvarint epoch
uvarint digestLen
bytes   digest            # == UserRequest.digest we sent
uvarint result            # enum TransactionResult
```

`enum TransactionResult { COMMIT = 0, PENDING = 1, ABORT = 2, ABORT_NO_RETRY = 3 }`.
Harness mapping: `result == 0` → `Valid = true`; `result == 2 || 3` → committed
but `Valid = false` (counts as failure); `result == 1` should not appear in a
committed block (treat as still-pending).

The full-tx frame (`serializeToString`) additionally carries payload + KVRWSet +
a trailing 32-byte SHA-256; the client only ever parses the **result** frame from
blocks, so Go only needs the 5-field decode above.

## 4. What is NOT reimplemented / assumed

Each becomes a manifest caveat on NeuChain runs:

- **Single logical user.** NeuChain's multi-proxy mode keeps a separate user
  keypair per block-server IP. The adapter uses one user keypair for all servers
  (`sendToAllClientProxy=false` semantics). Fine for a benchmark client; noted.
- **No client-side MVCC awareness.** The adapter submits and waits; conflict
  resolution is entirely server-side (deterministic execution), surfaced only as
  `result == ABORT`.
- **`funcName` / `table` come from NeuChain chaincode config**, not from the
  normalized workload (see §3.3).
- **RSA-1024.** NeuChain fixes `RSA_KEY_LENGTH 1024`. Weak by modern standards
  but it is what the platform verifies; the adapter matches it. Recorded in
  `CryptoInfo`.
- **Private-key password is empty** (`generateCrypto` passes an empty password).
  If a deployment sets one, the adapter needs the passphrase in config.
- **Query load.** `tip_query` / `block_query` polling adds REQ/REP load on
  `:7003`; at high block rates the poller may lag. Poll interval is configurable;
  a lagging poller shows up as growing finality latency, not lost txs.

## 5. CryptoInfo reported

```
signature_alg:             RSA-1024-PKCS1v15
hash_alg:                   SHA-256
per_tx_endorsement_verify:  false      # deterministic EV: block server verifies the user signature on submit, but there is no endorsement round
msp_note:                   "NeuChain EV: RSA-1024 user signature over the serialized TransactionPayload; no endorsement phase, ordering implicit via deterministic execution"
```

> Note: NeuChain *does* verify the user's RSA signature when a transaction is
> received (`CryptoHelper::getUserSigner(ip)->rsaDecrypt`). It is a single
> submit-time check, not the per-endorser, per-committer verification the EOV
> platforms do. `per_tx_endorsement_verify=false` is the right flag for the
> cross-platform table; this note carries the nuance.

## 6. Validation plan

- [ ] `scripts/neuchain-proto-spike.sh` generates stubs that compile.
- [ ] Unit test: build a `UserRequest` in Go, sign with a test RSA-1024 key,
      verify with Go's `rsa.VerifyPKCS1v15` (round-trip) and against an
      OpenSSL-generated signature of the same bytes (cross-impl).
- [ ] Unit test: decode a hand-crafted result frame, assert tid/epoch/digest/
      result.
- [ ] Integration (`//go:build integration`, live 4-node NeuChain): submit 10
      writes, poll to finality, assert every digest resolves to `COMMIT` and
      `T1 ≤ T2 ≤ T3`.
- [ ] Cross-check harness TPS against NeuChain's own `StatusThread` KTPS log over
      the same window (within ~10%).

## 7. Known divergences from the VLDB paper setup

- Paper runs `small_bank` / `ycsb` at cluster scale; our `local` profile is
  4 block servers + 1 epoch server on one 16-core host — far below paper
  hardware. `local` NeuChain numbers are not comparable to the paper; only
  `gcp-full` is.
- Paper uses the `neuchain_deployer` (`aria_deliver_server` + `deliver_client`)
  for orchestration; we run the servers directly in containers
  (`deploy/docker/neuchain/`), which does not change protocol behaviour but does
  mean our config files are hand-written, not deployer-generated.
- DB must be initialised once (`server.cpp` lines 48-49 uncommented, run
  `db_init`); the container build does this and ships the pre-initialised
  `small_bank` / `ycsb` databases so every node starts identical.
