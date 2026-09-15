#!/usr/bin/env bash
# Shared helpers for platform deploy scripts. Source this from each
# deploy/docker/<platform>/up.sh and down.sh.
set -euo pipefail
# Without this, bash turns errexit off inside $(...): a failing tar or docker
# tag inside SAMPLES="$(fabric_samples_bootstrap ...)" would be ignored.
shopt -s inherit_errexit

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROFILE="${1:-${BENCH_PROFILE:-local}}"
PROFILE_FILE="${REPO_ROOT}/deploy/profiles/${PROFILE}.yaml"

# log / warn / die / need / wait_for / dump_containers / on_error_dump
LOG_TAG=deploy
# shellcheck source=../../scripts/log-lib.sh
. "${REPO_ROOT}/scripts/log-lib.sh"

if [ ! -f "$PROFILE_FILE" ]; then
  die "profile '${PROFILE}' not found: ${PROFILE_FILE}" \
      "available: $(cd "${REPO_ROOT}/deploy/profiles" && ls ./*.yaml 2>/dev/null | sed 's#^\./##; s/\.yaml$//' | tr '\n' ' ')" \
      "pass it as the first argument (up.sh local-small) or set BENCH_PROFILE"
fi

# _yq — resolve a usable yq: system yq, else a cached static mikefarah binary,
# else python3+PyYAML. Echoes the command to run ("yq" or a path or "pyyaml").
_yq() {
  if command -v yq >/dev/null 2>&1; then echo yq; return; fi
  local cache="${REPO_ROOT}/deploy/docker/.cache"
  if [ -x "${cache}/yq" ]; then echo "${cache}/yq"; return; fi
  if python3 -c 'import yaml' 2>/dev/null; then echo pyyaml; return; fi
  mkdir -p "$cache"
  local ver="v4.44.3" arch
  case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch=amd64 ;; esac
  if curl -sSfL "https://github.com/mikefarah/yq/releases/download/${ver}/yq_linux_${arch}" -o "${cache}/yq" 2>/dev/null; then
    chmod +x "${cache}/yq"; echo "${cache}/yq"; return
  fi
  die "cannot read YAML profiles: no yq, no python3 with PyYAML, and downloading yq ${ver} failed" \
      "install one: scripts/install-deps.sh, or pip install pyyaml"
}

# yaml_get <file> <yq-expression> — YAML anchors are resolved (yq / PyYAML).
# Prints the value and returns 0, or prints nothing and returns 1 when the path
# is missing or null (mikefarah yq prints "null" and exits 0 for a missing path,
# which used to flow into configtx as the literal string "null").
yaml_get() {
  local file="$1" expr="$2" tool out
  tool="$(_yq)"
  case "$tool" in
    pyyaml)
      out="$(python3 - "$file" "$expr" <<'PY'
import sys, yaml
cur = yaml.safe_load(open(sys.argv[1]))
for part in sys.argv[2].lstrip('.').split('.'):
    if part == "":
        continue
    if not isinstance(cur, dict) or part not in cur:
        sys.exit(1)
    cur = cur[part]
if cur is None:
    sys.exit(1)
print(cur)
PY
)" || return 1
      ;;
    *) out="$("$tool" -r "$expr" "$file")" || return 1 ;;
  esac
  [ -n "$out" ] && [ "$out" != "null" ] || return 1
  printf '%s\n' "$out"
}

# platform_field <platform> <field-path-under-platform> — e.g. orderer_batch.batch_timeout
# Returns 1 (no output) when the field is absent.
platform_field() {
  local platform="$1" path="$2"
  yaml_get "$PROFILE_FILE" ".platforms.${platform}.${path}"
}

# require_platform_field <platform> <field-path> — like platform_field, but a
# missing value is fatal. Use it for fairness levers (orderer batch parameters):
# a silent default would make the platforms' configs differ without a trace.
require_platform_field() {
  platform_field "$1" "$2" || die "profile ${PROFILE_FILE} has no platforms.$1.$2" \
    "every Fabric-family platform must pin orderer_batch (docs/decisions/adr-011-orderer-batch-params.md)"
}

