#!/bin/sh
# fx-node2: sidecar + coordinator + verifier (same 2918 MiB box).
set -eu
BINS="${BINS_PATH:-/root/bin}"
CONFIG="${CONFIGS_PATH:-/root/config}"
C="${BINS}/committer"
# sidecar and verifier run in the background; if either dies the container must
# stop too, or the coordinator keeps it "running" with no deliver/verify path.
"$C" start sidecar -c "${CONFIG}/sidecar.yaml" &
SIDECAR=$!
"$C" start verifier -c "${CONFIG}/verifier.yaml" &
VERIFIER=$!
"$C" start coordinator -c "${CONFIG}/coordinator.yaml" &
COORD=$!
trap 'kill $SIDECAR $VERIFIER $COORD 2>/dev/null || true' TERM INT
while :; do
  for e in "$SIDECAR:sidecar" "$VERIFIER:verifier" "$COORD:coordinator"; do
    pid="${e%%:*}"
    if ! kill -0 "$pid" 2>/dev/null; then
      rc=0; wait "$pid" || rc=$?
      echo "ERROR: committer ${e#*:} (pid ${pid}) exited with status ${rc}; stopping the container" >&2
      kill $SIDECAR $VERIFIER $COORD 2>/dev/null || true
      exit "${rc:-1}"
    fi
  done
  sleep 2
done
