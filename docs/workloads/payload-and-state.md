# Payload to world state

What the load generator actually emits, what each adapter turns that into on
the wire, what the platform does with it, and which bytes land in world state.
Companion to [normalized.md](normalized.md) (the logical workloads) and
[mismatches.md](mismatches.md) (where those mappings are a poor fit).

The generator never sends a Fabric envelope or a NeuChain protobuf. It only
emits one Go struct (`adapters.Transaction` in `pkg/adapters/adapter.go`). Each
adapter rebuilds that into the platform's native call. Platforms run one at a
time; `--platform` selects the adapter.

Sources: `pkg/workloads/workload.go`, `pkg/loadgen/generator.go`,
`pkg/adapters/{fabric,drunix,fabricx,neuchain}`, `chaincodes/kvstore`.

## Concrete example used below

Same generator output on every platform.

**Write** (`kv-write`, seq `7`):

```
Kind:  TxWrite
Key:   "key-000000042"
Value: 64 ASCII bytes, byte i = 'a' + (7+i)%26   →  hijklmnop…  (64 bytes)
DestKey: ""
Amount: 0
Seq: 7
```

**Transfer** (`transfer` / `contention.yaml`):

```
Kind:    TxTransfer
Key:     "acct-000000010"      // source
DestKey: "acct-000000025"      // dest
Amount:  1
Value:   nil                   // the workload does not set this
Seq:     7
```

**Read** (`kv-read`):

```
Kind: TxRead
Key:  "key-000000042"
Value: nil
```

```
Workload.Next(seq) → adapters.Transaction → platform adapter
        → native payload → endorse / order / execute
        → world state + committed block
```

The harness record (`TxRecord` in `result.json`) is **not** a dump of world
state. It is timings and validity. World state stays inside the platform.

---

## Shared input

| Field | Write | Read | Transfer |
| ----- | ----- | ---- | -------- |
| `Kind` | `TxWrite` | `TxRead` | `TxTransfer` |
| `Key` | `key-%09d` | `key-%09d` | `acct-%09d` (source) |
| `DestKey` | empty | empty | `acct-%09d` (dest; bumped by 1 on collision) |
| `Value` | `value_size_bytes` (default 64), deterministic, non-constant | empty | **empty** (amount is used instead) |
| `Amount` | 0 | 0 | always `1` |
| `Seq` | generator sequence; logging only, not sent on the wire | same | same |

Keys and values are produced by `pkg/workloads` from a seeded distribution
(`uniform` / `zipfian` / `fixed`). Same seed ⇒ same key stream on every
platform. That is the fairness lever: contention pattern is identical even
when on-wire bytes are not.

---

## 1. Fabric CFT and Fabric BFT

Same adapter (`pkg/adapters/fabric`), same chaincode (`chaincodes/kvstore` on
channel `mychannel`). Only consensus differs (Raft vs SmartBFT). Normalized
runs request **LevelDB**.

Gateway flow for writes: `NewProposal` → **Endorse** → **Submit** to the
orderer. The one-shot `SubmitTransaction` is not used, so T2 (orderer accept)
and T3 (committed block) stay distinct.

### 1a. `kv-write` — `Put`

| Stage | What exists |
| ----- | ----------- |
| Generator input | `TxWrite`, key `key-000000042`, 64-byte value |
| Adapter rewrite | none; bytes pass through |
| Gateway call | `NewProposal("Put", args=[key, value])` → Endorse → Submit |
| Chaincode | `PutState("key-000000042", <those 64 bytes>)` |
| World state after commit | key `key-000000042` = `hijklmnop…` (64 raw bytes, not JSON) |
| If the key already existed | overwritten. No read of the old value |

```go
func (s *SmartContract) Put(ctx contractapi.TransactionContextInterface, key string, value string) error {
    return ctx.GetStub().PutState(key, []byte(value))
}
```

**Commit path**

1. Peer **executes** `Put` during endorsement → write-set `{key-000000042 → 64 bytes}`.
2. Client and peer **sign** the proposal (ECDSA-P256).
3. Orderer **orders** the envelope into a block (pinned: 100 messages / 1 s / 2 MB).
4. Committing peer **validates** (endorsement policy + MVCC). `Put` has no read-set, so MVCC almost never fails.
5. If valid, statedb **applies the write-set**. That is the world-state change.
6. The adapter's filtered-block listener sees the tx → **T3**, `Valid=true`.

A committed-but-invalid tx (policy / MVCC) is still in the block, with
`Valid=false`. It does not count as throughput.

### 1b. `transfer` — real balance read-modify-write

