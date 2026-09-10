#!/usr/bin/env bash
# Build the NeuChain images. SLOW and heavy: install_deps.sh compiles ~15 C++
# libraries from source (protobuf 3.19.4 autotools + brpc + braft ...). Budget
# ~1 hour, ~30 GB free disk, 16 GB+ RAM. Run this on a build host, not during a
# benchmark.
#
#   deploy/docker/neuchain/build.sh            # ref=ev
#   NEUCHAIN_REF=ev BUILD_JOBS=8 deploy/docker/neuchain/build.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

REF="${NEUCHAIN_REF:-ev}"
REPO="${NEUCHAIN_REPO:-https://github.com/iDC-NEU/NeuChain.git}"
JOBS="${BUILD_JOBS:-4}"
CACHE="${HERE}/.cache"
SRC="${CACHE}/NeuChain"

mkdir -p "$CACHE"
if [ ! -d "$SRC/.git" ]; then
  echo "== clone NeuChain @ $REF"
  git clone --branch "$REF" --depth 1 "$REPO" "$SRC"
fi

echo "== build bench/neuchain-deps:${REF}  (repo Dockerfile -> install_deps.sh, ~1h)"
docker build -t "bench/neuchain-deps:${REF}" "$SRC"

echo "== build bench/neuchain-build:${REF}  (compile block_server/epoch_server/user)"
docker build -f Dockerfile.build \
  --build-arg DEPS_IMAGE="bench/neuchain-deps:${REF}" \
  --build-arg BUILD_JOBS="${JOBS}" \
  -t "bench/neuchain-build:${REF}" .

echo "== build bench/neuchain:${REF}  (slim runtime)"
docker build -f Dockerfile.run -t "bench/neuchain:${REF}" .

echo
echo "done. images:"
docker images | grep -E "bench/neuchain(-deps|-build)?:" || true
echo
echo "next:"
echo "  1. extract config templates + user keys (see up.sh TODO / neuchain-client-implementation.md)"
echo "  2. bash deploy/docker/neuchain/up.sh local"
