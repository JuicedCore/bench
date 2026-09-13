#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE" || exit 1
docker compose down "${@:2}" 2>/dev/null || true
rm -f "${HERE}/connection.env"
drop_caches
log "neuchain down (images bench/neuchain-build:ev and bench/neuchain:ev kept; 'docker rmi' to reclaim space)"
