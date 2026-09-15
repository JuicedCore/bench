#!/bin/bash
set -euo pipefail
cd /data
mkdir -p /data/logs
export LD_LIBRARY_PATH=/usr/local/lib:${LD_LIBRARY_PATH:-}
EPOCH_DURATION="${EPOCH_DURATION:-50}"
RUN_EPOCH="${RUN_EPOCH:-0}"
if [ -d /snapshot/small_bank ] && [ ! -d /data/small_bank ]; then
  cp -a /snapshot/small_bank /data/small_bank
  # Fresh data volume: drop any leftover raft dirs so this matches the snapshot.
  rm -rf /data/raft_data* || true
fi
if [ "${RUN_EPOCH}" = "1" ]; then
  # epoch_server's main() only scans argv for --duration and --raft_port; every
  # other flag below (block_size, receiver_port, broadcaster_port, batch_size)
  # is parsed by nothing and has no effect. The ZMQ receiver port is derived
  # from --raft_port via a fixed mapping (8100 -> 6002 here), not from
  # --receiver_port. Flags kept for upstream CLI parity, not because they do
  # anything in this build.
  /opt/neuchain/bin/epoch_server \
    --block_size 50 \
    --raft_port 8100 \
    --receiver_port 9002 \
    --broadcaster_port 9003 \
    --batch_size 100 \
    --duration "${EPOCH_DURATION}" \
    > /data/logs/epoch.log 2>&1 &
  echo $! > /data/logs/epoch.pid
fi
exec /opt/neuchain/bin/block_server_test_comm
