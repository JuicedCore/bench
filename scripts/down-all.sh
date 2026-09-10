#!/usr/bin/env bash
# Bring DOWN every platform + the monitoring stack. Stops and removes the
# containers/volumes each platform's own down.sh manages. Leaves images, caches
# (deploy/docker/**/.cache), and results untouched - use scripts/clean.sh for a
# full wipe.
#
#   scripts/down-all.sh [profile]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PROFILE="${1:-local}"

log() { printf '\033[1;34m[down-all]\033[0m %s\n' "$*"; }

for p in fabric-cft fabric-bft drunix fabricx neuchain; do
  if [ -x "deploy/docker/${p}/down.sh" ]; then
    log "$p"
    bash "deploy/docker/${p}/down.sh" "$PROFILE" || log "$p down returned non-zero (continuing)"
  fi
done

log "monitoring"
bash deploy/docker/monitoring/down.sh || true

# Belt-and-braces: kill any bench container/network the down.sh scripts missed.
# (Never touches containers we did not create.)
log "sweeping stragglers"
docker ps -aq --filter label=service=drunix | xargs -r docker rm -f >/dev/null 2>&1 || true
for name in orderer.example.com peer0.org1.example.com peer0.org2.example.com \
            peer1.org1.example.com peer1.org2.example.com peer2.org1.example.com peer2.org2.example.com \
            lp1.org1 lp1.org2 cp.org1 cp.org2 vs1.org1 vs1.org2 \
            orderer2.example.com orderer3.example.com orderer4.example.com \
            yugabyte-org1 yugabyte-org2 hlf_keydb_org1msp hlf_keydb_org2msp \
            fabricx-kvview issuer.example.com endorser1.example.com owner1.example.com owner2.example.com \
            epoch-server block-server-0 block-server-1 block-server-2 block-server-3; do
  docker rm -f "$name" >/dev/null 2>&1 || true
done
for net in fabric_test drunix_test bench-monitoring_default bench-neuchain_nc bench-fabricx-kvview_default; do
  docker network rm "$net" >/dev/null 2>&1 || true
done

log "done. images + caches + results kept (scripts/clean.sh wipes those)."
