#!/usr/bin/env bash
# Full local wipe: everything scripts/down-all.sh removes, PLUS generated
# chaincode images, dangling bench volumes, cloned deploy caches, connection.env
# files, and results / generated reports. It is destructive and asks first.
#
#   scripts/clean.sh                 # containers, volumes, caches, results
#   scripts/clean.sh --images        # ALSO remove pulled platform images
#                                    #   (Fabric, npcioss/drunix-*, yugabyte,
#                                    #    keydb, bench/neuchain*, bench/fabricx-rest)
#   scripts/clean.sh -y [--images]   # no prompt
#
# What it NEVER touches: containers/images/volumes this project did not create
# (e.g. an unrelated postgres), and the git-tracked source.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

YES=0; IMAGES=0
for a in "$@"; do
  case "$a" in
    -y|--yes) YES=1 ;;
    --images) IMAGES=1 ;;
    *) echo "unknown flag: $a"; exit 2 ;;
  esac
done

log()  { printf '\033[1;34m[clean]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[clean]\033[0m %s\n' "$*"; }

cat <<EOF
This will remove, for the blockchain benchmark harness only:
  - all platform + monitoring containers and their volumes
  - generated chaincode images (dev-peer0.*-kvstore*)
  - cloned deploy caches      deploy/docker/**/.cache/ , deploy/docker/.cache/
  - connection.env files      deploy/docker/*/connection.env
  - run outputs               results/*   (keeps results/.gitkeep)
  - generated reports         docs/reports/*.html , *.png
EOF
[ "$IMAGES" = 1 ] && echo "  - pulled platform images    (Fabric, npcioss/drunix-*, yugabyte, keydb, bench/neuchain*, bench/fabricx-rest)"
echo
if [ "$YES" != 1 ]; then
  read -r -p "proceed? [y/N] " ans
  case "$ans" in y|Y|yes|YES) ;; *) echo "aborted."; exit 0 ;; esac
fi

# 1. stop + remove containers / networks (reuses down-all's sweep)
log "stopping all platforms + monitoring"
bash scripts/down-all.sh local || true

# 2. generated chaincode images
log "removing generated chaincode images"
docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^dev-peer[0-9].*-kvstore' | xargs -r docker rmi -f >/dev/null 2>&1 || true
docker images --filter dangling=true --filter label=org.hyperledger.fabric -q | xargs -r docker rmi -f >/dev/null 2>&1 || true

# 3. dangling compose_* volumes left by the sample networks
log "removing bench compose volumes"
docker volume ls -q | grep -E '^compose_(peer|orderer)|_prom-data$|_grafana-data$|^bench-' | xargs -r docker volume rm -f >/dev/null 2>&1 || true

# 4. cloned deploy caches + connection.env
log "removing deploy caches + connection.env"
rm -rf deploy/docker/.cache
rm -rf deploy/docker/*/.cache
find deploy/docker -maxdepth 2 -name connection.env -delete
find deploy/docker -name '*.bench.bak' -delete
find deploy/docker -name 'keydb-only.bench.yaml' -delete

# 5. results + generated reports
log "removing results + generated reports"
find results -mindepth 1 -maxdepth 1 ! -name '.gitkeep' -exec rm -rf {} + 2>/dev/null || true
find docs/reports -maxdepth 1 -type f \( -name '*.html' -o -name '*.png' \) -delete 2>/dev/null || true

# 6. optional: pulled platform images
if [ "$IMAGES" = 1 ]; then
  log "removing pulled platform images"
  docker images --format '{{.Repository}}:{{.Tag}}' \
    | grep -E '^(ghcr\.io/)?hyperledger/fabric-(peer|orderer|ccenv|baseos|ca)|^hyperledger/fabric-(peer|orderer|ccenv|baseos|ca)|^npcioss/drunix-|^yugabytedb/yugabyte|^eqalpha/keydb|^bench/(neuchain|neuchain-build|neuchain-deps|fabricx-rest)' \
    | sort -u | xargs -r docker rmi -f >/dev/null 2>&1 || true
fi

# 7. tidy
docker network prune -f >/dev/null 2>&1 || true
log "local benchmark environment cleaned."
[ "$IMAGES" != 1 ] && warn "platform images kept (re-run with --images to remove ~4-6 GB of them)."
