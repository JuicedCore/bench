#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"
UPSTREAM_COMPOSE="$(find "${HERE}/.cache" -maxdepth 5 \
  \( -name 'docker-compose*.y*ml' -o -name 'compose*.y*ml' \) 2>/dev/null | head -1 || true)"
[ -n "$UPSTREAM_COMPOSE" ] && docker compose -f "$UPSTREAM_COMPOSE" down 2>/dev/null || true
docker compose down "${@:2}" 2>/dev/null || true
rm -f "${HERE}/connection.env"
drop_caches
log "fabricx down"
