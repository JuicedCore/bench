#!/usr/bin/env bash
# Bring up the monitoring stack + every platform whose deploy is ready and whose
# ports do not collide.
#
#   scripts/up-all.sh [profile] [fabric-variant]
#     profile         default: local
#     fabric-variant  default: fabric-cft   (fabric-cft | fabric-bft | drunix)
#
# PORT REALITY: fabric-cft, fabric-bft and drunix all bind peer :7051 /
# orderer :7050 / operations :9443 - they are MUTUALLY EXCLUSIVE. Only one
# Fabric-family network runs at a time; pass the one you want as arg 2.
# fabricx (:6022/:6023/:4001/:9643) and neuchain (:5001/:7003/:18000) use
# distinct ports and come up alongside it when their images exist.
#
# For a fair cross-platform comparison you still run platforms SEQUENTIALLY with
# inter-run isolation - use scripts/run-all.sh for that, not this.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PROFILE="${1:-local}"
FABRIC_VARIANT="${2:-fabric-cft}"

log() { printf '\033[1;34m[up-all]\033[0m %s\n' "$*"; }

case "$FABRIC_VARIANT" in
  fabric-cft|fabric-bft|drunix) ;;
  *) echo "arg 2 must be fabric-cft | fabric-bft | drunix"; exit 2 ;;
esac

log "monitoring stack"
bash deploy/docker/monitoring/up.sh || log "monitoring failed (non-fatal)"

log "Fabric family: ${FABRIC_VARIANT} (profile ${PROFILE})"
bash "deploy/docker/${FABRIC_VARIANT}/up.sh" "$PROFILE"

# fabricx: only if its image was built, since the first build compiles Arma and
# the committer (15-20 min). FABRICX=1 builds it here.
if docker image inspect bench/fabricx:local >/dev/null 2>&1 || [ "${FABRICX:-0}" = 1 ]; then
  log "fabricx"
  bash deploy/docker/fabricx/up.sh "$PROFILE" || log "fabricx up failed (non-fatal)"
else
  log "fabricx: skipped (image bench/fabricx:local not built - run with FABRICX=1, or deploy/docker/fabricx/up.sh)"
fi

# neuchain: only if the runtime image was built (deploy/docker/neuchain/build.sh).
if docker image inspect bench/neuchain:ev >/dev/null 2>&1; then
  log "neuchain"
  bash deploy/docker/neuchain/up.sh "$PROFILE" || log "neuchain up failed (non-fatal)"
else
  log "neuchain: skipped (image bench/neuchain:ev not built - see deploy/docker/neuchain/build.sh)"
fi

echo
log "up. connection.env files:"
for f in deploy/docker/*/connection.env; do [ -f "$f" ] && echo "  $f"; done
log "Grafana http://localhost:3000  Prometheus http://localhost:9090"