| Stage | What exists |
| ----- | ----------- |
| Generator input | `TxTransfer`, from `acct-000000010`, to `acct-000000025`, amount `1`, **Value empty** |
| Adapter rewrite | `Transfer("acct-000000010", "acct-000000025", "1")` |
| Chaincode | reads both balances, subtracts/adds 1, writes JSON |

The **ledger key is not** `acct-000000010`. Chaincode prefixes it:

```go
func acctKey(account string) string { return "acct/" + account }
```

**First transfer of a never-seen account.** Missing key → lazy default
balance `1_000_000_000` (no seeding phase). After this tx:

| Ledger key | Value |
| ---------- | ----- |
| `acct/acct-000000010` | `{"balance":999999999}` |
| `acct/acct-000000025` | `{"balance":1000000001}` |

A second successful transfer of the same pair decrements/increments again.
The 64-byte workload payload is **not used**. Transfer only uses `Amount`.

**Why `contention.yaml` fails some txs.** Endorsement reads versions V of both
keys. If another transfer of a hot Zipfian account commits first, validation
sees a version mismatch → `MVCC_READ_CONFLICT` → in the block with
`Valid=false`. **Balances do not change** for that invalid tx.

Commit path is the same EOV as `Put`, but the write-set is two JSON balances
and the read-set makes MVCC real.

### 1c. `kv-read`

| Stage | What happens |
| ----- | ------------ |
| Adapter | `Evaluate("Get", key)` on **one peer** |
| Chaincode | `GetState(key)` → string, or `""` if missing |
| Orderer | **not involved** |
| World state | **unchanged** |
| `WaitForFinality` | returns immediately `Valid=true` |

This is not a committed transaction. Fabric read numbers are an evaluate
round-trip. Do not quote them beside Fabric-X or NeuChain reads
([mismatches.md](mismatches.md)).

---

## 2. Drunix

Same chaincode, same function names, same logical keys. Differences are
**where** execution happens and **how writes are encoded**.

Drunix splits the peer: **Lite Peer** (endorse + broadcast) and **Committing
Peer** (validate + commit to **YugabyteDB**). The Gateway sits on the Lite
Peer, which never commits.

The adapter wraps the Fabric adapter (`pkg/adapters/drunix`).

### Adapter change (writes only)

Raw 64-byte payloads panic YugabyteDB (it JSONB-casts every value). Before
calling Fabric `Submit`:

```
hijklmnop…   →   json.Marshal(string)   →   "hijklmnop…"   (JSON string, including quotes)
```

On-wire value is a few bytes larger than 64. Disclosed as a manifest caveat.

Reads and transfers are **not** wrapped: transfer values are already JSON
`{"balance":…}` from chaincode. `Query` unwraps JSON strings back to the
original 64 bytes for the harness.

### `kv-write`

| Stage | fabric-cft / fabric-bft | drunix |
| ----- | ----------------------- | ------ |
| Input | 64 raw bytes | 64 raw bytes |
| Adapter | pass through | wrap as a JSON string |
| Chaincode | `PutState(key, value)` | same, but value is `"hijklmnop…"` including quotes |
| **World state** | `key-000000042` = 64 bytes | `key-000000042` = JSON text `"hijklmnop…"` |
| Query | returns 64 bytes | adapter unwraps to 64 bytes |

### `transfer`

Identical to Fabric: keys `acct/acct-…`, JSON balances, real arithmetic. No
extra wrap.

### Commit path

```
Client → Lite Peer (endorse + broadcast) → orderer
              ↘
         Committing Peer (validate + commit to YugabyteDB)
```

- **T2:** Lite Peer / orderer accept (same point as Fabric gateway Submit).
- **T3:** **Committing Peer's** filtered-block stream. Gateway `Commit.Status()`
  would never fire, because the Lite Peer does not commit.

---

## 3. Fabric-X

No chaincode. The adapter **is** the contract: it builds the read/write set
the committer will apply (`pkg/adapters/fabricx`). Namespace id is `"0"`
(from `connection.env`). State is **PostgreSQL**.

Submit: client ECDSA-P256 endorsement over the namespace's ASN.1 encoding
(SHA-256 digest; txid is inside the signed payload, so it cannot be replayed)
→ wrap as `applicationpb.Tx` + Fabric `Envelope` → broadcast the **same**
envelope to all four Arma routers. First `SUCCESS` ack is T2 (router accepted
and forwarded to a batcher). Sending to one router whose party is not primary
adds ~10 s of FirstStrike delay.

Finality: sidecar deliver stream (`:4001`). T3 is stamped when the block is
decoded. Committer status `COMMITTED` → valid; anything else → `Valid=false`.

