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
need docker
if ! out="$(docker compose down -v --remove-orphans 2>&1)"; then
  # An already-down stack is fine; a daemon that is not answering is not.
  docker info >/dev/null 2>&1 || die "docker daemon not reachable; the stack may still be running" "$out"
  warn "compose down reported: $(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi

# Stale connection.env would silently point the next run at a dead network.
rm -f "${HERE}/connection.env"
# The signing key belongs to the image that just went away.
rm -f "${HERE}/.cache/keys/ns-signing-key.pem"

drop_caches
log "fabricx down"
