#!/usr/bin/env bash
# Shared helpers for platform deploy scripts. Source this from each
# deploy/docker/<platform>/up.sh and down.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROFILE="${1:-${BENCH_PROFILE:-local}}"
PROFILE_FILE="${REPO_ROOT}/deploy/profiles/${PROFILE}.yaml"

log()  { printf '\033[1;34m[deploy]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[deploy]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[deploy]\033[0m %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

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
  echo none
}

# yaml_get <file> <yq-expression> — YAML anchors are resolved (yq / PyYAML).
yaml_get() {
  local file="$1" expr="$2" tool
  tool="$(_yq)"
  case "$tool" in
    none)   return 1 ;;
    pyyaml)
      python3 - "$file" "$expr" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
cur = doc
for part in sys.argv[2].lstrip('.').split('.'):
    if part == "": continue
    cur = cur[part]
print(cur)
PY
      ;;
    *)      "$tool" -r "$expr" "$file" ;;
  esac
}

# platform_field <platform> <field-path-under-platform> — e.g. per_container.cpus
platform_field() {
  local platform="$1" path="$2"
  yaml_get "$PROFILE_FILE" ".platforms.${platform}.${path}"
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
  local v
  v="$(platform_field "$platform" state_db 2>/dev/null || echo leveldb)"
  [ -n "$v" ] && [ "$v" != "null" ] && echo "$v" || echo leveldb
}

export REPO_ROOT PROFILE PROFILE_FILE