# git_checkout_pinned <url> <dir> <ref>
#
# Check out <ref> - a commit SHA, tag or branch - into <dir>, reusing an existing
# checkout when it is already there. Pin upstream sources by SHA: a branch such as
# `main` moves, so a clone made on another machine next month is a different
# platform build than the one the recorded results came from. `git clone --branch`
# cannot take a SHA, hence init + fetch.
git_checkout_pinned() {
  local url="$1" dir="$2" ref="$3" n
  if [ -d "${dir}/.git" ]; then
    local head want
    head="$(git -C "$dir" rev-parse HEAD 2>/dev/null || true)"
    want="$(git -C "$dir" rev-parse -q --verify "${ref}^{commit}" 2>/dev/null || true)"
    if [ -n "$head" ] && [ "$head" = "$want" ]; then
      return 0
    fi
    warn "$(basename "$dir") is at ${head:-nothing}, want ${ref} - refetching"
  else
    rm -rf "$dir"
    mkdir -p "$dir"
    git -C "$dir" init -q
    git -C "$dir" remote add origin "$url"
  fi
  for n in 1 2 3; do
    log "fetching $(basename "$dir") @ ${ref} (attempt ${n})"
    if git -C "$dir" fetch -q --depth 1 origin "$ref"; then
      git -C "$dir" checkout -q --force FETCH_HEAD && return 0
    fi
    sleep 5
  done
  die "could not fetch ${url} @ ${ref}"
}

# fabric_samples_bootstrap <fabric_version> <ca_version> [samples_ref]
# Ensures a shared fabric-samples checkout at deploy/docker/.cache/fabric-samples
# with the given Fabric binary/config version installed. Re-installs bin+config
# when the requested version differs from what's cached (fabric-cft and
# fabric-bft use different Fabric majors but share the version-agnostic
# test-network scripts). Echoes the samples dir. Retries the clone on flaky
# networks.
fabric_samples_bootstrap() {
  local fver="$1" caver="$2" ref="${3:?fabric-samples ref required}"
  local samples="${REPO_ROOT}/deploy/docker/.cache/fabric-samples"
  mkdir -p "${REPO_ROOT}/deploy/docker/.cache"

  git_checkout_pinned https://github.com/hyperledger/fabric-samples.git "$samples" "$ref" >&2

  local have=""
  [ -f "${samples}/bin/.fabricver" ] && have="$(cat "${samples}/bin/.fabricver")"
  if [ ! -x "${samples}/bin/peer" ] || [ ! -f "${samples}/config/core.yaml" ] || [ "$have" != "$fver" ]; then
    log "installing Fabric ${fver} binaries + config (had: ${have:-none})"
    rm -rf "${samples:?}/bin" "${samples:?}/config" "${samples:?}/builders"
    mkdir -p "${samples}/bin" "${samples}/config"
    # Resumable, retrying downloads - install-fabric.sh's plain curl chokes on a
    # flaky link mid-tarball.
    local dl="${REPO_ROOT}/deploy/docker/.cache/dl"
    mkdir -p "$dl"
    local fbtar="hyperledger-fabric-linux-amd64-${fver}.tar.gz"
    local catar="hyperledger-fabric-ca-linux-amd64-${caver}.tar.gz"
    _resume_get "https://github.com/hyperledger/fabric/releases/download/v${fver}/${fbtar}" "${dl}/${fbtar}"
    _resume_get "https://github.com/hyperledger/fabric-ca/releases/download/v${caver}/${catar}" "${dl}/${catar}"
    tar -xzf "${dl}/${fbtar}" -C "$samples"   # -> bin/ config/ builders/
    tar -xzf "${dl}/${catar}" -C "$samples"   # -> bin/fabric-ca-client (+ server)
    [ -f "${samples}/config/core.yaml" ] || die "fabric ${fver} tarball did not contain config/core.yaml"
    # Docker images (per-layer resume is robust); tolerate transient failure.
    log "pulling Fabric ${fver} docker images"
    # pin_fabric_images below pulls anything still missing and dies if it cannot,
    # so a failure here is survivable - but say what failed.
    local installer="${dl}/install-fabric-${fver}.sh"
    if curl -fsSL --retry 3 --connect-timeout 20 -o "$installer" \
         "https://raw.githubusercontent.com/hyperledger/fabric/v${fver}/scripts/install-fabric.sh"; then
      ( cd "$samples" && bash "$installer" --fabric-version "$fver" --ca-version "$caver" docker ) >&2 \
        || warn "install-fabric.sh docker pull exited non-zero; missing images are pulled individually next"
    else
      warn "could not download install-fabric.sh (curl exit $?); missing images are pulled individually next"
    fi
    echo "$fver" > "${samples}/bin/.fabricver"
  fi
  pin_fabric_images "$fver" "$caver"
  # stdout: ONLY the samples path (callers do SAMPLES="$(fabric_samples_bootstrap ...)")
  printf '%s\n' "$samples"
}