### 3a. `kv-write` — blind write

Adapter builds:

```
TxNamespace {
  NsId: "0"
  NsVersion: 0
  BlindWrites: [
    { Key: "key-000000042", Value: <64 bytes hijklmnop…> }
  ]
}
```

Arma orders a digest; assemblers rebuild the block; the committer verifies
the signature against the namespace policy in `_meta`, then applies writes.

**World state after commit**

| Store | Key | Value |
| ----- | --- | ----- |
| PostgreSQL, namespace `0` | `key-000000042` | the **same 64 bytes** the generator made |

No chaincode transform. Input bytes **are** the stored bytes. Blind write =
no read-set, so no MVCC on the old version. Last writer wins.

### 3b. `kv-read` — QueryService point lookup

The validator still rejects a read-only *transaction* (`MALFORMED_NO_WRITES`).
The adapter does not send one. `TxRead` calls `QueryService.GetRows` on `:7001`
(no view → latest committed snapshot):

```
GetRows { namespaces: [ { ns_id: "0", keys: ["key-000000042"] } ] }
```

**World state after the read:** unchanged. No `_r/...` dummy key.

T2 = GetRows returned. `WaitForFinality` is immediate `Valid: true` (same
shape as Fabric Evaluate). PostgreSQL is reached only through the query
process, never from the benchrunner.

`Query()` uses the same RPC. Missing keys are not-found, not an error.

### 3c. `transfer` — two-key write, not a balance

The generator sets `Amount=1` and **does not set `Value`**. The adapter does:

```
ReadWrites: [
  { Key: "acct-000000010", Value: tx.Value },   // nil / empty
  { Key: "acct-000000025", Value: tx.Value },   // nil / empty
]
```

**World state after a successful commit**

| Key | Value |
| --- | ----- |
| `acct-000000010` | empty |
| `acct-000000025` | empty |

No `acct/` prefix. No JSON. No `±1`. Both accounts are written to the **same
empty value**.

Comparable: two keys in one RW-set, so hot Zipfian keys can still MVCC-abort.
Not comparable: token / balance semantics. After many "transfers", Fabric
shows balances moving; Fabric-X shows two keys stuck at empty.

(The adapter unit test passes `Value: []byte("1")` by hand. The live
`transfer` workload does not, so production transfers write empty.)

---

## 4. NeuChain

No Fabric envelope. The adapter builds NeuChain YCSB protobufs and PUB-sends
them on ZeroMQ `:5001` (`pkg/adapters/neuchain`). Pure Go, no cgo. The YCSB
chaincode always uses table `ycsb`; the `TransactionPayload` header selects
the function. Wire protocol: [neuchain-client-implementation.md](../platforms/neuchain-client-implementation.md).

PUB is fire-and-forget. **T2 is "the send call returned"**, not a platform
ack. Submit/commit split is not comparable to Fabric; compare end-to-end
only. A heartbeat (`"empty"` txs, default 100 ms) keeps epochs moving so the
tail of a burst finalizes.

### 4a. `kv-write`

Nested payloads, inside-out:

```
YCSB_FOR_BLOCK_BENCH {
  values: [ { key: "field0", value: <64 bytes hijklmnop…> } ]
}
        ▼ marshal
YCSB_PAYLOAD {
  table: "ycsb"
  reads: [ "key-000000042", <marshaled record above> ]
  update: []
}
        ▼ marshal
TransactionPayload {
  header:  "write"
  payload: <YCSB_PAYLOAD>
  nonce:   (random high 32 bits) | 7     // seq in the low 32 bits
  digest:  empty                         // server fills
}
        ▼ marshal, RSA-1024 PKCS1v15 SHA-256
UserRequest {
  payload: <TransactionPayload bytes>
  digest:  <128-byte signature>          // this IS the tx id
}
```

That `UserRequest` blob is the wire payload. Block servers verify RSA and
execute YCSB `"write"`: merge `field0` into the record for `key-000000042`.
Deterministic execute-validate; no separate orderer.

**World state after `COMMIT`**

| Table | Key | Record |
| ----- | --- | ------ |
| `ycsb` | `key-000000042` | `{ field0: hijklmnop… }` (other fields left untouched) |

The 64 bytes land in **field `field0`**, not as the raw key value the way
Fabric `PutState` does.

**Finality.** Poller REQ `:7003` `tip_query` / `block_query`. Each block
entry is a hand-rolled varint frame, not protobuf:

```
tid, epoch, digestLen, digest, result
result: 0 = COMMIT, 2 = ABORT, 3 = ABORT_NO_RETRY
```

