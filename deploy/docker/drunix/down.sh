#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for cand in "${BENCH_DRUNIX_REPO:-}" "${HERE}/.cache/drunix"; do
  NET="${cand}/drunix-network/test-network"
  if [ -d "$NET" ]; then
    cd "$NET" && ./network.sh down || true
    # restore the LevelDB patch if we made one
    [ -f "${NET}/compose/compose-test-net.yaml.bench.bak" ] && \
      mv "${NET}/compose/compose-test-net.yaml.bench.bak" "${NET}/compose/compose-test-net.yaml"
    break
  fi
done
rm -f "${HERE}/connection.env"
drop_caches
log "drunix down"
