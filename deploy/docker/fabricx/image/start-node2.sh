#!/bin/sh
# fx-node2: sidecar + coordinator + verifier (same 2918 MiB box).
set -eu
BINS="${BINS_PATH:-/root/bin}"
CONFIG="${CONFIGS_PATH:-/root/config}"
C="${BINS}/committer"
"$C" start sidecar -c "${CONFIG}/sidecar.yaml" &
"$C" start verifier -c "${CONFIG}/verifier.yaml" &
exec "$C" start coordinator -c "${CONFIG}/coordinator.yaml"
