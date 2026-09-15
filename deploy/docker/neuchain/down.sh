#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE" || exit 1
need docker
# -v: state lives in the containers' anonymous volumes; every run starts empty.
docker compose --profile init down -v "${@:2}" || warn "docker compose down reported an error (already down?)"
rm -f "${HERE}/connection.env"
drop_caches
log "neuchain down (image and .cache/crypto kept; 'docker rmi' / rm -rf .cache to reclaim)"
