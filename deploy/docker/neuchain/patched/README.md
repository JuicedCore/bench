# Patched NeuChain build (provisional)

`bench/neuchain:ev` is currently this build, not a clean upstream one. It was
built in a separate NeuChain harness on 2026-09-04 (image id `e57b7f97611a`) and
retagged here. Upstream: `iDC-NEU/NeuChain@ev`.

Patches over upstream, all from that harness (`neuchain.Dockerfile` applies them):

| File | Change |
| ---- | ------ |
| `patches/crypto_sign.cpp` | global mutex around RSA sign/verify (OpenSSL 1.1 EVP_PKEY is not thread-safe on a shared key) - serializes crypto, likely lowers throughput |
| `patches/block_generator_impl.cpp` | block body generated for empty epochs |
| `patches/ev_consensus_manager.{h,cpp}`, `patches/epoch_server.cpp` | epoch server / consensus fixes |
| `patches/workload_size_adapter.h` | coordinator workload sizing |
| `user_collector.h` (sed) | `clientSize` 10 -> 1: serialize epoch assignment |
| `db_user_base.cpp` (sed) | client retry delay 0 -> 0.05 s (native `user` client only; unused by Bench) |
| `patches/status_thread_csv.patch` | latency CSV output |

Unpatched upstream was seen to crash in that harness (remote block-signature
CHECK failures, exit 139). Results on this image are **provisional**; the
manifest records `platform_version: neuchain-ev-patched`. The clean upstream
build (`../build.sh`) is still pending, see `docs/REMAINING-WORK.md` §1.

## Rebuilding

`neuchain.Dockerfile` expects the original harness layout as build context:
`third_party/NeuChain` (upstream checkout), `docker/patches/`,
`docker/entrypoint-*.sh`, `configs/neuchain/config-{dbinit,crypto}.yaml`.
Stage those from this directory and run
`docker build -f docker/neuchain.Dockerfile -t bench/neuchain:ev .`
(45-90 min; compiles ~15 C++ dependencies).