# pin_fabric_images <fabric_version> <ca_version>
#
# The fabric-samples test-network starts `hyperledger/fabric-*:latest`, and the
# peer builds chaincode with `fabric-ccenv:<major.minor>`. Those tags are only
# re-pointed when install-fabric.sh runs, which the bootstrap above does only when
# the *binary* version changes. Bringing up fabric-cft (2.5.x) after fabric-bft
# (3.1.x), or after a partly failed pull, therefore left a mixed network - found
# with peer, ccenv and baseos on 3.1.4 while the orderer and binaries were 2.5.16,
# and the manifest reporting 2.5.16. Re-point every tag on every bring-up, from
# images already present where possible, and verify inside the containers.
pin_fabric_images() {
  local fver="$1" caver="$2"
  local mm="${fver%.*}" camm="${caver%.*}"
  local repo ver src
  for repo in peer orderer ccenv baseos ca; do
    ver="$fver"; [ "$repo" = ca ] && ver="$caver"
    src="hyperledger/fabric-${repo}:${ver}"
    if ! docker image inspect "$src" >/dev/null 2>&1; then
      if docker image inspect "ghcr.io/${src}" >/dev/null 2>&1; then
        docker tag "ghcr.io/${src}" "$src"
      else
        log "pulling ${src}"
        docker pull -q "$src" >/dev/null || docker pull -q "ghcr.io/${src}" >/dev/null \
          || die "cannot obtain ${src}"
        docker image inspect "$src" >/dev/null 2>&1 || docker tag "ghcr.io/${src}" "$src"
      fi
    fi
    docker tag "$src" "hyperledger/fabric-${repo}:latest"
    if [ "$repo" = ca ]; then
      docker tag "$src" "hyperledger/fabric-ca:${camm}"
    else
      docker tag "$src" "hyperledger/fabric-${repo}:${mm}"
    fi
  done

  local got raw bin
  for bin in peer orderer; do
    raw="$(docker run --rm "hyperledger/fabric-${bin}:latest" "$bin" version 2>&1)" || true
    got="$(printf '%s\n' "$raw" | sed -ne 's/^ *Version: v\{0,1\}//p' | head -1)"
    [ "$got" = "$fver" ] || die "fabric-${bin}:latest reports version '${got:-nothing}', wanted ${fver}" \
      "docker run output: $(printf '%s' "$raw" | tr '\n' ' ' | cut -c1-300)" \
      "remove stale tags (docker rmi hyperledger/fabric-${bin}:latest) and rerun"
  done
  log "fabric images pinned to ${fver} (ca ${caver}); peer and orderer verified"
}

# _resume_get <url> <dest> — download with resume + aggressive retry.
_resume_get() {
  local url="$1" dest="$2" n rc
  for n in 1 2 3 4 5 6; do
    rc=0
    curl -fsSL -C - --retry 5 --retry-delay 5 --retry-all-errors \
         --connect-timeout 20 --speed-time 30 --speed-limit 1024 \
         -o "$dest" "$url" || rc=$?
    [ "$rc" -eq 0 ] && return 0
    warn "download ${url##*/} attempt ${n}/6 failed (curl exit ${rc}); retrying in 5s"
    sleep 5
  done
  die "failed to download ${url} after 6 attempts" \
      "check network/proxy access to $(printf '%s' "$url" | cut -d/ -f3); a partial file is kept at ${dest} and resumed next time"
}

