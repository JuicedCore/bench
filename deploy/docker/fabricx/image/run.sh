#!/bin/sh
# Role supervisor adapted from fabric-x-committer docker/images/test_node/run.
# Lab default is TLS none (private Docker bridge).
set -eu

insecure_tls() {
  export SC_COORDINATOR_SERVER_TLS_MODE="none"
  export SC_COORDINATOR_VERIFIER_TLS_MODE="none"
  export SC_COORDINATOR_VALIDATOR_COMMITTER_TLS_MODE="none"
  export SC_COORDINATOR_MONITORING_TLS_MODE="none"
  export SC_QUERY_SERVER_TLS_MODE="none"
  export SC_QUERY_MONITORING_TLS_MODE="none"
  export SC_SIDECAR_SERVER_TLS_MODE="none"
  export SC_SIDECAR_MONITORING_TLS_MODE="none"
  export SC_SIDECAR_COMMITTER_TLS_MODE="none"
  export SC_SIDECAR_ORDERER_TLS_MODE="none"
  export SC_VC_SERVER_TLS_MODE="none"
  export SC_VC_MONITORING_TLS_MODE="none"
  export SC_VERIFIER_SERVER_TLS_MODE="none"
  export SC_VERIFIER_MONITORING_TLS_MODE="none"
  export SC_ORDERER_SERVER_TLS_MODE="none"
  export SC_LOADGEN_SERVER_TLS_MODE="none"
  export SC_LOADGEN_MONITORING_TLS_MODE="none"
  export SC_LOADGEN_ORDERER_CLIENT_SIDECAR_CLIENT_TLS_MODE="none"
  export SC_LOADGEN_ORDERER_CLIENT_ORDERER_TLS_MODE="none"
}

insecure_tls

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
