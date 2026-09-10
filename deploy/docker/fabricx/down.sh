#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"
docker compose down 2>/dev/null || true
FXS="${HERE}/.cache/fabric-x-samples"
[ -d "$FXS/tokens" ] && ( cd "$FXS/tokens" && make teardown ) || true
rm -f "${HERE}/connection.env"
drop_caches
log "fabricx down"
