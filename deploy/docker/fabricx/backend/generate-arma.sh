#!/bin/sh
# Generate Arma 4-party/1-shard genesis + per-party config, all packed in one
# container. Adapted from a proven working reference (see
# docs/platforms/fabricx-comparability.md) - same armageddon invocation, same
# required patch to the orderer's own testutil sample configtx.yaml.
set -eu
ARMA_OUT="${ARMA_OUT:-/out/arma}"
DEPLOY="${DEPLOY:-/tmp/arma-deployment.yaml}"
SAMPLE="${SAMPLE:-/tmp/sampleconfig}"
ARMAGEDDON="${ARMAGEDDON:-/out/armageddon}"

# The sidecar's ValidateConfigTx requires SnapshotEndorsement +
# CheckpointEndorsement policies; the orderer's own testutil configtx.yaml
# sample only defines LifecycleEndorsement. Without this patch the sidecar
# rejects the genesis config block at startup.
python3 - "$SAMPLE/configtx.yaml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
if "SnapshotEndorsement:" not in t:
    t = t.replace(
        """        LifecycleEndorsement:
            Type: ImplicitMeta
            Rule: "MAJORITY Endorsement"
        Endorsement:""",
        """        LifecycleEndorsement:
            Type: ImplicitMeta
            Rule: "MAJORITY Endorsement"
        SnapshotEndorsement:
            Type: ImplicitMeta
            Rule: "MAJORITY Endorsement"
        CheckpointEndorsement:
            Type: ImplicitMeta
            Rule: "MAJORITY Endorsement"
        Endorsement:""",
        1,
    )
    p.write_text(t)
PY

"$ARMAGEDDON" generate --config "$DEPLOY" --output "$ARMA_OUT" --sampleConfigPath "$SAMPLE"

yaml="$ARMA_OUT/bootstrap/shared_config.yaml"
sed -i 's/BatchCreationTimeout: 500ms/BatchCreationTimeout: 50ms/' "$yaml"
sed -i 's/MaxMessageCount: 10000/MaxMessageCount: 50/' "$yaml"
sed -i 's/RequestBatchMaxInterval: 200ms/RequestBatchMaxInterval: 50ms/' "$yaml"

"$ARMAGEDDON" createSharedConfigProto --sharedConfigYaml "$yaml" --output "$ARMA_OUT/bootstrap"
"$ARMAGEDDON" createBlock --sharedConfigYaml "$yaml" --blockOutput "$ARMA_OUT/bootstrap" \
  --baseDir "$ARMA_OUT" --sampleConfigPath "$SAMPLE"

# All 4 parties run in this one container - rebind each party's ledger/store
# path under /data/arma and bind every listener to 0.0.0.0 (the per-party
# fixed IPs in arma-deployment.yaml are only used to generate the genesis
# block; the actual container has one loopback for all of them).
for i in 1 2 3 4; do
  d="$ARMA_OUT/config/party${i}"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/router|g" "$d/local_config_router.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/assembler|g" "$d/local_config_assembler.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/batcher|g" "$d/local_config_batcher1.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/consenter|g" "$d/local_config_consenter.yaml"
  for f in local_config_router.yaml local_config_assembler.yaml local_config_batcher1.yaml local_config_consenter.yaml; do
    sed -i "/^General:/,/^FileStore:/ s/ListenAddress:.*/ListenAddress: 0.0.0.0/" "$d/$f"
  done
done