# drop_caches — best effort page-cache drop for inter-run isolation.
drop_caches() {
  if [ "$(id -u)" = "0" ]; then
    sync && echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
  else
    sync
    sudo sh -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null \
      || warn "could not drop page cache (needs root); continuing"
  fi
}

# pin_state_db_for_normalized <platform> — echoes the state DB the deploy should
# use. Normalized runs are always leveldb; the run config decides normalized, so
# deploy scripts default to the profile value and the run itself asserts parity.
state_db() {
  local platform="$1"
  platform_field "$platform" state_db || echo leveldb
}

export REPO_ROOT PROFILE PROFILE_FILE

# memory_role <container-name> — the resource role a container plays, which
# decides its share of the memory budget (see apply_budget). One table for every
# platform, so no platform gets a role mapping the others do not.
#
# Fabric-X packs services into containers, so its containers take the weight of
# what they hold, not of their nominal role: fabricx-arma runs all 16 Arma
# processes (router, batcher, consenter, assembler for 4 parties) and
# fabricx-pipeline runs sidecar + verifier + coordinator. The reference
# deployment on this host runs each of them at 1765m.
memory_role() {
  case "$1" in
    peer[0-9]*.*|lp[0-9]*.*|cp.*|*fabricx-committer*|*fabricx-arma*|*fabricx-pipeline*|*neuchain-block-server*) echo peer ;;
    yugabyte*|*fabricx-db*)                                                  echo statedb ;;
    orderer*|*neuchain-epoch-server*)                                        echo orderer ;;
    *)                                                                       echo other ;;
  esac
}

# memory_weight <role> — relative memory share. Set from the cAdvisor peaks of the
# 2026-09-13 local-small sweeps under the old even split: the gateway peer reached
# 1432 MiB (and was OOM-killed at 2000 TPS), YugabyteDB sat at its 630 MiB cap and
# had postgres OOM-killed, orderers peaked at 170-330 MiB, and chaincode, KeyDB and
# VSCC containers stayed under 210 MiB.
memory_weight() {
  case "$1" in
    peer|statedb) echo 4 ;;
    orderer)      echo 2 ;;
    *)            echo 1 ;;
  esac
}
MEMORY_WEIGHTS_DESC="peer=4 statedb=4 orderer=2 other=1"

