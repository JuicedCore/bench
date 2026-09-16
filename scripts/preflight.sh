#!/usr/bin/env bash
# Preflight: is THIS machine able to run the benchmark, and with which profile?
#
#   scripts/preflight.sh [--remote-loadgen] [profile]        # default: local
#
# --remote-loadgen: the load generator runs on another machine (scripts/gcp-run.sh
# puts it on its own VM), so only the platform budget and monitoring must fit here.
#
# Checks tooling, the Docker daemon, and - the part that actually bites - whether
# the host has the CPU/RAM/disk the chosen profile budgets. A benchmark run on an
# over-committed host measures swap, not the platform, so this refuses to call a
# host "ready" when it is not.
#
# Exit 0 = ready. Exit 1 = hard blocker. Exit 2 = runnable but numbers will be
# suspect (over-committed or under-resourced); re-run with a smaller profile.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
REMOTE_LOADGEN=0
if [ "${1:-}" = "--remote-loadgen" ]; then REMOTE_LOADGEN=1; shift; fi
PROFILE="${1:-local}"
PROFILE_FILE="deploy/profiles/${PROFILE}.yaml"

red()   { printf '\033[1;31m%s\033[0m\n' "$*"; }
amber() { printf '\033[1;33m%s\033[0m\n' "$*"; }
green() { printf '\033[1;32m%s\033[0m\n' "$*"; }

blockers=0
warnings=0
fail() { red   "  FAIL  $*"; blockers=$((blockers+1)); }
warn() { amber "  WARN  $*"; warnings=$((warnings+1)); }
ok()   { printf '  ok    %s\n' "$*"; }

echo "== tooling =="
for t in go docker git curl jq python3; do
  if command -v "$t" >/dev/null 2>&1; then
    # go spells it `go version`, everything else takes --version.
    case "$t" in go) v="$(go version 2>&1 | head -1)" ;; *) v="$("$t" --version 2>&1 | head -1)" ;; esac
    ok "$(printf '%-8s %s' "$t" "$v")"
  else
    fail "$t not installed"
  fi
done

# go.mod pins the language version; an older toolchain fails with a confusing
# build error rather than a clear one.
if command -v go >/dev/null 2>&1; then
  need="$(awk '/^go /{print $2; exit}' go.mod)"
  have="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if [ -n "$need" ] && [ -n "$have" ]; then
    if [ "$(printf '%s\n%s\n' "$need" "$have" | sort -V | head -1)" != "$need" ]; then
      fail "go $have is older than go.mod's $need"
    else
      ok "go $have satisfies go.mod ($need)"
    fi
  fi
fi

docker compose version >/dev/null 2>&1 && ok "docker compose plugin" || fail "docker compose plugin missing"
docker info >/dev/null 2>&1 && ok "docker daemon reachable" \
  || fail "cannot talk to the docker daemon (not running, or user not in the docker group)"

# Optional python deps. yaml has a yq fallback chain in deploy/docker/lib.sh, so
# it is only a warning; matplotlib only affects charts inside the per-run report;
# python-pptx is only needed for campaign PPT decks (scripts/gen-pptx.py).
# The capacity checks below read the profile with PyYAML.
python3 -c 'import yaml' 2>/dev/null && ok "python3 yaml" \
  || fail "python3 yaml (PyYAML) missing - run: sudo scripts/install-deps.sh"
python3 -c 'import matplotlib' 2>/dev/null && ok "python3 matplotlib" \
  || warn "python3 matplotlib missing - per-run reports render without charts"
python3 -c 'import pptx' 2>/dev/null && ok "python3 python-pptx" \
  || warn "python3 python-pptx missing - campaign PPT decks cannot be generated"

# run-all.sh drops the page cache between platforms for inter-run isolation.
sudo -n true 2>/dev/null && ok "passwordless sudo (page-cache drop between runs)" \
  || warn "no passwordless sudo - run-all.sh will prompt, or skip the page-cache drop"

echo
echo "== host vs profile '${PROFILE}' =="
[ -f "$PROFILE_FILE" ] || { fail "no such profile: $PROFILE_FILE"; echo; red "NOT READY"; exit 1; }
python3 -c 'import yaml' 2>/dev/null || { echo; red "NOT READY - ${blockers} blocker(s)"; exit 1; }

host_cores=$(nproc)
host_mem_gb=$(awk '/MemTotal/{printf "%.1f", $2/1048576}' /proc/meminfo)
host_avail_gb=$(awk '/MemAvailable/{printf "%.1f", $2/1048576}' /proc/meminfo)
swap_used_gb=$(awk '/SwapTotal/{t=$2} /SwapFree/{f=$2} END{printf "%.1f", (t-f)/1048576}' /proc/meminfo)
disk_avail_gb=$(df -BG --output=avail . | tail -1 | tr -dc '0-9')

