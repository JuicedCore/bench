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

wait
