#!/usr/bin/env bash
# Bring up a Drunix network (Lite Peer + Committing Peer + VSCC validation
# service + Raft orderer, 2 orgs) and deploy the kvstore chaincode.
#
#   ./up.sh [profile]
#
# Wraps npci/drunix -> drunix-network/test-network/network.sh (fabric-samples
# style). Verified against github.com/npci/drunix @ main:
#   - lite peer  org1 = peer0.org1.example.com : 7051   (endorsement + gateway)
#   - committing peer org1 = peer1.org1.example.com : 7061
#   - vscc       org1 = peer2.org1.example.com
#   - the lite peer knows CORE_PEER_COMMITTINGPEER_ENDPOINT, so its gateway
#     federates commit-status; the adapter connects to :7051 only.
#
# IMPORTANT: the shipped test-network hardcodes CORE_LEDGER_STATE_STATEDATABASE
# =sqldb (YugabyteDB) on the peers. Normalized runs (adr-012) require LevelDB, so
# for state_db=leveldb this script patches compose-test-net.yaml to drop the SQL
# env (peer then falls back to goleveldb from core.yaml).
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
need docker; need git; need jq; need python3
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
on_error_dump '^(lp1|cp\.|vs1|orderer|hlf_keydb|yugabyte|dev-)'


DRUNIX_REPO="${BENCH_DRUNIX_REPO:-https://github.com/npci/drunix.git}"
# Pinned by commit: Drunix publishes no release tags, and main moves.
DRUNIX_REF="${BENCH_DRUNIX_REF:-ddc0eae778158d3f8a96605cfeda383ae5eafcfc}"
CACHE="${HERE}/.cache"
SRC="${CACHE}/drunix"
CHANNEL="${CHANNEL:-mychannel}"
CC_NAME="kvstore"
CC_SRC="${REPO_ROOT}/chaincodes/kvstore"

mkdir -p "$CACHE"
if [ -d "$DRUNIX_REPO/drunix-network" ]; then
  SRC="$DRUNIX_REPO"                                   # local checkout supplied
else
  git_checkout_pinned "$DRUNIX_REPO" "$SRC" "$DRUNIX_REF"
fi

NET="${SRC}/drunix-network/test-network"
[ -d "$NET" ] || die "expected ${NET} - check the Drunix repo layout"
cd "$NET" || exit 1

STATE_DB="$(state_db drunix)"          # "leveldb" for normalized runs, else profile value
log "drunix state_db=${STATE_DB}"

# --- pin orderer batch params (ADR-011) -----------------------------------
patch_orderer_batch drunix "${NET}/configtx/configtx.yaml"

# --- state DB -----------------------------------------------------------
# Drunix's shipped test-network runs on YugabyteDB and its network.sh only
# wires the full node set (KeyDB, VSCC hostnames, SQL config) for `-s yugabyte`.
# A LevelDB run needs compose surgery that also breaks the Committing Peer's
# VSCC path (the drunix-peer/vscc images expect more than the state-DB toggle).
# So: default to Yugabyte (Drunix's tested config); a normalized run on Drunix
# carries a "ran on YugabyteDB, LevelDB unavailable in the shipped test-network"
# manifest caveat. Set BENCH_DRUNIX_FORCE_LEVELDB=1 to try the experimental
# LevelDB patch anyway.
NETWORK_SH_DB="yugabyte"
COMPOSE_NET="${NET}/compose/compose-test-net.yaml"
if [ "${BENCH_DRUNIX_FORCE_LEVELDB:-0}" = "1" ] && [ -f "$COMPOSE_NET" ]; then
  warn "BENCH_DRUNIX_FORCE_LEVELDB=1: experimental LevelDB patch (known to break the VSCC path)"
  grep -q 'CORE_LEDGER_STATE_STATEDATABASE=sqldb' "$COMPOSE_NET" && {
    cp "$COMPOSE_NET" "${COMPOSE_NET}.bench.bak"
    python3 - "$COMPOSE_NET" <<'PY'
import sys
p = sys.argv[1]
out = []
for ln in open(p):
    if 'CORE_LEDGER_STATE_STATEDATABASE=sqldb' in ln:
        out.append(ln.replace('sqldb', 'goleveldb')); continue
    if 'CORE_LEDGER_STATE_SQLDBCONFIG_' in ln:
        continue
    out.append(ln)
open(p, 'w').write(''.join(out))
PY
  }
  NETWORK_SH_DB="leveldb"
