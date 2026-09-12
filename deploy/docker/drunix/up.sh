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
need docker; need git; need jq
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DRUNIX_REPO="${BENCH_DRUNIX_REPO:-https://github.com/npci/drunix.git}"
DRUNIX_REF="${BENCH_DRUNIX_REF:-main}"
CACHE="${HERE}/.cache"
SRC="${CACHE}/drunix"
CHANNEL="${CHANNEL:-mychannel}"
CC_NAME="kvstore"
CC_SRC="${REPO_ROOT}/chaincodes/kvstore"

mkdir -p "$CACHE"
if [ -d "$DRUNIX_REPO/drunix-network" ]; then
  SRC="$DRUNIX_REPO"                                   # local checkout supplied
elif [ ! -d "$SRC/.git" ]; then
  log "cloning Drunix ${DRUNIX_REPO} @ ${DRUNIX_REF}"
  git clone --depth 1 --branch "$DRUNIX_REF" "$DRUNIX_REPO" "$SRC"
fi

NET="${SRC}/drunix-network/test-network"
[ -d "$NET" ] || die "expected ${NET} - check the Drunix repo layout"
cd "$NET"

STATE_DB="$(state_db drunix)"          # "leveldb" for normalized runs, else profile value
log "drunix state_db=${STATE_DB}"

# --- pin orderer batch params (ADR-011) -----------------------------------
CONFIGTX="${NET}/configtx/configtx.yaml"
if [ -f "$CONFIGTX" ]; then
  BT="$(platform_field drunix orderer_batch.batch_timeout       || echo 1s)"
  MMC="$(platform_field drunix orderer_batch.max_message_count   || echo 100)"
  PMB="$(platform_field drunix orderer_batch.preferred_max_bytes || echo '2 MB')"
  AMB="$(platform_field drunix orderer_batch.absolute_max_bytes  || echo '10 MB')"
  log "pinning orderer batch: timeout=${BT} maxMsgCount=${MMC} preferred=${PMB} absolute=${AMB}"
  python3 - "$CONFIGTX" "$BT" "$MMC" "$PMB" "$AMB" <<'PY'
import re, sys
path, bt, mmc, pmb, amb = sys.argv[1:6]
s = open(path).read()
s = re.sub(r'BatchTimeout:\s*\S+',        f'BatchTimeout: {bt}', s, count=1)
s = re.sub(r'MaxMessageCount:\s*\d+',     f'MaxMessageCount: {mmc}', s, count=1)
s = re.sub(r'PreferredMaxBytes:\s*[^\n]+',f'PreferredMaxBytes: {pmb}', s, count=1)
s = re.sub(r'AbsoluteMaxBytes:\s*[^\n]+', f'AbsoluteMaxBytes: {amb}', s, count=1)
open(path,'w').write(s)
PY
fi

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
    for n in 1 2 3 4; do docker pull "$img" && break; warn "retry pull $img ($n)"; sleep 5; done
  done
  # Reuse the shared fabric-samples CLI binaries if Drunix didn't fetch its own.
  if [ "$have_bins" != true ] && [ -x "${REPO_ROOT}/deploy/docker/.cache/fabric-samples/bin/peer" ]; then
    mkdir -p "${NET}/bin"
    cp "${REPO_ROOT}/deploy/docker/.cache/fabric-samples/bin/"* "${NET}/bin/" 2>/dev/null || true
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
  for n in 1 2 3; do docker pull eqalpha/keydb && break; sleep 5; done
  docker network create drunix_test 2>/dev/null || true
  docker compose -f "$KEYDB_COMPOSE" up -d
  sleep 4
fi
./network.sh up createChannel -c "$CHANNEL" -s "$NETWORK_SH_DB"

# Drunix's LP -> orderer -> CP -> stateless-VSCC path makes the first lifecycle
# tx slower than stock Fabric. The peer CLI's commit-wait uses
# peer.client.connTimeout (3s in Drunix's core.yaml) - too short, hence
# "timed out waiting for txid on all peers" on approveformyorg. Bump it and
# give deployCC more retries / delay.
export CORE_PEER_CLIENT_CONNTIMEOUT=120s
log "deploying ${CC_NAME} from ${CC_SRC} (connTimeout=120s, retries=10)"
./network.sh deployCC -c "$CHANNEL" -ccn "$CC_NAME" -ccp "$CC_SRC" -ccl go -r 10 -d 10

# --- emit connection.env ----------------------------------------------
ORG1="${NET}/organizations/peerOrganizations/org1.example.com"
USER_MSP="${ORG1}/users/User1@org1.example.com/msp"
CERT="$(ls "${USER_MSP}"/signcerts/* 2>/dev/null | head -1)"
cat > "${HERE}/connection.env" <<EOF
# generated by deploy/docker/drunix/up.sh  ($(date -u +%FT%TZ))
# endorse against the Lite Peer (:7051); its gateway federates commit-status
# from the Committing Peer (:7061).
BENCH_ADAPTER_PEER_ENDPOINT=localhost:7051
BENCH_ADAPTER_ENDORSE_ENDPOINT=localhost:7051
BENCH_ADAPTER_COMMIT_ENDPOINT=localhost:7061
BENCH_ADAPTER_GATEWAY_PEER=peer0.org1.example.com
BENCH_ADAPTER_MSP_ID=Org1MSP
BENCH_ADAPTER_CERT_PATH=${CERT:-${USER_MSP}/signcerts/User1@org1.example.com-cert.pem}
BENCH_ADAPTER_KEY_PATH=${USER_MSP}/keystore
BENCH_ADAPTER_TLS_CA_CERT_PATH=${ORG1}/peers/peer0.org1.example.com/tls/ca.crt
BENCH_ADAPTER_CHANNEL=${CHANNEL}
BENCH_ADAPTER_CHAINCODE=${CC_NAME}
BENCH_ADAPTER_METRICS_ENDPOINT=http://localhost:9444/metrics
BENCH_PLATFORM_VERSION=drunix-${DRUNIX_REF}
# The state DB this network ACTUALLY came up on. The harness records it as
# manifest.state_db alongside the requested value, so a normalized run cannot
# silently claim LevelDB parity it does not have.
BENCH_ACTUAL_STATE_DB=${NETWORK_SH_DB}
EOF
log "drunix up. lite-peer :7051  committing-peer :7061  lite-peer operations :9444"
