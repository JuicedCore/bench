#!/usr/bin/env bash
# Bring up Fabric-X for benchmarking: the fabric-x-samples "tokens" stack
# (devnet Arma+committer + issuer/endorser/owner services) plus the custom
# kv-write view service. Emits connection.env for the fabricx adapter.
#
#   ./up.sh [profile]
#
# Verified against hyperledger/fabric-x-samples @ main:
#   tokens/  `make setup && make start`  ->  devnet + token services
#   ports: issuer :9100  endorser1 :9300  owner1 :9500  owner2 :9600
#   custom kvview: :9700
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
need docker; need git; need make; need go
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

FXS_REF="${FXS_REF:-main}"
CACHE="${HERE}/.cache"
FXS="${CACHE}/fabric-x-samples"
mkdir -p "$CACHE"

# Re-clone whenever FXS_REF doesn't match what's actually checked out - a bare
# `[ -d "$FXS/.git" ] || git clone` only ever clones once, so on a machine with
# a cached checkout, changing FXS_REF silently kept whatever ref was cloned
# first (and BENCH_PLATFORM_VERSION below would then record a ref that was
# never actually deployed).
current_ref=""
if [ -d "$FXS/.git" ]; then
  current_ref="$(git -C "$FXS" describe --tags --exact-match 2>/dev/null \
    || git -C "$FXS" rev-parse --abbrev-ref HEAD 2>/dev/null)"
fi
if [ "$current_ref" != "$FXS_REF" ]; then
  rm -rf "$FXS"
  git clone --depth 1 -b "$FXS_REF" \
    https://github.com/hyperledger/fabric-x-samples.git "$FXS"
fi

# tokens/ansible/inventory/ ships only fabric-x.yaml + group_vars/all/env.yaml -
# it never copies in the collection's own examples/inventory/vars.yaml, which
# defines a base-paths chain (project_dir -> out_dir -> control_node_dir ->
# config_build_dir -> {cryptogen,configtxgen,armageddon}_artifacts_dir) plus
# channel_id/actual_host that dozens of role files reference unconditionally.
# Without it every setup-fabric run fails: 'config_build_dir' is undefined.
# (Confirmed by isolation: removing an unrelated override here reproduced the
# identical failure, and grepping the whole ansible/ tree finds these vars
# defined nowhere except that one uncopied example file.) project_dir here
# uses the same PROJECT_DIR env var fabricx_ansible.mk already exports
# (= this tokens/ checkout), so out_dir lands at tokens/out - the same
# directory the Makefile's own clean-fabric target and this repo's up.sh/down.sh
# already treat as the deployment's scratch dir.
#
# "Lever B" was tried and REVERTED - see docs/platforms/fabricx-comparability.md.
# The samples repo's own Ansible role (hyperledger.fabricx collection,
# tokens/ansible/requirements.yml) pins fabric-x-committer:0.1.7, older than
# what tokens/go.mod links against (v1.0.4) - the deployed committer doesn't
# register the gRPC service the built endorser app calls
# (committerpb.QueryService), so endorser/init fails Unimplemented. Overriding
# committer_image_tag to 1.0.4 does NOT fix this: the 1.0.4 binary's CLI is
# restructured, not just its config schema - every committer container
# (coordinator/sidecar/validator/verifier/query-service) crash-loops with
# `Error: unknown command "committer" for "Committer"`. That's strictly worse
# than the 0.1.7 baseline (which at least runs, just fails the one RPC), so
# the tag override is NOT applied. Left as a documented dead end rather than
# silently forgotten.
#
# The base-paths fix above IS written every run so a fresh clone on another
# machine picks it up automatically; not reverted by down.sh since it's a
# deliberate persistent fix, not a temporary patch.
GROUP_VARS="${FXS}/tokens/ansible/inventory/group_vars/all"
mkdir -p "$GROUP_VARS"
cat > "${GROUP_VARS}/vars.yaml" <<'EOF'
# Written by deploy/docker/fabricx/up.sh - see docs/platforms/fabricx-comparability.md.

# Base-paths chain the collection's roles require but tokens/'s own inventory
# never supplies (see up.sh for the full explanation). Mirrors
# examples/inventory/vars.yaml, adapted to this checkout's PROJECT_DIR.
project_dir: "{{ lookup('env', 'PROJECT_DIR') }}"
out_dir: "{{ project_dir }}/out"
control_node_dir: "{{ out_dir }}/control-node"
source_code_dir: "{{ control_node_dir }}/code"
cli_bin_dir: "{{ control_node_dir }}/cli"
fetched_artifacts_dir: "{{ control_node_dir }}/fetched"
config_build_dir: "{{ control_node_dir }}/config"
cryptogen_artifacts_dir: "{{ config_build_dir }}/cryptogen-artifacts"
armageddon_artifacts_dir: "{{ config_build_dir }}/armageddon-artifacts"
configtxgen_artifacts_dir: "{{ config_build_dir }}/configtxgen-artifacts"
channel_id: arma
actual_host: "{{ 'localhost' if ansible_connection == 'local' else ansible_host }}"

# committer_image_tag deliberately NOT overridden here - see up.sh comment
# above ("Lever B" tried and reverted: 1.0.4's CLI is incompatible with this
# collection's templates, not just its config schema).
EOF

echo "== fabric-x-samples tokens: make setup && make start (slow: builds images) =="
( cd "$FXS/tokens" && make setup && make start )