fi
log "drunix state DB: ${NETWORK_SH_DB}"

# --- prereq: Fabric CLI binaries + Drunix images ------------------------
have_bins=false
{ [ -x "${NET}/../bin/peer" ] || [ -x "${NET}/bin/peer" ]; } && have_bins=true
have_imgs=true
for img in npcioss/drunix-orderer:1.0.0 npcioss/drunix-peer:1.0.0 npcioss/drunix-vscc:1.0.0 npcioss/drunix-ccenv:1.0 npcioss/drunix-baseos:1.0; do
  docker image inspect "$img" >/dev/null 2>&1 || have_imgs=false
done
if [ "$have_bins" != true ] || [ "$have_imgs" != true ]; then
  # Pull the Drunix images directly (docker per-layer resume beats prereq's curl).
  for img in npcioss/drunix-orderer:1.0.0 npcioss/drunix-peer:1.0.0 npcioss/drunix-vscc:1.0.0 npcioss/drunix-ccenv:1.0 npcioss/drunix-baseos:1.0; do
    docker image inspect "$img" >/dev/null 2>&1 || pull_image "$img" 4
  done
  # Reuse the shared fabric-samples CLI binaries if Drunix didn't fetch its own.
  if [ "$have_bins" != true ] && [ -x "${REPO_ROOT}/deploy/docker/.cache/fabric-samples/bin/peer" ]; then
    mkdir -p "${NET}/bin"
    cp "${REPO_ROOT}/deploy/docker/.cache/fabric-samples/bin/"* "${NET}/bin/" \
      || warn "could not copy shared Fabric CLI binaries into ${NET}/bin; falling back to network.sh prereq"
  fi
  { [ -x "${NET}/../bin/peer" ] || [ -x "${NET}/bin/peer" ]; } || \
    ( log "running network.sh prereq for Fabric binaries"; ./network.sh prereq || warn "prereq non-zero; continuing" )
fi

# Drunix network.sh checkPrereqs runs `peer version` - put its binaries on PATH.
export PATH="${NET}/../bin:${NET}/bin:${PATH}"
peer version >/dev/null 2>&1 || die "drunix: 'peer' not runnable after prereq (PATH=${NET}/../bin)"

# --- KeyDB: only needed for the experimental LevelDB path (Yugabyte's own
# compose, which network.sh starts for -s yugabyte, already includes KeyDB). ---
KEYDB_COMPOSE="${NET}/scripts/keydb-only.bench.yaml"
if [ "$NETWORK_SH_DB" = "leveldb" ]; then
  cat > "$KEYDB_COMPOSE" <<'YAML'
networks:
  test: {name: drunix_test}
services:
  hlf_keydb_org1msp:
    image: eqalpha/keydb
    container_name: hlf_keydb_org1msp
    command: ["keydb-server", "/etc/keydb/keydb.conf"]
    environment: ["ALLOW_EMPTY_PASSWORD=yes"]
    ports: ["6479:6379"]
    networks: [test]
  hlf_keydb_org2msp:
    image: eqalpha/keydb
    container_name: hlf_keydb_org2msp
    command: ["keydb-server", "/etc/keydb/keydb.conf"]
    environment: ["ALLOW_EMPTY_PASSWORD=yes"]
    ports: ["6389:6379"]
    networks: [test]
YAML
fi

# --- start network + channel + chaincode --------------------------------
./network.sh down || true
[ -f "$KEYDB_COMPOSE" ] && docker compose -f "$KEYDB_COMPOSE" down 2>/dev/null || true
drop_caches
if [ "$NETWORK_SH_DB" = "leveldb" ]; then
  pull_image eqalpha/keydb 3
  docker network inspect drunix_test >/dev/null 2>&1 || docker network create drunix_test >/dev/null
  docker compose -f "$KEYDB_COMPOSE" up -d || die "drunix: KeyDB compose up failed"
  wait_for "KeyDB containers running" 60 sh -c '[ "$(docker ps --filter name=hlf_keydb_org --format x | wc -l)" -ge 2 ]' \
    || die "drunix: KeyDB did not start" "docker compose -f ${KEYDB_COMPOSE} logs"