# apply_budget <platform> <container-name-regex>
#
# Give the platform under test the profile's TOTAL resource budget, split across
# the containers it actually runs, and print what was applied as connection.env
# lines on stdout (logs go to stderr).
#
# Why a total rather than a per-container figure: the platforms run very
# different numbers of containers (Fabric-X 4, Drunix ~13), so one per-container
# cap would hand the many-container platforms several times the hardware of the
# others. Equal total budget is "same slice of the same machine".
#
# CPU is split evenly. Memory is split by role weight (memory_role/memory_weight):
# an even split OOM-killed the gateway peer on Fabric and YugabyteDB's postgres on
# Drunix while chaincode and KeyDB containers used a fraction of their share. The
# weights are one table for every platform and are recorded in the manifest. The
# trade-off - a container cannot borrow another's idle share - applies to every
# platform alike.
#
# Uses `docker update` rather than compose limits because the Fabric-family
# compose files are generated by upstream scripts this repo does not own. Call it
# after every container that belongs to the measured path is running (chaincode
# containers included).
apply_budget() {
  local platform="$1" pattern="$2"
  local total_cpus total_gb
  total_cpus="$(yaml_get "$PROFILE_FILE" '.budget.total_cpus')" || die "profile ${PROFILE}: no budget.total_cpus"
  total_gb="$(yaml_get "$PROFILE_FILE" '.budget.total_memory_gb')" || die "profile ${PROFILE}: no budget.total_memory_gb"

  local names
  names="$(docker ps --format '{{.Names}}' | grep -E "$pattern" | sort || true)"
  [ -n "$names" ] || die "apply_budget ${platform}: no running containers match /${pattern}/"
  local n; n="$(printf '%s\n' "$names" | wc -l | tr -d ' ')"

  local c total_weight=0
  while read -r c; do
    total_weight=$(( total_weight + $(memory_weight "$(memory_role "$c")") ))
  done <<< "$names"

  local cpus
  cpus="$(awk -v t="$total_cpus" -v n="$n" 'BEGIN{c=t/n; if (c<0.1) c=0.1; printf "%.2f", c}')"

  log "resource budget for ${platform}: ${total_cpus} CPU / ${total_gb} GB across ${n} containers = ${cpus} CPU each; memory by role weight (${MEMORY_WEIGHTS_DESC}, total weight ${total_weight})"
  local role w mem_mb got_cpu got_mem split=""
  while read -r c; do
    role="$(memory_role "$c")"
    w="$(memory_weight "$role")"
    # Floor, so the per-container limits never sum past the total.
    mem_mb="$(awk -v g="$total_gb" -v w="$w" -v tw="$total_weight" 'BEGIN{m=int(g*1024*w/tw); if (m<64) m=64; print m}')"
    # --memory-swap == --memory: no swap, so a container at its cap is throttled
    # or OOM-killed rather than quietly paging and inflating latency.
    docker update --cpus "$cpus" --memory "${mem_mb}m" --memory-swap "${mem_mb}m" "$c" >/dev/null \
      || die "apply_budget: docker update failed for ${c}"
    got_cpu="$(docker inspect -f '{{.HostConfig.NanoCpus}}' "$c")"
    got_mem="$(docker inspect -f '{{.HostConfig.Memory}}' "$c")"
    [ "$got_cpu" -gt 0 ] && [ "$got_mem" -gt 0 ] || die "apply_budget: limits did not stick on ${c}"
    [ "$got_mem" -eq $(( mem_mb * 1024 * 1024 )) ] || die "apply_budget: ${c} memory limit is ${got_mem} bytes, wanted ${mem_mb}m"
    log "  ${c}  (${role}) ${mem_mb}m"
    split="${split:+${split},}${c}=${mem_mb}m"
  done <<< "$names"

  cat <<ENV
# Resource budget actually applied by deploy (lib.sh apply_budget); recorded in
# the manifest as the resource fairness lever.
BENCH_RESOURCE_CONTAINERS=${n}
BENCH_RESOURCE_CPUS_EACH=${cpus}
BENCH_RESOURCE_MEMORY_SPLIT=${split}
BENCH_RESOURCE_MEMORY_WEIGHTS="${MEMORY_WEIGHTS_DESC}"
BENCH_RESOURCE_CPUS_TOTAL=${total_cpus}
BENCH_RESOURCE_MEMORY_TOTAL_GB=${total_gb}
ENV
}

# warm_chaincode <network.sh dir> <channel> <chaincode> <org>...
#
# Fabric starts a chaincode container on its first invocation, which would
# otherwise be the benchmark's first transaction - after apply_budget has run,
# leaving that container unconstrained. Query once per org so the containers
# exist before limits are applied.
warm_chaincode() {
  local dir="$1" channel="$2" cc="$3"; shift 3
  local org
  local out
  for org in "$@"; do
    # A failed warm-up query is not fatal - the wait below reports what matters
    # (containers without limits) - but its output is the only clue why.
    out="$( cd "$dir" && ./network.sh cc query -org "$org" -c "$channel" -ccn "$cc" \
        -ccqc '{"Args":["Get","__bench_warmup__"]}' 2>&1 )" \
      || warn "warm-up query for org ${org} failed: $(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-300)"
  done
  local i=0
  while [ "$i" -lt 60 ]; do
    [ "$(docker ps --format '{{.Names}}' | grep -c '^dev-')" -ge "$#" ] && return 0
    i=$((i+1)); sleep 1
  done
  warn "only $(docker ps --format '{{.Names}}' | grep -c '^dev-') of $# chaincode containers started; they will run without resource limits"
}