# Replace the Ansible-deployed orderer+committer with a self-built backend
# (Arma 4-party/1-shard + all 5 committer services, one container) - see
# docs/platforms/fabricx-comparability.md. The Ansible role's committer is
# pinned at fabric-x-committer:0.1.7, which predates the committerpb.QueryService
# RPC tokens/'s go.mod-pinned client expects (confirmed: endorser/init fails
# Unimplemented). Building committer+orderer together from one consistent
# source snapshot (adapted from a proven working reference) avoids that skew
# entirely. tokens/'s REST app (issuer/owner/endorser) is unchanged - its
# core.yaml already points at committer-sidecar:4001/committer-query-service:7001
# with tls.enabled: false, so this backend just needs to answer to those two
# names on the same "fabric_test" network with TLS off, which it does.
echo "== stop the Ansible-deployed orderer+committer (replaced by a self-built backend) =="
docker rm -f \
  orderer-assembler-1 orderer-assembler-2 orderer-assembler-3 orderer-assembler-4 \
  orderer-batcher-1 orderer-batcher-2 orderer-batcher-3 orderer-batcher-4 \
  orderer-consenter-1 orderer-consenter-2 orderer-consenter-3 orderer-consenter-4 \
  orderer-router-1 orderer-router-2 orderer-router-3 orderer-router-4 \
  committer-coordinator committer-sidecar committer-query-service \
  committer-validator committer-verifier committer-db \
  >/dev/null 2>&1 || true

echo "== build + start the self-built Fabric-X backend =="
docker build -t bench/fabricx-backend:latest \
  --build-arg COMMITTER_SRC=.cache/fabric-x-committer-src \
  --build-arg ORDERER_SRC=.cache/fabric-x-orderer-src \
  -f "$HERE/backend/Dockerfile" "$HERE"
docker rm -f fabricx-backend >/dev/null 2>&1 || true
docker run -d --name fabricx-backend \
  --network fabric_test \
  --network-alias committer-sidecar \
  --network-alias committer-query-service \
  bench/fabricx-backend:latest

attempt=1; max_attempts=60
until docker logs fabricx-backend 2>&1 | grep -qF "Serving gRPC on [::]:4001"; do
  [ "$attempt" -ge "$max_attempts" ] && die "fabricx-backend sidecar never came up - check: docker logs fabricx-backend"
  attempt=$((attempt + 1)); sleep 2
done
log "fabricx-backend up (sidecar :4001, query-service :7001)"

# KNOWN BLOCKER: no namespace exists yet on this fresh backend, and creating
# one isn't automated here - endorser/init below WILL fail with
# `relation "ns_token_namespace" does not exist` every time until that's
# fixed. The version-skew bug this backend was built to fix (see
# docs/platforms/fabricx-comparability.md) is confirmed resolved; namespace
# bootstrap against this from-scratch genesis is the next open problem -
# full repro trail, exact commands/errors tried, and where to look in the
# fabric-x-committer/fabric-x-orderer source are all in that doc's
# "Lever C" section. Don't re-derive the investigation - start from its
# "Where to look next" pointers.
echo "== initialise the token network (endorser init) =="
# Mirror tokens/scripts/test.sh's own init_fabricx(): the endorser's /readyz
# only reports its HTTP server is up, not that the ~20-container Fabric-X
# network has converged enough to accept the setup transaction, so wait for
# readiness first, then retry with a real backoff - and show the actual
# response body on failure instead of the old `curl -sf` swallowing it.
attempt=1; max_attempts=30
until curl -fsS "http://localhost:9300/readyz" >/dev/null 2>&1; do
  [ "$attempt" -ge "$max_attempts" ] && die "endorser :9300/readyz never became ready"
  attempt=$((attempt + 1)); sleep 2
done

attempt=1; max_attempts=30
while true; do
  body="$(curl -sS -X POST -w '\n%{http_code}' http://localhost:9300/endorser/init)" || body=$'connection failed\n000'
  status="${body##*$'\n'}"
  resp="${body%$'\n'*}"
  [ "$status" = "200" ] && break
  if [ "$attempt" -ge "$max_attempts" ]; then
    die "endorser/init failed after ${max_attempts} attempts (HTTP ${status}): ${resp}"
  fi
  warn "endorser/init attempt ${attempt}/${max_attempts} failed (HTTP ${status}): ${resp}"
  attempt=$((attempt + 1)); sleep 5
done

echo "== build + start the custom kv-write view service =="
docker build -t bench/fabricx-rest:latest "$HERE/kvview"
export FXS_DEVNET_CONF="$FXS/devnet/config"
drop_caches
docker compose up -d

# quick liveness
curl -sf http://localhost:9700/healthz >/dev/null && log "kvview :9700 healthy" \
  || warn "kvview :9700 not healthy yet"

cat > "${HERE}/connection.env" <<EOF
# generated by deploy/docker/fabricx/up.sh  ($(date -u +%FT%TZ))
BENCH_ADAPTER_OWNER_URL=http://localhost:9500
BENCH_ADAPTER_ISSUER_URL=http://localhost:9100
BENCH_ADAPTER_KV_URL=http://localhost:9700
BENCH_ADAPTER_SENDER_ACCOUNT=alice
BENCH_ADAPTER_COUNTERPARTY_NODE=owner2
BENCH_ADAPTER_TOKEN_CODE=EURX
BENCH_ADAPTER_METRICS_ENDPOINT=http://localhost:9301/metrics
BENCH_PLATFORM_VERSION=fabric-x-samples-${FXS_REF}
# State DB this network actually came up on; the harness records it as
# manifest.state_db next to the requested value (fairness lever).
BENCH_ACTUAL_STATE_DB=leveldb
EOF
log "fabricx up. token API :9100/:9500  kv-write view :9700"
warn "kv-write view is a Phase-3 stub (POST /kv -> 501); see deploy/docker/fabricx/kvview/README.md"
