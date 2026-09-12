#!/bin/sh
# Single-container Fabric-X backend: 4-party/1-shard Arma + all 5 committer
# services + embedded Postgres. Replaces fabric-x-samples' Ansible-deployed
# orderer+committer (version-skewed against tokens/'s go.mod - see
# docs/platforms/fabricx-comparability.md). Adapted from a proven working
# reference: same role split, same insecure-everywhere TLS posture (matches
# tokens/'s endorser core.yaml: tls.enabled: false).
set -eu

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

BINS="${BINS_PATH:-/root/bin}"
CONFIG="${CONFIGS_PATH:-/root/config}"
ARMA_DIR="${ARMA_DIR:-/root/arma}"
C="${BINS}/committer"

# --- Postgres (embedded, matches fabric-x-committer's own test-node image) ---
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
docker-entrypoint.sh postgres -p 5433 &

i=0
until pg_isready -h 127.0.0.1 -p 5433 -U postgres >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    echo "postgres never became ready" >&2
    exit 1
  fi
  sleep 1
done
"$C" init-db --config "${CONFIG}/vc.yaml"

# --- Arma: 4-party 1-shard, all roles in this container ---
mkdir -p /data/arma
for i in 1 2 3 4; do
  mkdir -p "/data/arma/party${i}/router" "/data/arma/party${i}/assembler" \
    "/data/arma/party${i}/batcher" "/data/arma/party${i}/consenter"
done
echo "Starting Arma 4-party 1-shard (TLS none on router/assembler)"
for i in 1 2 3 4; do
  "${BINS}/arma" consensus --config "${ARMA_DIR}/config/party${i}/local_config_consenter.yaml" &
done
sleep 2
for i in 1 2 3 4; do
  "${BINS}/arma" batcher --config "${ARMA_DIR}/config/party${i}/local_config_batcher1.yaml" &
done
sleep 2
for i in 1 2 3 4; do
  "${BINS}/arma" assembler --config "${ARMA_DIR}/config/party${i}/local_config_assembler.yaml" &
done
sleep 2
for i in 1 2 3 4; do
  "${BINS}/arma" router --config "${ARMA_DIR}/config/party${i}/local_config_router.yaml" &
done
sleep 2

# --- Committer: sidecar (client-facing :4001), coordinator, verifier, query
# (client-facing :7001), vc ---
"$C" start sidecar     -c "${CONFIG}/sidecar.yaml" &
"$C" start verifier    -c "${CONFIG}/verifier.yaml" &
"$C" start coordinator -c "${CONFIG}/coordinator.yaml" &
"$C" start query       -c "${CONFIG}/query.yaml" &
"$C" start vc          -c "${CONFIG}/vc.yaml" &

wait