read -r want_cpus want_mem want_loadgen <<EOF
$(python3 - "$PROFILE_FILE" <<'PY'
import sys, yaml
b = (yaml.safe_load(open(sys.argv[1])) or {}).get('budget', {}) or {}
print(b.get('total_cpus', 0), b.get('total_memory_gb', 0), b.get('load_gen_cpus', 0))
PY
)
EOF

# Monitoring (Prometheus/Grafana/cAdvisor/node_exporter) is ~1 core / 1 GB and
# runs alongside every benchmark.
mon_cpus=1; mon_mem=1
lg_mem=2
if [ "$REMOTE_LOADGEN" = 1 ]; then want_loadgen=0; lg_mem=0; fi
need_cpus=$(python3 -c "print(f'{$want_cpus + $want_loadgen + $mon_cpus:.1f}')")
need_mem=$(python3 -c "print(f'{$want_mem + $lg_mem + $mon_mem:.1f}')")

printf '  host:    %s cores, %s GB RAM (%s GB available), %s GB disk free\n' \
  "$host_cores" "$host_mem_gb" "$host_avail_gb" "$disk_avail_gb"
printf '  profile: %s cores + %s loadgen + %s monitoring = %s cores\n' \
  "$want_cpus" "$want_loadgen" "$mon_cpus" "$need_cpus"
printf '           %s GB platform + %s GB loadgen + %s GB monitoring = %s GB\n' \
  "$want_mem" "$lg_mem" "$mon_mem" "$need_mem"

awk -v h="$host_cores" -v n="$need_cpus" 'BEGIN{exit !(h+0 >= n+0)}' \
  && ok "cores sufficient" || warn "profile wants ${need_cpus} cores, host has ${host_cores}"

awk -v h="$host_mem_gb" -v n="$need_mem" 'BEGIN{exit !(h+0 >= n+0)}' \
  && ok "total RAM sufficient" \
  || fail "profile wants ${need_mem} GB, host has only ${host_mem_gb} GB - pick a smaller profile"

# Total RAM can be fine while the machine is busy; that is the case that silently
# ruins a run, so check what is actually free right now.
awk -v a="$host_avail_gb" -v n="$need_mem" 'BEGIN{exit !(a+0 >= n+0)}' \
  && ok "RAM available right now" \
  || warn "only ${host_avail_gb} GB available of the ${need_mem} GB needed - close other applications before running"

awk -v s="$swap_used_gb" 'BEGIN{exit !(s+0 < 1.0)}' \
  && ok "not swapping" \
  || warn "${swap_used_gb} GB already in swap - latency percentiles will measure swap, not the platform"

[ "${disk_avail_gb:-0}" -ge 40 ] && ok "disk headroom" \
  || warn "only ${disk_avail_gb} GB free; platform images need ~30 GB. Try: make clean"

# When the chosen profile does not fit, say which shipped profile does rather
# than leaving the operator to guess.
if [ "$blockers" -gt 0 ]; then
  echo
  echo "== profiles that fit this host =="
  fits=0
  for pf in deploy/profiles/*.yaml; do
    pn="$(basename "$pf" .yaml)"
    read -r _ m _ <<EOF2
$(python3 - "$pf" <<'PY2'
import sys, yaml
b = (yaml.safe_load(open(sys.argv[1])) or {}).get('budget', {}) or {}
print(b.get('total_cpus', 0), b.get('total_memory_gb', 0), b.get('load_gen_cpus', 0))
PY2
)
EOF2
    tot=$(python3 -c "print(f'{$m + 2 + 1:.1f}')")
    if awk -v h="$host_mem_gb" -v n="$tot" 'BEGIN{exit !(h+0 >= n+0)}'; then
      printf '  %-14s needs %s GB total\n' "$pn" "$tot"; fits=$((fits+1))
    fi
  done
  [ "$fits" -gt 0 ] || echo "  (none - this host is too small for any shipped profile)"
  echo
  echo "  re-run:  scripts/preflight.sh <profile>"
  echo "  then:    scripts/run-all.sh configs/normalized/probe-sweep.yaml <profile> <platforms...>"
fi

echo
if [ "$blockers" -gt 0 ]; then
  red "NOT READY - ${blockers} blocker(s), ${warnings} warning(s)"
  exit 1
elif [ "$warnings" -gt 0 ]; then
  amber "RUNNABLE, but ${warnings} warning(s) - numbers may not be trustworthy"
  exit 2
else
  green "READY"
fi