fi
# network.sh starts YugabyteDB and the peers together, and a peer whose ledger
# provider cannot reach YSQL (:5433) panics instead of waiting - seen as
# "failed to connect to user=yugabyte ... connection refused" and every peer
# Exited (2). Retry the bring-up rather than failing the whole run on that race.
for attempt in 1 2 3; do
  ./network.sh up createChannel -c "$CHANNEL" -s "$NETWORK_SH_DB" && break
  if [ "$attempt" = 3 ]; then
    dump_containers '^(lp1|cp\.|vs1|orderer|yugabyte)' 60
    die "drunix network did not come up after 3 attempts" \
        "exited peers with 'connection refused' on :5433 = YugabyteDB not ready; OOM (exit 137) = raise the profile's memory budget"
  fi
  warn "drunix bring-up attempt ${attempt} failed (peers racing YugabyteDB start-up?); retrying"
  ./network.sh down || true
  sleep 10
done

# Drunix's LP -> orderer -> CP -> stateless-VSCC path makes the first lifecycle
# tx slower than stock Fabric. The peer CLI's commit-wait uses
# peer.client.connTimeout (3s in Drunix's core.yaml) - too short, hence
# "timed out waiting for txid on all peers" on approveformyorg. Bump it and
# give deployCC more retries / delay.
export CORE_PEER_CLIENT_CONNTIMEOUT=120s
log "deploying ${CC_NAME} from ${CC_SRC} (connTimeout=120s, retries=10)"
./network.sh deployCC -c "$CHANNEL" -ccn "$CC_NAME" -ccp "$CC_SRC" -ccl go -r 10 -d 10 \
  || die "drunix: chaincode deploy failed" "'docker logs lp1.org1.example.com' and 'docker logs cp.org1.example.com' have lifecycle errors"

# --- resource budget (equal total across platforms, split evenly) ------------
# Every Drunix node counts: Lite/Committing Peers, VSCC, orderer, and the KeyDB +
# YugabyteDB state stores the Committing Peers depend on.
warm_chaincode "$NET" "$CHANNEL" "$CC_NAME" 1 2
RES_ENV="$(apply_budget drunix '^(lp1\.org[12]|cp\.org[12]|vs1\.org[12]|orderer\.example\.com|hlf_keydb_org[12]msp|yugabyte-org[12]|dev-)')"

# --- emit connection.env ----------------------------------------------
ORG1="${NET}/organizations/peerOrganizations/org1.example.com"
USER_MSP="${ORG1}/users/User1@org1.example.com/msp"
CERT="$(user_signcert "$USER_MSP")"
check_paths "$CERT" "${USER_MSP}/keystore" "${ORG1}/peers/peer0.org1.example.com/tls/ca.crt"
cat > "${HERE}/connection.env" <<EOF
# generated by deploy/docker/drunix/up.sh  ($(date -u +%FT%TZ))
# endorse against the Lite Peer (:7051); its gateway federates commit-status
# from the Committing Peer (:7061).
BENCH_ADAPTER_PEER_ENDPOINT=localhost:7051
BENCH_ADAPTER_ENDORSE_ENDPOINT=localhost:7051
BENCH_ADAPTER_COMMIT_ENDPOINT=localhost:7061
BENCH_ADAPTER_GATEWAY_PEER=peer0.org1.example.com
BENCH_ADAPTER_MSP_ID=Org1MSP
BENCH_ADAPTER_CERT_PATH=${CERT}
BENCH_ADAPTER_KEY_PATH=${USER_MSP}/keystore
BENCH_ADAPTER_TLS_CA_CERT_PATH=${ORG1}/peers/peer0.org1.example.com/tls/ca.crt
BENCH_ADAPTER_CHANNEL=${CHANNEL}
BENCH_ADAPTER_CHAINCODE=${CC_NAME}
BENCH_ADAPTER_METRICS_ENDPOINT=http://localhost:9444/metrics
BENCH_PLATFORM_VERSION=drunix-${DRUNIX_REF:0:12}
# The state DB this network ACTUALLY came up on. The harness records it as
# manifest.state_db alongside the requested value, so a normalized run cannot
# silently claim LevelDB parity it does not have.
BENCH_ACTUAL_STATE_DB=${NETWORK_SH_DB}
${RES_ENV}
EOF
log "drunix up. lite-peer :7051  committing-peer :7061  lite-peer operations :9444"
