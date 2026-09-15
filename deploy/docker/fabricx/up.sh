#!/usr/bin/env bash
# Bring up Fabric-X for benchmarking.
#
#   bash deploy/docker/fabricx/up.sh [profile]
#
# Clones the pinned upstream sources, builds a single image serving four roles,
# starts the stack in dependency order, registers the application namespace, and
# writes connection.env for the adapter.
#
# The adapter talks gRPC: broadcast to all four Arma routers (:6022, :6122,
# :6222, :6322), finality from the sidecar's deliver stream on :4001.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${HERE}/../lib.sh"

need docker
need git
on_error_dump '^bench-fabricx-'

CACHE="${HERE}/.cache"
IMAGE="${BENCH_FX_IMAGE:-bench/fabricx:local}"
NS="${BENCH_FX_NAMESPACE:-0}"

# Pinned upstream refs. Keep these on one coherent set: mismatched committer /
# orderer versions are the recurring failure mode on this platform.
COMMITTER_REF="${COMMITTER_REF:-v1.0.5}"
ORDERER_REF="${ORDERER_REF:-v1.0.6}"

mkdir -p "$CACHE"
git_checkout_pinned https://github.com/hyperledger/fabric-x-committer.git "${CACHE}/fabric-x-committer-src" "$COMMITTER_REF"
git_checkout_pinned https://github.com/hyperledger/fabric-x-orderer.git   "${CACHE}/fabric-x-orderer-src"   "$ORDERER_REF"

# --- block-cutting parameters from the profile (adr-011) --------------------
# These must be identical across fabric-cft, fabric-bft, drunix and fabricx, or
# the normalized comparison is meaningless. They are baked at image build time
# because Arma's genesis block is generated there.
BT="$(require_platform_field fabricx orderer_batch.batch_timeout)"
MMC="$(require_platform_field fabricx orderer_batch.max_message_count)"
log "orderer batch from profile '${PROFILE}': timeout=${BT} maxMessageCount=${MMC}"

export BENCH_FX_IMAGE="$IMAGE"

log "building ${IMAGE} (first build pulls a Go toolchain and compiles Arma + committer; several minutes)"
docker build \
  --build-arg "BENCH_BATCH_TIMEOUT=${BT}" \
  --build-arg "BENCH_BATCH_MAX_MESSAGE_COUNT=${MMC}" \
  -t "$IMAGE" -f "${HERE}/image/Dockerfile" "$HERE" \
  || die "fabricx image build failed" "the failing Dockerfile step is above; out of disk (docker system df) and Go module download failures are the usual causes"

cd "$HERE" || exit 1
docker compose down -v >/dev/null 2>&1 || warn "compose down of a previous stack failed; continuing (up will report conflicts)"
drop_caches

log "starting stack"
docker compose up -d || { compose_dump 60; die "docker compose up failed for fabricx"; }
# A container that died on start makes apply_budget's "no running containers"
# message misleading, so check first.
sleep 2
if docker compose ps -a --status exited --format '{{.Name}}' 2>/dev/null | grep -q .; then
  compose_dump 80
  die "fabricx container(s) exited right after start: $(docker compose ps -a --status exited --format '{{.Name}}' | tr '\n' ' ')"
fi

# Resource budget (equal total across platforms, split evenly). Applied straight
# away so bring-up itself runs under the real limits, not the compose placeholders.
RES_ENV="$(apply_budget fabricx '^bench-fabricx-')"

# --- readiness --------------------------------------------------------------
# Arma's routers must be listening before anything is submitted; compose's
# depends_on only orders container start, not process readiness. Wait on both
# ports the adapter uses; on timeout, show why.
WAIT_FOR_ON_TIMEOUT=compose_dump
for port in 6022 6122 6222 6322; do
  wait_for "Arma router on :${port}" 120 port_open 127.0.0.1 "$port" \
    || die "Arma router on :${port} never came up" "compose status and logs are above; 'docker compose logs fabricx-arma' for the full log"
done
wait_for "sidecar deliver on :4001" 120 port_open 127.0.0.1 4001 \
  || die "sidecar deliver endpoint never came up" "'docker compose logs fabricx-pipeline' has the detail"