Match `digest` to the signature we sent. T3 = when that block was fetched.
`COMMIT` → world state changed; `ABORT` → **no** state change, `Valid=false`.

### 4b. `kv-read`

Same nesting, but:

```
header: "read"
YCSB_FOR_BLOCK_BENCH { values: [] }   // empty filter = all fields
```

This **is** a real committed transaction (unlike Fabric Evaluate). World
state is unchanged if it only reads. The harness still requires `COMMIT` to
count it.

### 4c. `transfer`

**Does not run.** `ycsbCall` returns:

```
transaction kind transfer is not supported by the NeuChain YCSB chaincode
(one key per call; transfers need the small_bank chaincode)
```

No payload is published. The collector records a submit error. World state
is unchanged. A unit test asserts that transfer must be rejected. Docs that
describe a two-key read+update set are the *intended* mapping, not the
current code.

---

## 5. Mock

In-process fake (`pkg/adapters/mock`). Submit sleeps a configured ack delay,
then schedules a fake commit. Writes go into `map[string][]byte`. Used by
`make smoke`. No network, no chaincode.

---

## One-page comparison: what lands in state

Same generator transaction on every platform.

### Write of `key-000000042` = 64-byte `hijklmnop…`

| Platform | Stored key | Stored value | Transform |
| -------- | ---------- | ------------ | --------- |
| fabric-cft / fabric-bft | `key-000000042` | 64 raw bytes | none |
| drunix | `key-000000042` | JSON string `"hijklmnop…"` | adapter JSON-wraps |
| fabricx | namespace `0` / `key-000000042` | 64 raw bytes | none (blind write) |
| neuchain | table `ycsb` / `key-000000042` | record `{field0: 64 bytes}` | wrapped into YCSB field |

### Transfer `acct-000000010` → `acct-000000025` amount 1

| Platform | Keys written | Values written | Arithmetic? |
| -------- | ------------ | -------------- | ----------- |
| fabric-cft / fabric-bft | `acct/acct-000000010`, `acct/acct-000000025` | `{"balance": N±1}` | **yes**, in chaincode |
| drunix | same | same JSON balances | **yes** |
| fabricx | `acct-000000010`, `acct-000000025` (no prefix) | **empty** (`tx.Value` is nil) | **no** |
| neuchain | — | — | **submit fails** |

### Read of `key-000000042`

| Platform | Hits ledger? | State change |
| -------- | ------------ | ------------ |
| fabric / drunix | evaluate one peer, no order | none |
| fabricx | QueryService GetRows, no order | none |
| neuchain | full commit | none (read-set transaction) |

---

## What the harness records vs world state

Two different outputs.

**World state** stays inside the platform, as in the tables above.
`result.json` does not dump it. `Query` exists for verification and is not
on the TPS path. Fabric-X and NeuChain `Query` currently return not-found.

**Harness record** — one `TxRecord` per transaction:

```
seq, txid,
scheduled, T1, T2, T3,
outcome = committed | invalid | error | timeout,
block number,
error string
```

| Timestamp | Meaning |
| --------- | ------- |
| scheduled | when the generator *meant* to send (open-loop latency is measured from here) |
| T1 | just before `adapter.Submit`, captured in the generator for every platform |
| T2 | platform ack (see table below) |
| T3 | adapter observed the tx in a committed block |

| Platform | T2 | T3 |
| -------- | -- | -- |
| fabric-cft / fabric-bft | gateway returns after the orderer accepted | block-event stream on the gateway peer |
| drunix | same as Fabric | Committing Peer filtered-block event |
| fabricx | first SUCCESS from the four Arma routers (writes); GetRows return (reads) | sidecar deliver stream (writes); immediate (reads) |
| neuchain | local ZMQ send return (no real ack) | poller decoded the block |

Aggregated over `[phaseStart+warmup, loadEnd−cooldown)`, keyed on
**scheduled** send time:

```
confirmed TPS = committed-and-valid / window seconds
end-to-end    = T3 − scheduled
submit        = T2 − T1
commit        = T3 − T2
send gap      = T1 − scheduled     // p99 > 50 ms → reject the run
```

Invalid (MVCC, policy) is a **failure**, not throughput. Written to
`results/<platform>/<timestamp>/` as `summary.txt`, `result.json`,
`manifest.json`, `phases.csv`.

**Input** is always the same `Transaction`. **On-wire payload** is rebuilt
per adapter. **World-state value** is identical bytes only for Fabric
CFT/BFT writes vs Fabric-X writes. Drunix quotes them as JSON, NeuChain
puts them in `field0`, and transfer is a real balance move only on
Fabric/Drunix.
