# NeuChain: build once, run anywhere — a step-by-step guide

## Who this is for

You want to get NeuChain running locally in this repo's benchmark harness, and
you want the result to also run on a **different** Linux machine later without
repeating the slow part. This guide covers both: the one-time build, and how to
carry it to another machine.

It does not re-explain the wire protocol or the Go adapter — that's already
documented in [`docs/platforms/neuchain-client-implementation.md`](../platforms/neuchain-client-implementation.md).
This guide is purely operational: what to run, in what order, what to check
after each step, and what to copy if you move machines.

## Why NeuChain needs this and the other platforms don't

Every other platform in this repo (`fabric-cft`, `fabric-bft`, `drunix`,
`fabricx`) brings its network up by **pulling pre-built images** from a public
registry (Hyperledger's official images, `npcioss/drunix-*`, etc.) — no local
compilation, on this machine or any other. NeuChain is different: upstream
(`iDC-NEU/NeuChain`) does not publish pre-built images anywhere. The only way
to get runnable binaries is to compile ~15 C++ libraries from source yourself,
once, inside a Docker build. That compile is what this guide walks through —
and it's also exactly the part you can avoid repeating on a second machine, if
you follow the portability section below.

## Before you start — resource check

Run this before doing anything else. The build genuinely needs the headroom;
do not skip this check.

```bash
df -h /home        # need >= 30 GB free on whatever filesystem holds this repo
free -h             # need >= 10 GB RAM actually available, not just "total"
```

- **Disk**: ~25-30 GB for the build image + intermediate compile objects. Less
  than ~35 GB free is cutting it close once you add Docker's own layer
  overhead — free up space first if you're near the line.
- **RAM**: parallel compilation (protobuf's autotools build, brpc, braft)
  spikes to 6-10 GB. If `free -h`'s "available" column is much below 10 GB,
  either close other work first or lower `BUILD_JOBS` (see Phase 1) to trade
  time for a smaller memory spike.
- **Time**: 45-90 minutes, mostly unattended, but the machine will be under
  real CPU/memory load that whole time — don't run it in the background while
  also trying to run a benchmark on another platform.
- **Toolchain**: none needed on your host. Everything (cmake 3.16.3, gcc
  9.4.0, Ubuntu 20.04) is pinned inside the Docker build stages.

## What you already have vs. what you must produce

| Already done in this repo | You must produce |
| --- | --- |
| Go adapter (`pkg/adapters/neuchain/`) — protocol, signing, ZeroMQ client, finality poller. Fully implemented and unit-tested. | The NeuChain **server binaries** (`block_server_test_comm`, `epoch_server`, `user`) — nobody has built these here yet. |
| `deploy/docker/neuchain/build.sh`, `Dockerfile.build`, `Dockerfile.run` — the build recipe. | Actually **running** that recipe to produce three Docker images. |
| `deploy/docker/neuchain/docker-compose.yml` — topology scaffolding (4 block servers + 1 epoch server). | Filling in its `# PLACEHOLDER` command/port lines with real values (Phase 2 below) — and reconciling a known mismatch (see the callout in Phase 2). |
| `deploy/docker/neuchain/up.sh` — already emits a `connection.env` with the ports the Go adapter expects. | The deterministic database and the RSA user keypair those ports/config assume. |

---

## Phase 1 — build the three Docker images

```bash
cd ~/bench
bash deploy/docker/neuchain/build.sh
```

Equivalent to, and configurable via:

```bash
NEUCHAIN_REF=ev NEUCHAIN_REPO=https://github.com/iDC-NEU/NeuChain.git BUILD_JOBS=4 \
  bash deploy/docker/neuchain/build.sh
```

- `NEUCHAIN_REF` — the git ref to clone (defaults to `ev`; this repo's adapter
  was built against this branch, don't change it unless you know the wire
  protocol still matches).
- `BUILD_JOBS` — parallel compile jobs. Default `4`. Lower it (e.g. `2`) if
  your RAM headroom from the check above is marginal; the build takes longer
  but won't spike as hard.

### What happens, in order

1. **Clone**: `deploy/docker/neuchain/.cache/NeuChain/` ← `iDC-NEU/NeuChain@ev`
   (shallow clone, `--depth 1`).
2. **`bench/neuchain-deps:ev`** (~45-75 min, the slow part): builds the
   cloned repo's own `Dockerfile`, which runs its `install_deps.sh` —
   compiling gperftools, leveldb, gflags, googletest, glog, zlib/zlib-ng,
   protobuf 3.19.4 (autotools), brpc, braft, yaml-cpp, libzmq, cppzmq,
   libpqxx from source on Ubuntu 20.04. This is the stage that eats disk,
   RAM, and time.
3. **`bench/neuchain-build:ev`** (a few minutes): `Dockerfile.build` compiles
   just the three targets we need (`block_server_test_comm`, `epoch_server`,
   `user`) on top of the deps image, via cmake/`Release` build. Copies the
   resulting binaries, the `.proto` files, and the repo's `doc/` directory
   into `/out` inside the image.
4. **`bench/neuchain:ev`** (seconds): `Dockerfile.run` — a slim `ubuntu:20.04`
   image with just the runtime shared libs and the three binaries copied in.
   This is the **only image you actually need to run the network** — the
   deps/build images were just scaffolding to produce it.

### Watch it / verify it worked

The script prints a header before each stage (`== build bench/neuchain-deps:ev
...`). If you want to watch disk/memory while it runs, in a second terminal:

```bash
watch -n 5 'df -h /home; echo; free -h'
```

When it finishes, confirm all three images exist:

```bash
docker images | grep -E '^bench/neuchain(-deps|-build)?\s'
```

You should see three rows: `bench/neuchain-deps`, `bench/neuchain-build`,
`bench/neuchain`, each tagged `ev`.

### If it fails partway

- **OOM-killed compile** (the build process silently dies, or `docker build`
  reports a non-zero exit with no clear C++ error): lower `BUILD_JOBS` and
  re-run. Docker's build cache means completed layers (e.g. the deps image, if
  it finished) won't be rebuilt.
- **Disk full mid-build**: free space and re-run; same caching applies.
- **A specific library's `./configure`/`cmake` step errors**: this means
  upstream's `install_deps.sh` or `CMakeLists.txt` has drifted since this repo
  was tested against it. Check
  `deploy/docker/neuchain/cmakelists.patch` (referenced but optional in
  `Dockerfile.build` — create it if you need to patch something) and the
  upstream repo's own README/issues for that library version.

---

## Phase 2 — the manual setup steps

The build produces binaries; it does not configure them. Four things are still
needed before `docker compose up` will produce a working network.

### 2.1 Extract NeuChain's own config templates

```bash
mkdir -p deploy/docker/neuchain/conf
cp deploy/docker/neuchain/.cache/NeuChain/doc/config_template_4_servers.yaml deploy/docker/neuchain/conf/
cp deploy/docker/neuchain/.cache/NeuChain/doc/config_local.yaml deploy/docker/neuchain/conf/
cp deploy/docker/neuchain/.cache/NeuChain/doc/init_crypto.yaml deploy/docker/neuchain/conf/
```

Open these three files and read them — they define the actual config schema
the `block_server_test_comm`/`epoch_server` binaries expect on the command
line and in their config file (`--config /etc/neuchain/*.conf` in
`docker-compose.yml`). Adapt `config_template_4_servers.yaml` into the
`epoch.conf`/`block.conf` files `docker-compose.yml` mounts at
`./conf:/etc/neuchain:ro` — the exact field names come from this template, not
from this guide, since they're upstream's schema and may have changed since
this repo's adapter was last checked against them.

### 2.2 Initialise the deterministic database

NeuChain benchmarks want every node starting from a byte-identical database
(`small_bank` or `ycsb` dataset), so results are reproducible. This means a
one-time `db_init` run baked into the runtime image, not done live at
container start:

1. Inside the build image (or a container from `bench/neuchain-build:ev`),
   uncomment the `db_init`-triggering lines noted in
   `src/block_server/server.cpp` (around lines 48-49 in the cloned source —
   line numbers can drift with upstream changes, so search for the `db_init`
   comment block rather than trusting the exact line number).
2. Rebuild just that binary with the change, run it in `db_init` mode.
3. Back up the resulting `small_bank`/`ycsb` database files.
4. Bake that pre-initialised database into `bench/neuchain:ev` (add a `COPY`
   step in `Dockerfile.run`, or mount it as a volume at container start — a
   `COPY` is simpler for portability, since it travels with the image).

This step is the least mechanical of the four — it requires actually reading
`server.cpp` and `Dockerfile.run` together and deciding how to wire the
pre-initialised DB into the runtime image. Budget real time for it.

### 2.3 Generate the RSA-1024 user keypair

```bash
mkdir -p deploy/docker/neuchain/.cache/crypto
docker run --rm -v "$(pwd)/deploy/docker/neuchain/conf:/conf:ro" \
  -v "$(pwd)/deploy/docker/neuchain/.cache/crypto:/out" \
  -w /out bench/neuchain-build:ev \
  /out/../user -b 1 1 1   # adjust the binary path/working dir to match where Dockerfile.build actually copies `user`
```

(Adjust the exact invocation once you've read `doc/init_crypto.yaml` from 2.1
— `user -b 1 1 1` is the command noted in this repo's own build script
comments, but confirm its arguments against the upstream doc since they encode
key count/type and aren't self-explanatory.)

Confirm you end up with:

```
deploy/docker/neuchain/.cache/crypto/user_0.pri
deploy/docker/neuchain/.cache/crypto/user_0.pub
```

`up.sh` already points `BENCH_ADAPTER_USER_PRIV_KEY_PATH`/
`BENCH_ADAPTER_USER_PUB_KEY_PATH` at exactly these two paths — no config change
needed on the Go side once the files exist. Per
`docs/platforms/neuchain-client-implementation.md`, these keys are generated
with an **empty password** (plain PKCS#1) — the adapter assumes that; if you
generate keys with a passphrase, the adapter has no config field for it today.

### 2.4 Fill in `docker-compose.yml`'s PLACEHOLDER lines — and reconcile a port mismatch

`deploy/docker/neuchain/docker-compose.yml` currently has:

```yaml
command: ["--config", "/etc/neuchain/epoch.conf"]      # PLACEHOLDER
ports: ["18000:18000"]                                  # PLACEHOLDER
...
command: ["--id", "0", "--config", "/etc/neuchain/block.conf"]   # PLACEHOLDER
ports: ["19000:19000"]                                  # PLACEHOLDER (gRPC submit)
```

**Important — these placeholder ports do not match the protocol the Go adapter
actually speaks.** Per
`docs/platforms/neuchain-client-implementation.md` §3.1-3.2 (verified against
NeuChain's own source), the client talks to each block server on:

- **`tcp://<block-server>:5001`** — ZeroMQ PUB socket, transaction submit.
- **`tcp://<block-server>:7003`** — ZeroMQ REQ socket, tip/block query.

`up.sh` already generates a `connection.env` assuming exactly this
(`BENCH_ADAPTER_BLOCK_SERVERS=localhost:5001,localhost:5011,localhost:5021,localhost:5031`,
`BENCH_ADAPTER_QUERY_ENDPOINT=localhost:7003`) — i.e. **the Go side already
assumes the real ports**; it's `docker-compose.yml`'s placeholders that are
stale and need to change, not the other way round. Concretely:

- Change each `block-server-N`'s exposed port mapping from `1900N:19000` to
  something that lands on `5001`/`7003` inside the container — e.g.
  `"5001:5001"` and `"7003:7003"` for `block-server-0`, and offset host-side
  ports for `block-server-1..3` (`5011:5001`, `7013:7003`, etc.) to match what
  `connection.env` already expects (`5011`, `5021`, `5031` — note `up.sh`
  currently only sets one query endpoint, `7003`, implying single-node query
  in the `local` profile; confirm this is intentional before assuming all 4
  block servers need a distinct exposed query port).
- The epoch server's `18000` port is **inter-server only** (block servers ↔
  epoch server over `brpc`'s `ChainService`, per the client-implementation
  doc's decision table) — the benchmark client never touches it directly, so
  it doesn't need to match any Go-side assumption. Leave it as whatever
  upstream's own multi-node example uses; just make sure it's consistent
  between the epoch server and all four block servers' config files.
- The `command:` args (`--config /etc/neuchain/epoch.conf` /
  `--id N --config /etc/neuchain/block.conf`) depend on the actual CLI flags
  `epoch_server`/`block_server_test_comm` accept — read those off `--help` on
  the built binary, or off `doc/config_template_4_servers.yaml` from step 2.1,
  since upstream's actual flag names aren't duplicated here to avoid this
  guide going stale independently of the source.

Resource limits (`BENCH_NC_CPUS`/`BENCH_NC_MEM`) are already wired from
`deploy/profiles/local.yaml`'s `neuchain: {nodes: {block_server: 4,
epoch_server: 1}}` — no change needed; `up.sh` splits the profile budget across the five containers
there.

---

## Phase 3 — bring it up and verify

```bash
bash deploy/docker/neuchain/up.sh local
set -a; source deploy/docker/neuchain/connection.env; set +a
```

Then, in order:

```bash
# 1. Protocol-level check: 10 writes -> finality, through the real adapter
go test -tags integration -run Integration -v ./pkg/adapters/neuchain/

# 2. Full harness smoke test
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform neuchain
```

Cross-check: compare the harness's confirmed-TPS in `summary.txt` against
NeuChain's own `StatusThread` KTPS log for the same time window (visible in
`docker logs` on a block server) — they should agree within ~10%. Also confirm
on a live node that `tip_query` returns a bare ASCII integer and `block_query`
returns a serialized `Block.Block` — the client-implementation doc notes this
was "not yet seen on a running node" as of last write-up, so this is the first
real confirmation that the documented wire format matches reality.

```bash
bash deploy/docker/neuchain/down.sh
```

(Keeps the built images; only tears down the running containers + removes
`connection.env`. `docker rmi bench/neuchain-build:ev bench/neuchain:ev` if you
want to reclaim the disk space instead.)

---

## Portability — running the *same* build on another Linux machine

This is the part that makes Phase 1's 45-90 minutes a one-time cost instead of
a per-machine cost.

### What actually needs to travel

| Artifact | Where it lives | How to move it |
| --- | --- | --- |
| The runtime image `bench/neuchain:ev` (with the baked-in deterministic DB from 2.2) | Docker's local image store | `docker save` / `docker load`, or push to a registry you control |
| Filled-in `docker-compose.yml` | `deploy/docker/neuchain/docker-compose.yml` | It's a repo file — commit it (once placeholders are resolved) or copy it directly |
| Config files | `deploy/docker/neuchain/conf/` | Copy the directory |
| RSA user keypair | `deploy/docker/neuchain/.cache/crypto/` | Copy the directory (contains a private key — treat it like any other credential in transit: scp/rsync over a trusted channel, not a public share) |

You do **not** need to move `bench/neuchain-deps:ev` or `bench/neuchain-build:ev`
— those only exist to produce the final runtime image. Confirm you're not
carrying them by accident (they're the multi-GB ones):

```bash
docker images bench/neuchain --format '{{.Repository}}:{{.Tag}}  {{.Size}}'
```

### Exporting

```bash
docker save bench/neuchain:ev | gzip > neuchain-ev.tar.gz
```

Check the resulting file size is in the hundreds-of-MB range (a slim
`ubuntu:20.04` base plus a handful of binaries and shared libs), not tens of
GB — if it's huge, you likely tagged/saved the wrong image (the build or deps
stage instead of the runtime one).

Then copy `neuchain-ev.tar.gz` plus the `conf/`, `.cache/crypto/`, and the
filled-in `docker-compose.yml` to the other machine (scp, rsync, or however you
move files between these two hosts).

### Importing on the other machine

```bash
docker load < neuchain-ev.tar.gz
docker images | grep bench/neuchain     # confirm it landed, tagged :ev
```

Then, in a checkout of this same repo on that machine:

1. Place the copied `docker-compose.yml`, `conf/`, and `.cache/crypto/` at the
   same relative paths under `deploy/docker/neuchain/`.
2. Run `bash deploy/docker/neuchain/up.sh local` — `up.sh` already checks for
   `bench/neuchain:ev` via `docker image inspect` before doing anything else,
   so it will skip straight to bringing the network up instead of demanding a
   rebuild.
3. Re-run Phase 3's verification steps on that machine to confirm parity.

### The one hard requirement: matching CPU architecture

Docker images are portable across Linux machines **only when the CPU
architecture matches** (e.g. both `amd64`/`x86_64`, or both `arm64`). A
`docker load` of an amd64 image on an arm64 host will fail to run (or run
under slow emulation if you have `qemu-user-static` set up, which is not
recommended for a benchmark — emulation would invalidate your latency/TPS
numbers). Check both machines first:

```bash
uname -m       # x86_64 or aarch64
```

If they don't match, you have two options, neither of which this guide covers
in depth since it's outside NeuChain-specific concerns:
- Rebuild natively on the second machine (back to Phase 1, on that machine).
- Produce a multi-arch image with `docker buildx build --platform
  linux/amd64,linux/arm64 ...` — only worth it if you'll be doing this
  repeatedly across mixed architectures.

### Why this also helps reproducibility, not just speed

Because the deterministic database and RSA keys are baked into the artifacts
you're copying, running the *same* exported image + config on two machines
gives you identical starting state on both — one less variable when comparing
results across environments, on top of saving the rebuild time.

---

## Quick reference — every command in one place

```bash
# --- resource check ---
df -h /home && free -h

# --- Phase 1: build (45-90 min) ---
bash deploy/docker/neuchain/build.sh
docker images | grep -E '^bench/neuchain(-deps|-build)?\s'

# --- Phase 2: manual setup ---
mkdir -p deploy/docker/neuchain/conf
cp deploy/docker/neuchain/.cache/NeuChain/doc/config_template_4_servers.yaml deploy/docker/neuchain/conf/
cp deploy/docker/neuchain/.cache/NeuChain/doc/config_local.yaml deploy/docker/neuchain/conf/
cp deploy/docker/neuchain/.cache/NeuChain/doc/init_crypto.yaml deploy/docker/neuchain/conf/
# ... db_init per §2.2 (manual, read server.cpp) ...
mkdir -p deploy/docker/neuchain/.cache/crypto
# ... generate user_0.pri/.pub per §2.3 ...
# ... edit docker-compose.yml per §2.4 (fix ports to 5001/7003, real CLI flags) ...

# --- Phase 3: run + verify ---
bash deploy/docker/neuchain/up.sh local
set -a; source deploy/docker/neuchain/connection.env; set +a
go test -tags integration -run Integration -v ./pkg/adapters/neuchain/
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform neuchain
bash deploy/docker/neuchain/down.sh

# --- Portability: export here ---
docker save bench/neuchain:ev | gzip > neuchain-ev.tar.gz
# copy neuchain-ev.tar.gz + deploy/docker/neuchain/{conf,.cache/crypto,docker-compose.yml} to the other machine

# --- Portability: import there ---
docker load < neuchain-ev.tar.gz
bash deploy/docker/neuchain/up.sh local
set -a; source deploy/docker/neuchain/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform neuchain
```

## Related reading

- [`docs/platforms/neuchain-client-implementation.md`](../platforms/neuchain-client-implementation.md) — the wire protocol, proto files, signing, and what the Go adapter does and doesn't reimplement.
- [`docs/REMAINING-WORK.md`](../REMAINING-WORK.md) §1 — the original compute-gated status this guide operationalizes.
- [`COMMANDS.md`](../../COMMANDS.md) — where NeuChain sits among the other platforms' run commands.
- [`CONFIGS.md`](../../CONFIGS.md) — which run-config YAMLs exist for NeuChain today.