unset WAIT_FOR_ON_TIMEOUT

# --- namespace bootstrap ----------------------------------------------------
# Creating a namespace is a write to the `_meta` namespace, governed by the
# channel's LifecycleEndorsement policy (MAJORITY of the four Arma orgs). The
# image's loadgen.yaml declares all four identities, so `--only-namespace` signs
# with all of them and satisfies it.
#
# loadgen's exit status says nothing about whether that worked: in committer
# v1.0.5 the delivery receiver always returns context.Canceled when the workload
# ends (loadgen/adapters/sidecar_receiver.go), so a successful run exits 1 with
# "receiver done: context canceled", and a re-run against a live ledger is an
# ABORTED_MVCC_CONFLICT that exits the same way. Success is instead read from the
# committed state: the namespace's row in the _meta table must exist and hold
# the verification key the adapter's signing key pairs with.
log "registering application namespace '${NS}'"
NS_OUT="$(docker compose exec -T fabricx-arma \
      timeout 300 loadgen start --config /root/config/loadgen.yaml --only-namespace 2>&1)" && NS_RC=0 || NS_RC=$?
NS_POLICY_HEX="$(docker compose exec -T fabricx-db psql -U postgres -tAc \
      "select encode(value, 'hex') from ns__meta where key = convert_to('${NS}', 'UTF8')" 2>/dev/null | tr -d '[:space:]')"
NS_KEY_HEX="$(docker compose exec -T fabricx-arma \
      sh -c 'od -An -tx1 /root/artifacts/ns-verification-key.pem | tr -d " \n"' 2>/dev/null)"
if [ -z "$NS_POLICY_HEX" ]; then
  printf '%s\n' "$NS_OUT" | sed 's/\x1b\[[0-9;]*m//g' | tail -25 >&2
  die "namespace bootstrap failed: no '${NS}' row in ns__meta after loadgen (loadgen exit ${NS_RC}); every transaction would be rejected without it" \
      "loadgen output is above; rerun: docker compose exec -T fabricx-arma loadgen start --config /root/config/loadgen.yaml --only-namespace" \
      "see docs/platforms/fabricx-integration.md#why-namespace-bootstrap-failed"
fi
if [ -z "$NS_KEY_HEX" ] || [ "${NS_POLICY_HEX#*"$NS_KEY_HEX"}" = "$NS_POLICY_HEX" ]; then
  die "namespace '${NS}' exists but its policy does not hold /root/artifacts/ns-verification-key.pem; the adapter's signatures would all be rejected" \
      "the ledger predates this image's key: bash deploy/docker/fabricx/down.sh (removes volumes), then up.sh again"
fi
log "namespace '${NS}' committed with the image's ECDSA verification key"

# --- signing key for the adapter --------------------------------------------
# The adapter signs each transaction's namespace with the key registered as that
# namespace's policy. Export it to the host so the adapter can load it.
KEYDIR="${HERE}/.cache/keys"
mkdir -p "$KEYDIR"
docker compose exec -T fabricx-arma cat /root/artifacts/ns-signing-key.pem \
  > "${KEYDIR}/ns-signing-key.pem" || warn "reading /root/artifacts/ns-signing-key.pem from fabricx-arma failed"
[ -s "${KEYDIR}/ns-signing-key.pem" ] \
  || die "could not export the namespace signing key from the image; the adapter cannot sign without it"
chmod 600 "${KEYDIR}/ns-signing-key.pem"
log "exported namespace signing key to ${KEYDIR}/ns-signing-key.pem"

cat > "${HERE}/connection.env" <<ENVEOF
# generated by deploy/docker/fabricx/up.sh  ($(date -u +%FT%TZ))
# Fabric-X speaks gRPC: broadcast to the Arma router, finality from the sidecar.
# Every party's router: the adapter broadcasts each envelope to all of them.
BENCH_ADAPTER_BROADCAST_ENDPOINT=localhost:6022,localhost:6122,localhost:6222,localhost:6322
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

log "fabricx up. broadcast :6022,:6122,:6222,:6322  deliver :4001  metrics :9643"
log "wrote ${HERE}/connection.env"
