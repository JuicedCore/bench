#!/usr/bin/env bash
# Tear down the Fabric-X stack.
#
#   bash deploy/docker/fabricx/down.sh [profile]
#
# Tolerates an already-down stack. Leaves .cache/ (cloned sources, built image)
# alone - `make clean` handles those.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${HERE}/../lib.sh"

cd "$HERE" || exit 1
# -v: Postgres and the ledger must start empty for the next run, or throughput is
# measured against a pre-populated state store (adr-005, inter-run isolation).
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

# Stale connection.env would silently point the next run at a dead network.
rm -f "${HERE}/connection.env"
# The signing key belongs to the image that just went away.
rm -f "${HERE}/.cache/keys/ns-signing-key.pem"

drop_caches
log "fabricx down"
