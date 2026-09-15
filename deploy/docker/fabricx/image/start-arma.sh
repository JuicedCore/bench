#!/bin/sh
# fx-node0: 4-party / 1-shard Arma (router, batcher, consenter, assembler × 4).
set -eu
ARMA_DIR="${ARMA_DIR:-/root/arma}"
BINS="${BINS_PATH:-/root/bin}"

mkdir -p /data/arma
for i in 1 2 3 4; do
  mkdir -p "/data/arma/party${i}/router" "/data/arma/party${i}/assembler" \
    "/data/arma/party${i}/batcher" "/data/arma/party${i}/consenter"
done
echo "Starting Arma 4-party 1-shard (TLS none on router/assembler)"

# Record every child's PID and role. A plain `wait` would keep the container
# "running" after one of the 16 processes died, leaving a half-broken ordering
# service that only shows up as failed transactions.
PIDS=""
start() { # start <role> <party> <args...>
  role="$1"; party="$2"; shift 2
  "${BINS}/arma" "$@" &
  PIDS="$PIDS $!:${role}-party${party}"
}
for i in 1 2 3 4; do start consenter "$i" consensus --config "${ARMA_DIR}/config/party${i}/local_config_consenter.yaml"; done
sleep 2
for i in 1 2 3 4; do start batcher "$i" batcher --config "${ARMA_DIR}/config/party${i}/local_config_batcher1.yaml"; done
sleep 2
for i in 1 2 3 4; do start assembler "$i" assembler --config "${ARMA_DIR}/config/party${i}/local_config_assembler.yaml"; done
sleep 2
for i in 1 2 3 4; do start router "$i" router --config "${ARMA_DIR}/config/party${i}/local_config_router.yaml"; done

trap 'for e in $PIDS; do kill "${e%%:*}" 2>/dev/null || true; done' TERM INT

# Exit (and so stop the container, visibly) as soon as any process is gone.
while :; do
  for e in $PIDS; do
    pid="${e%%:*}"
    if ! kill -0 "$pid" 2>/dev/null; then
      rc=0; wait "$pid" || rc=$?
      echo "ERROR: arma ${e#*:} (pid ${pid}) exited with status ${rc}; stopping the container so the failure is visible" >&2
      for o in $PIDS; do kill "${o%%:*}" 2>/dev/null || true; done
      exit "${rc:-1}"
    fi
  done
  sleep 2
done