# patch_orderer_batch <platform> <configtx.yaml> [bft]
#
# Write the profile's orderer_batch into a configtx file. Every substitution must
# match: if upstream renames or reformats a key, the patch fails loudly instead
# of benchmarking with upstream's default block cutting while the manifest
# claims the pinned values (adr-011). With "bft", SmartBFT's request batching is
# aligned to the same values (it may be absent, so it is not required).
patch_orderer_batch() {
  local platform="$1" configtx="$2" bft="${3:-}"
  local bt mmc pmb amb
  bt="$(require_platform_field "$platform" orderer_batch.batch_timeout)"
  mmc="$(require_platform_field "$platform" orderer_batch.max_message_count)"
  pmb="$(require_platform_field "$platform" orderer_batch.preferred_max_bytes)"
  amb="$(require_platform_field "$platform" orderer_batch.absolute_max_bytes)"
  [ -f "$configtx" ] || die "cannot pin orderer batch: ${configtx} does not exist (upstream layout changed?)"
  log "pinning orderer batch in ${configtx##*/}: timeout=${bt} maxMsgCount=${mmc} preferred=${pmb} absolute=${amb}"
  python3 - "$configtx" "$bt" "$mmc" "$pmb" "$amb" "$bft" <<'PY' || die "orderer batch patch failed for ${configtx} (see message above)"
import re, sys
path, bt, mmc, pmb, amb, bft = sys.argv[1:7]
s = open(path).read()
required = [
    (r'BatchTimeout:\s*\S+',         f'BatchTimeout: {bt}',          1),
    (r'MaxMessageCount:\s*\d+',      f'MaxMessageCount: {mmc}',      1),
    (r'PreferredMaxBytes:\s*[^\n]+', f'PreferredMaxBytes: {pmb}',    1),
    (r'AbsoluteMaxBytes:\s*[^\n]+',  f'AbsoluteMaxBytes: {amb}',     1),
]
missing = []
for pat, rep, count in required:
    s, n = re.subn(pat, rep, s, count=count)
    if n == 0:
        missing.append(pat.split(':')[0])
if bft:
    s = re.sub(r'RequestBatchMaxCount:\s*\d+',    f'RequestBatchMaxCount: {mmc}', s)
    s = re.sub(r'RequestBatchMaxInterval:\s*\S+', f'RequestBatchMaxInterval: {bt}', s)
if missing:
    sys.stderr.write(f"orderer batch keys not found in {path}: {', '.join(missing)}\n")
    sys.exit(1)
open(path, 'w').write(s)
PY
}

# user_signcert <msp-dir> - echo the one signing certificate in <msp>/signcerts,
# or die saying where it looked (the network's crypto material was not generated).
user_signcert() {
  local msp="$1" f
  for f in "${msp}"/signcerts/*; do
    [ -f "$f" ] && { printf '%s\n' "$f"; return 0; }
  done
  die "no signing certificate in ${msp}/signcerts" \
      "the network's crypto material was not generated - look for an earlier cryptogen/CA error above"
}

# check_paths <path>... - die naming every connection-material path that does
# not exist, before connection.env points the harness at it.
check_paths() {
  local p missing=()
  for p in "$@"; do [ -e "$p" ] || missing+=("$p"); done
  [ "${#missing[@]}" -eq 0 ] || die "connection material missing: ${missing[*]}" \
    "the network came up without the expected organizations/ layout; rerun up.sh and read its output"
}

# pull_image <image> [attempts] - docker pull with retries; dies after the last.
pull_image() {
  local img="$1" attempts="${2:-4}" n
  for n in $(seq 1 "$attempts"); do
    docker pull -q "$img" >/dev/null && return 0
    warn "docker pull ${img} failed (attempt ${n}/${attempts})"
    [ "$n" -lt "$attempts" ] && sleep 5
  done
  die "cannot pull ${img}" "check registry access (docker login / proxy) and disk space (docker system df)"
}

# port_open <host> <port> - true when a TCP connect succeeds (no nc needed).
port_open() { timeout 2 bash -c ">/dev/tcp/$1/$2" 2>/dev/null; }

# compose_dump [lines] - `docker compose ps` and log tails for the compose
# project in the current directory. For WAIT_FOR_ON_TIMEOUT.
compose_dump() {
  local lines="${1:-80}"
  printf '\n----- docker compose ps -----\n' >&2
  docker compose ps -a >&2 2>&1 || true
  printf '\n----- docker compose logs --tail %s -----\n' "$lines" >&2
  docker compose logs --no-color --tail "$lines" >&2 2>&1 || true
}
