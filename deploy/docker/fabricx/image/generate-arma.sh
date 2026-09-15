#!/bin/sh
# Generate Arma 4-party / 1-shard crypto + config, then apply the benchmark
# profile's block-cutting parameters.
#
# Adapted from the working reference deployment at
# Projects/NeuChain/harness/fabric-x/docker/generate-arma.sh. The substantive
# change: that one hardcoded 50ms/50-tx blocks to match NeuChain, whereas this
# takes the values from the profile's shared orderer_batch anchor so every
# platform in the comparison cuts blocks the same way.
set -eu
ARMA_OUT="${ARMA_OUT:-/out/arma}"
DEPLOY="${DEPLOY:-/tmp/arma-deployment.yaml}"
SAMPLE="${SAMPLE:-/tmp/sampleconfig}"
ARMAGEDDON="${ARMAGEDDON:-/out/armageddon}"

# Sidecar ValidateConfigTx requires SnapshotEndorsement + CheckpointEndorsement.
# Arma's testutil configtx.yaml only has LifecycleEndorsement.
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

# Block-cutting parameters come from the benchmark profile's shared orderer_batch
# anchor (deploy/profiles/*.yaml), NOT from a value tuned to suit this platform.
# They must be identical across fabric-cft, fabric-bft, drunix and fabricx or the
# normalized comparison is meaningless - see adr-011 and
# docs/architecture/fairness-guarantees.md. up.sh passes them in.
: "${BENCH_BATCH_TIMEOUT:?generate-arma.sh needs BENCH_BATCH_TIMEOUT}"
: "${BENCH_BATCH_MAX_MESSAGE_COUNT:?generate-arma.sh needs BENCH_BATCH_MAX_MESSAGE_COUNT}"
echo "applying profile orderer_batch: timeout=${BENCH_BATCH_TIMEOUT} maxMessageCount=${BENCH_BATCH_MAX_MESSAGE_COUNT}"
# Replace whatever value armageddon wrote rather than a hardcoded upstream
# default, so a changed default cannot leave Arma on upstream's value while the
# manifest claims the profile's. Each key must appear exactly once and must hold
# the profile value afterwards.
pin() { # pin <key> <value>
  n="$(grep -cE "^[[:space:]]*$1:" "$yaml" || true)"
  [ "$n" = 1 ] || {
    echo "ERROR: generate-arma.sh: expected exactly one '$1:' in $yaml, found ${n} - armageddon's config layout changed; update this script (adr-011)" >&2
    grep -n "$1" "$yaml" >&2 || true
    exit 1
  }
  sed -i -E "s/^([[:space:]]*$1:).*/\1 $2/" "$yaml"
  grep -qE "^[[:space:]]*$1: $2\$" "$yaml" || { echo "ERROR: generate-arma.sh: failed to set $1 to $2 in $yaml" >&2; exit 1; }
  echo "  $1 = $2"
}
pin BatchCreationTimeout "${BENCH_BATCH_TIMEOUT}"
pin MaxMessageCount "${BENCH_BATCH_MAX_MESSAGE_COUNT}"
# Consensus.BFTConfig.requestbatchmaxinterval is deliberately NOT set from the
# profile. The batcher above is Arma's block cutter: its batches are the blocks,
# and they are what adr-011 equalises with Fabric's BatchTimeout/MaxMessageCount.
# SmartBFT's interval is a second, separate layer that batches batch
# *attestations* into consensus proposals; classic Fabric's Raft orderer has no
# counterpart, so pinning it to the block timeout equalises nothing and only adds
# a second wait on top of the batcher's. It stays at the upstream default.

"$ARMAGEDDON" createSharedConfigProto --sharedConfigYaml "$yaml" --output "$ARMA_OUT/bootstrap"
"$ARMAGEDDON" createBlock --sharedConfigYaml "$yaml" --blockOutput "$ARMA_OUT/bootstrap" \
  --baseDir "$ARMA_OUT" --sampleConfigPath "$SAMPLE"

# Persist under the node0 volume; each role gets its own dir.
for i in 1 2 3 4; do
  d="$ARMA_OUT/config/party${i}"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/router|g" "$d/local_config_router.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/assembler|g" "$d/local_config_assembler.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/batcher|g" "$d/local_config_batcher1.yaml"
  sed -i "s|/var/dec-trust/production/orderer/store|/data/arma/party${i}/consenter|g" "$d/local_config_consenter.yaml"
  # Bind all interfaces so 172.29.0.10 does not have to exist at image-build time.
  for f in local_config_router.yaml local_config_assembler.yaml local_config_batcher1.yaml local_config_consenter.yaml; do
    sed -i "/^General:/,/^FileStore:/ s/ListenAddress:.*/ListenAddress: 0.0.0.0/" "$d/$f"
  done
done
