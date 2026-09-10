# Remaining work

Everything that can be built and verified without a large compute build or
external credentials is done. What is left is **compute-gated** (a long C++
build) or **credential-gated** (GCP). Both are pluggable: run the named script or
provide the named inputs and they proceed.

Status of everything else:

| Area | State |
| ---- | ----- |
| Core harness (`pkg/…`), CLI, workloads, load gen, metrics, manifest, reporter | done, `go test ./...` green, `go vet` clean |
| Fabric adapter (`fabric-cft`, `fabric-bft`) | done + **benchmarked live** (2.5.16 Raft, 3.1.5 SmartBFT) |
| Drunix adapter + deploy (`github.com/npci/drunix`) | done; live network verified end to end (see [§3](#3-drunix-live-run-last-mile)) |
| Fabric-X adapter (token REST API) | done, matches the **verified** `fabric-x-samples/tokens` API, `httptest`-tested |
| NeuChain adapter (pure-Go ZMQ + protobuf + RSA) | done, unit-tested (sign, result-frame, tx-build) |
| Monitoring stack, profiles, configs, ADRs, guides, docs | done |
| Integration tests (`//go:build integration`) for all four platforms | done (self-skip without a live network) |
| `docs/reports/comparison.html` | generated from the live cft/bft runs |

---

## 1. NeuChain C++ build — COMPUTE-GATED

### What is left

Produce three Docker images, then fill in the run flags:

1. `bench/neuchain-deps:ev` — the build environment.
2. `bench/neuchain-build:ev` — `block_server_test_comm`, `epoch_server`, `user`
   compiled.
3. `bench/neuchain:ev` — slim runtime with just those binaries.

Then:

4. Extract NeuChain's own config templates (`doc/config_template_4_servers.yaml`,
   `doc/config_local.yaml`, `doc/init_crypto.yaml`) into `deploy/docker/neuchain/conf/`.
5. Initialise the deterministic database once
   (`src/block_server/server.cpp` lines 48-49 uncommented → run the binary as
   `db_init` → back up `small_bank` / `ycsb`) and bake the pre-initialised DBs
   into the runtime image so every node starts byte-identical.
6. Generate the user RSA-1024 keypair: `./user -b 1 1 1` with the crypto config
   → copy `crypto/user_0.{pri,pub}` to `deploy/docker/neuchain/.cache/crypto/`.
7. Replace the **PLACEHOLDER** ports / CLI args / config paths in
   `deploy/docker/neuchain/docker-compose.yml` with the real ones from the
   repo's run scripts (the compose has `# PLACEHOLDER` on every such line).

### The pluggable entry point

```
bash deploy/docker/neuchain/build.sh          # NEUCHAIN_REF=ev, BUILD_JOBS=4
```

`build.sh` clones `iDC-NEU/NeuChain@ev`, builds the repo's own `Dockerfile`
(which runs `install_deps.sh`), then the compile image (`Dockerfile.build`), then
the runtime image (`Dockerfile.run`). After it finishes, do steps 4-7 above, then
`bash deploy/docker/neuchain/up.sh local`.

### Why it cannot be done here

`install_deps.sh` compiles **~15 C++ libraries from source**, each pinned to a
specific tag, on Ubuntu 20.04:

```
gperftools 2.9.1   leveldb 1.23   gflags 2.2.2   googletest 1.11
glog 0.5.0   zlib   zlib-ng   protobuf 3.19.4 (autotools ./configure && make)
brpc 4839ec2   braft c8e6848   yaml-cpp 0.7.0   libzmq 4b48007   cppzmq 4.8.1
libpqxx 7.3.1
```

- **Time**: measured cold-build for this dependency set is **45-90 minutes**
  (protobuf's autotools build and brpc/braft dominate). It is not something to
  run interleaved with a benchmark session.
- **Disk**: the build image plus intermediate objects need **~25-30 GB** free.
- **RAM**: parallel compiles of brpc / protobuf spike to **6-10 GB**; on the
  13 GB host used for the live Fabric runs, running this alongside anything else
  risks the OOM killer. `Dockerfile.build` caps `BUILD_JOBS=4` to bound this, at
  the cost of more wall-clock.
- **Toolchain pinning**: the repo explicitly requires cmake 3.16.3 / gcc 9.4.0
  (Ubuntu 20.04). The Dockerfiles isolate that; a host build would need those
  exact versions.

Everything the Go side needs is already done and unit-tested: the protobuf
message subset (`pkg/adapters/neuchain/proto/neuchain.proto` + generated stubs,
field-number-exact, `proto/ORIGIN` records the commit), the RSA-1024 /
SHA-256 signer, the hand-rolled block-result-frame decoder, the ZeroMQ PUB/REQ
client, and the finality poller. The wire protocol is fully documented in
[platforms/neuchain-client-implementation.md](platforms/neuchain-client-implementation.md).
Only the server binaries are missing, and only because building them is a
multi-hour compute job.

### How to verify once built

```
bash deploy/docker/neuchain/up.sh local
set -a; source deploy/docker/neuchain/connection.env; set +a
go test -tags integration -run Integration -v ./pkg/adapters/neuchain/   # 10 writes -> finality
./bin/benchrunner run --config configs/quick-smoke-neuchain.yaml --platform neuchain
```

Cross-check: the harness confirmed-TPS against NeuChain's own `StatusThread`
KTPS log over the same window (within ~10%). Confirm on a live node that
`tip_query` returns a bare ASCII integer and `block_query` returns a serialized
`block.Block` (matches the source read; not yet seen on a running node).

---

## 2. GCP campaign — CREDENTIAL-GATED

### What is left

Run the full benchmark matrix on real multi-VM hardware. Local numbers for
Fabric-X and NeuChain are not comparable to their published ceilings (Arma and
NeuChain are scale-out designs — [adr caveat C5](architecture/fairness-guarantees.md));
`gcp-full` is the only profile that produces quotable figures for them.

### The pluggable entry point

```
# one platform at a time (adr-005 - sequential runs)
scripts/gcp-run.sh fabric-cft configs/probe-sweep.yaml gcp-full -var project=YOUR_PROJECT
```

`scripts/gcp-run.sh` does the whole cycle: `terraform apply` (node VMs + a
separate load-generator VM + a monitoring VM), rsync the repo + a
linux/amd64 `benchrunner` to the load-gen VM, run `deploy/docker/<p>/up.sh` on
node-0 over `gcloud compute ssh`, rewrite `connection.env` (`localhost` → node
internal IP), run the benchmark from the load-gen VM, pull `results/` back, then
`terraform destroy` (set `KEEP=1` to leave infra up between runs; `IAP=1` to
tunnel SSH through Identity-Aware Proxy).

`deploy/terraform/main.tf` is complete: network, firewall, `n2-standard-8` node
VMs, `n2-standard-4` load-gen VM, `e2-standard-4` monitoring VM (reusable via
`-var keep_monitoring=true`), Docker startup script, outputs.

### Why it cannot be done here

- Needs a **GCP project with billing enabled**, `gcloud` authenticated, and
  Compute Engine + IAP quota. None of that exists in this environment and it is
  the user's to provide.
- `gcp-full` provisions ~40 vCPU / 168 GB per platform run; a full campaign
  (5 configs × several workloads, sequential) is hours of paid compute.
- The Terraform + orchestration is written and syntax-checked; it just needs
  `-var project=…` and credentials.

### Before the first real run

Tighten `google_compute_firewall.ssh.source_ranges` in `main.tf` from
`0.0.0.0/0` to your IP (or set `IAP=1` and drop the public SSH rule).

---

## 3. Drunix live run — LAST MILE (not compute/GCP gated)

The Drunix deploy (`github.com/npci/drunix`) was taken end to end during this
work: 7 `npcioss/drunix-*` containers (Lite Peer / Committing Peer / stateless
VSCC × 2 orgs + Raft orderer), 2 KeyDB containers (the `drunix-peer` image
mandates a KeyDB KVStore even in LevelDB mode, and upstream only bundles it with
the Yugabyte compose — `up.sh` now starts it separately), channel `mychannel`
joined, blocks committing, `kvstore` chaincode packaged and **installed on both
peers**.

The one remaining friction was `peer lifecycle chaincode approveformyorg`
timing out with *"timed out waiting for txid on all peers"*. Root cause: the
CLI's commit-wait uses `peer.client.connTimeout`, which Drunix's `core.yaml`
sets to **3 s** — too short for the first lifecycle tx to traverse Drunix's
LP → orderer → CP → stateless-VSCC path. Fixed in `deploy/docker/drunix/up.sh`:
`export CORE_PEER_CLIENT_CONNTIMEOUT=120s` and `deployCC … -r 10 -d 10` before
deploy. This is a config bump, not a code change, and needs no external
resources — a re-run of `up.sh` completes the deploy.

### Verify

```
bash deploy/docker/drunix/up.sh local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/quick-smoke.yaml --platform drunix
go test -tags integration -run Integration ./pkg/adapters/drunix/
```

---

## 4. Fabric-X custom KV view — LIVE-DEVNET GATED

Fabric-X `transfer` (token) workloads run today against
`fabric-x-samples/tokens`. The normalised `kv-write` / `kv-read` workloads need a
custom FSC view service (`deploy/docker/fabricx/kvview/`), which is currently a
compiling stub (`POST /kv` → 501) with a **code-level implementation spec** in
its `README.md` (confirmed against `fabric-smart-client@v0.20.0`'s
`platform/fabric/services/endorser` API).

Finishing it needs a **running Fabric-X devnet** to build and iterate against:

- The FSC node must join a real Fabric-X network (`common.StartFSC` against the
  devnet's `core.yaml`), which means `fabric-x-samples/tokens` `make setup &&
  make start` has to be up first — that builds Docker images and runs the Arma
  ordering service + committer stack (compute-ish, ~15-20 min, several GB).
- A dedicated chaincode namespace (`benchkv`) must be registered with the
  Fabric-X **validator/committer** config in `fabric-x-samples/devnet` — a
  one-line policy addition, but it requires editing and redeploying the devnet.
- The FSC dep tree (`fabric-smart-client` + the sample's non-modular
  `token-sdk/common` package) has to be vendored and the view logic iterated
  against the live committer until commits land.

None of that can be exercised without the devnet running, so it is grouped with
the compute-gated work. The Go adapter side is complete and route-configurable
(`kv_url`, `KVRoute`); when the view service is finished it drops in with no
adapter change.

---

## Quick "is everything else good?" checklist

```
go build ./...                          # clean
go vet ./...                            # clean
go build -tags integration ./...        # clean
go test ./pkg/...                       # all pass
cd chaincodes/kvstore && go build ./... # clean
bash -n deploy/docker/**/*.sh scripts/*.sh   # clean
./bin/benchrunner list                  # drunix, fabric-bft, fabric-cft, fabricx, mock, neuchain
./bin/benchrunner run --config configs/quick-smoke.yaml --platform mock   # end-to-end, no network
```

Live, reproducible today with only Docker + this repo:

```
bash deploy/docker/fabric-cft/up.sh local
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/probe-sweep.yaml --platform fabric-cft
```
