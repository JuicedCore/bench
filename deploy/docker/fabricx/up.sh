#!/usr/bin/env bash
# Bring up Fabric-X for benchmarking.
#
#   bash deploy/docker/fabricx/up.sh [profile]
#
# Clones the pinned upstream sources, builds a single image serving four roles,
# starts the stack in dependency order, registers the application namespace, and
# writes connection.env for the adapter.
#
# The adapter talks gRPC: broadcast to the Arma router on :6022, finality from
# the sidecar's deliver stream on :4001.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${HERE}/../lib.sh"

need docker
need git

CACHE="${HERE}/.cache"
IMAGE="${BENCH_FX_IMAGE:-bench/fabricx:local}"
NS="${BENCH_FX_NAMESPACE:-0}"

# Pinned upstream refs. Keep these on one coherent set: mismatched committer /
# orderer versions are the recurring failure mode on this platform.
COMMITTER_REF="${COMMITTER_REF:-v1.0.5}"
ORDERER_REF="${ORDERER_REF:-v1.0.6}"

clone_pinned() {
  local url="$1" dir="$2" ref="$3"
  if [ -d "${dir}/.git" ]; then
    local have
    have="$(git -C "$dir" describe --tags --always 2>/dev/null || echo unknown)"
    if [ "$have" = "$ref" ]; then
      log "$(basename "$dir") already at ${ref}"
      return
    fi
    warn "$(basename "$dir") is at ${have}, want ${ref} - refetching"
    git -C "$dir" fetch --tags --depth 1 origin "$ref" >/dev/null 2>&1 || true
    git -C "$dir" checkout -q FETCH_HEAD 2>/dev/null && return
    rm -rf "$dir"
  fi
  log "cloning $(basename "$dir") @ ${ref}"
  git clone -q --depth 1 --branch "$ref" "$url" "$dir"
}

mkdir -p "$CACHE"
clone_pinned https://github.com/hyperledger/fabric-x-committer.git "${CACHE}/fabric-x-committer-src" "$COMMITTER_REF"
clone_pinned https://github.com/hyperledger/fabric-x-orderer.git   "${CACHE}/fabric-x-orderer-src"   "$ORDERER_REF"

# --- block-cutting parameters from the profile (adr-011) --------------------
# These must be identical across fabric-cft, fabric-bft, drunix and fabricx, or
# the normalized comparison is meaningless. They are baked at image build time
# because Arma's genesis block is generated there.
BT="$(platform_field fabricx orderer_batch.batch_timeout     || echo 1s)"
MMC="$(platform_field fabricx orderer_batch.max_message_count || echo 100)"
log "orderer batch from profile '${PROFILE}': timeout=${BT} maxMessageCount=${MMC}"

export BENCH_FX_IMAGE="$IMAGE"

log "building ${IMAGE} (first build pulls a Go toolchain and compiles Arma + committer; several minutes)"
docker build \
  --build-arg "BENCH_BATCH_TIMEOUT=${BT}" \
  --build-arg "BENCH_BATCH_MAX_MESSAGE_COUNT=${MMC}" \
  -t "$IMAGE" -f "${HERE}/image/Dockerfile" "$HERE"

cd "$HERE"
docker compose down -v >/dev/null 2>&1 || true
drop_caches

log "starting stack"
docker compose up -d

# Resource budget (equal total across platforms, split evenly). Applied straight
# away so bring-up itself runs under the real limits, not the compose placeholders.
RES_ENV="$(apply_budget fabricx '^bench-fabricx-')"

# --- readiness --------------------------------------------------------------
# Arma's routers must be listening before anything is submitted; compose's
# depends_on only orders container start, not process readiness.
wait_for_port() {
  local name="$1" port="$2" tries="${3:-90}"
  local i=0
  while [ "$i" -lt "$tries" ]; do
    if docker compose exec -T fabricx-arma sh -c "nc -z 127.0.0.1 ${port} 2>/dev/null" 2>/dev/null; then
      log "${name} listening on ${port}"
      return 0
    fi
    i=$((i+1)); sleep 1
  done
  return 1
}
wait_for_port "arma router (party 1)" 6022 || die "Arma router never came up; 'docker compose logs fabricx-arma' has the detail"

# --- namespace bootstrap ----------------------------------------------------
# Creating a namespace is a write to the `_meta` namespace, governed by the
# channel's LifecycleEndorsement policy (MAJORITY of the four Arma orgs). The
# image's loadgen.yaml declares all four identities, so `--only-namespace` signs
# with all of them and satisfies it. Idempotent: re-running against a live ledger
# reports "already exists" and carries on.
log "registering application namespace '${NS}'"
if ! docker compose exec -T fabricx-arma \
      loadgen start --config /root/config/loadgen.yaml --only-namespace 2>&1 | tail -5; then
  warn "namespace bootstrap reported an error (harmless if the namespace already exists)"
fi

# --- signing key for the adapter --------------------------------------------
# The adapter signs each transaction's namespace with the key registered as that
# namespace's policy. Export it to the host so the adapter can load it.
KEYDIR="${HERE}/.cache/keys"
mkdir -p "$KEYDIR"
docker compose exec -T fabricx-arma cat /root/artifacts/ns-signing-key.pem \
  > "${KEYDIR}/ns-signing-key.pem" 2>/dev/null || true
[ -s "${KEYDIR}/ns-signing-key.pem" ] \
  || die "could not export the namespace signing key from the image; the adapter cannot sign without it"
chmod 600 "${KEYDIR}/ns-signing-key.pem"
log "exported namespace signing key to ${KEYDIR}/ns-signing-key.pem"

cat > "${HERE}/connection.env" <<ENVEOF
# generated by deploy/docker/fabricx/up.sh  ($(date -u +%FT%TZ))
# Fabric-X speaks gRPC: broadcast to the Arma router, finality from the sidecar.
BENCH_ADAPTER_BROADCAST_ENDPOINT=localhost:6022
BENCH_ADAPTER_DELIVER_ENDPOINT=localhost:4001
BENCH_ADAPTER_CHANNEL_ID=arma
BENCH_ADAPTER_NAMESPACE=${NS}
BENCH_ADAPTER_SIGNING_KEY_PATH=${KEYDIR}/ns-signing-key.pem
BENCH_ADAPTER_METRICS_ENDPOINT=http://localhost:9643/metrics
BENCH_PLATFORM_VERSION=fabricx-committer-${COMMITTER_REF}-orderer-${ORDERER_REF}
# State DB this network actually came up on; recorded as manifest.state_db.
BENCH_ACTUAL_STATE_DB=postgres
${RES_ENV}
ENVEOF

log "fabricx up. broadcast :6022  deliver :4001  metrics :9643"
log "wrote ${HERE}/connection.env"
