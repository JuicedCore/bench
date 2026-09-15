#!/bin/sh
# Role supervisor adapted from fabric-x-committer docker/images/test_node/run.
# TLS modes (all none) come from deploy/docker/fabricx/endpoints.env so that
# processes started with `docker compose exec` get them too.
set -eu

[ "${SC_LOADGEN_ORDERER_CLIENT_ORDERER_TLS_MODE:-}" = none ] || {
  echo "run.sh: SC_*_TLS_MODE not set; start this image through deploy/docker/fabricx (env_file endpoints.env)" >&2
  exit 1
}

BINS="${BINS_PATH:-/root/bin}"
CONFIG="${CONFIGS_PATH:-/root/config}"

case "${1:-}" in
  db|endorser)
    export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
    exec docker-entrypoint.sh postgres
    ;;
  orderer)
    exec /opt/fabric-x/start-arma.sh
    ;;
  coordinator|pipeline)
    exec /opt/fabric-x/start-node2.sh
    ;;
  committer)
    exec /opt/fabric-x/start-node3.sh
    ;;
  loadgen)
    exec "${BINS}/loadgen" start --config "${CONFIG}/loadgen.yaml"
    ;;
  smallbank)
    shift
    exec "${BINS}/smallbank" "$@"
    ;;
  *)
    echo "usage: db|orderer|pipeline|committer|loadgen|smallbank" >&2
    exit 1
    ;;
esac
