#!/usr/bin/env bash
# Move Docker images between machines with `docker save` / `docker load`.
#
#   scripts/images.sh export [--all] [dir]   # write <dir>/*.tar.gz + SHA256SUMS (default dir: images/)
#   scripts/images.sh import [dir]           # verify checksums, docker load every tarball
#
# Without --all only the images a fresh clone CANNOT obtain are exported:
#   bench/neuchain:ev  - the patched NeuChain build (deploy/docker/neuchain/patched/);
#                        rebuilding it takes 45-90 min.
# Everything else is pulled by the deploy scripts or built from pinned sources.
# --all also exports those pinned public images, for a machine with no or slow
# internet access. Images that are not present locally are skipped with a warning.
#
# Fabric-X is not included: deploy/docker/fabricx/up.sh always rebuilds
# bench/fabricx:local, so a loaded copy would not save the build.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_TAG=images
# shellcheck source=log-lib.sh
. "${ROOT}/scripts/log-lib.sh"

REQUIRED=(bench/neuchain:ev)
PUBLIC=(
  # fabric-cft (2.5.16) and fabric-bft (3.1.5): deploy/docker/lib.sh pin_fabric_images
  hyperledger/fabric-peer:2.5.16 hyperledger/fabric-orderer:2.5.16
  hyperledger/fabric-ccenv:2.5.16 hyperledger/fabric-baseos:2.5.16
  hyperledger/fabric-peer:3.1.5 hyperledger/fabric-orderer:3.1.5
  hyperledger/fabric-ccenv:3.1.5 hyperledger/fabric-baseos:3.1.5
  hyperledger/fabric-ca:1.5.22
  # drunix: deploy/docker/drunix/up.sh and its test-network compose
  npcioss/drunix-orderer:1.0.0 npcioss/drunix-peer:1.0.0 npcioss/drunix-vscc:1.0.0
  npcioss/drunix-ccenv:1.0 npcioss/drunix-baseos:1.0
  eqalpha/keydb:latest yugabytedb/yugabyte:2025.2.0.0-b131
  # monitoring: deploy/docker/monitoring/docker-compose.yml
  prom/prometheus:v3.1.0 grafana/grafana:11.4.0
  ghcr.io/google/cadvisor:0.57.0 prom/node-exporter:v1.9.0
)

usage() { sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

file_for() { printf '%s.tar.gz' "$(printf '%s' "$1" | tr '/:' '__')"; }

cmd_export() {
  local all=0 dir="${ROOT}/images"
  for a in "$@"; do
    case "$a" in
      --all) all=1 ;;
      -h|--help) usage ;;
      *) dir="$a" ;;
    esac
  done
  need docker
  need gzip
  mkdir -p "$dir"
  local imgs=("${REQUIRED[@]}")
  [ "$all" = 1 ] && imgs+=("${PUBLIC[@]}")
  local img f wrote=()
  for img in "${imgs[@]}"; do
    if ! docker image inspect "$img" >/dev/null 2>&1; then
      case " ${REQUIRED[*]} " in
        *" $img "*) die "required image ${img} is not on this machine" "load it from another machine, or build it: deploy/docker/neuchain/patched/README.md" ;;
      esac
      warn "skipping ${img}: not present locally (the deploy scripts will pull it)"
      continue
    fi
    f="$(file_for "$img")"
    log "saving ${img} -> ${dir}/${f}"
    docker save "$img" | gzip -1 > "${dir}/${f}.part"
    mv "${dir}/${f}.part" "${dir}/${f}"
    wrote+=("$f")
  done
  (cd "$dir" && sha256sum "${wrote[@]}" > SHA256SUMS)
  log "done: ${#wrote[@]} image(s), $(du -sh "$dir" | cut -f1) in ${dir}"
  log "copy the whole directory to the other machine, then: scripts/images.sh import <dir>"
}

cmd_import() {
  local dir="${1:-${ROOT}/images}"
  need docker
  [ -d "$dir" ] || die "no such directory: ${dir}" "copy the exported images/ directory here first"
  if [ -f "${dir}/SHA256SUMS" ]; then
    log "verifying checksums"
    (cd "$dir" && sha256sum -c --quiet SHA256SUMS) || die "checksum mismatch in ${dir}" "the copy is incomplete or corrupt; copy it again"
  else
    warn "no SHA256SUMS in ${dir}; loading without verification"
  fi
  local f n=0
  for f in "$dir"/*.tar.gz; do
    [ -e "$f" ] || die "no *.tar.gz in ${dir}"
    log "loading $(basename "$f")"
    gzip -dc "$f" | docker load
    n=$((n + 1))
  done
  local img
  for img in "${REQUIRED[@]}"; do
    docker image inspect "$img" >/dev/null 2>&1 || die "${img} is still missing after import" "was it exported? scripts/images.sh export"
  done
  log "loaded ${n} image(s); ${REQUIRED[*]} present"
}

case "${1:-}" in
  export) shift; cmd_export "$@" ;;
  import) shift; cmd_import "$@" ;;
  *) usage ;;
esac
