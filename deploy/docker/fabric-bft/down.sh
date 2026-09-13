#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SAMPLES="${REPO_ROOT}/deploy/docker/.cache/fabric-samples"
if [ -d "${SAMPLES}/test-network" ]; then
  cd "${SAMPLES}/test-network" || exit 1
  export PATH="${SAMPLES}/bin:${PATH}"
  ./network.sh down || true
fi
rm -f "${HERE}/connection.env"
drop_caches
log "fabric-bft down"
