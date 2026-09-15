#!/bin/sh
# fx-node3: init-db, then query + validator-committer. Postgres lives on fx-node1.
set -eu
BINS="${BINS_PATH:-/root/bin}"
CONFIG="${CONFIGS_PATH:-/root/config}"
C="${BINS}/committer"

i=0
ok=0
while [ "$i" -lt 40 ]; do
  if "$C" init-db --config "${CONFIG}/vc.yaml" --timeout 20s; then
    ok=1
    break
  fi
  i=$((i + 1))
  echo "init-db: waiting for postgres on fx-node1 (attempt $i/40)" >&2
  sleep 2
done
if [ "$ok" != 1 ]; then
  echo "ERROR: init-db failed 40 times; postgres (fabricx-db) is not reachable or rejected the schema - see 'docker compose logs fabricx-db'" >&2
  exit 1
fi

"$C" start query -c "${CONFIG}/query.yaml" &
exec "$C" start vc -c "${CONFIG}/vc.yaml"
