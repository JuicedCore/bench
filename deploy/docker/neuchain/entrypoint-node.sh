#!/bin/bash
# One NeuChain node: epoch server (background) + block server (foreground).
# Mounted over the image's own entrypoint so the logs land where `docker logs`
# and compose_dump can see them.
set -euo pipefail
cd /data
export LD_LIBRARY_PATH=/usr/local/lib:${LD_LIBRARY_PATH:-}

if [ "${RUN_EPOCH:-0}" = "1" ]; then
  # epoch_server's main() reads only --duration and --raft_port; the ZMQ
  # receiver port is derived from --raft_port (8100 -> 6002).
  /opt/neuchain/bin/epoch_server --raft_port 8100 --duration "${EPOCH_DURATION:-50}" 2>&1 \
    | sed -u 's/^/[epoch] /' &
fi
exec /opt/neuchain/bin/block_server_test_comm
