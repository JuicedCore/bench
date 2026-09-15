#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1
need docker
log "stopping monitoring stack"
docker compose down "${@:2}"
