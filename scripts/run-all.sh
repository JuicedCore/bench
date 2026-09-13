#!/usr/bin/env bash
# Run one or more configs across several platforms on THIS machine, SEQUENTIALLY,
# with full inter-run isolation (docs/architecture/fairness-guarantees.md, adr-005).
#
#   scripts/run-all.sh <config.yaml[,config.yaml...]> [profile] [platform ...]
#
#   scripts/run-all.sh configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml local-small
#
# Use configs from configs/normalized/ for a comparison: one file serves every
# platform, which is what makes running the same bytes against each meaningful.
#
# Default platforms: fabric-cft fabric-bft drunix. fabricx is supported but not
# yet verified on a live network, and neuchain needs its image built first (see
# docs/REMAINING-WORK.md); name them explicitly to include them.
#
# Every platform is deployed fresh for every config and torn down afterwards. The
# log and a comparison report covering only this campaign's runs go to
# results/_campaigns/<timestamp>-<profile>/. Exits non-zero if any deploy or run
# failed.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

CONFIGS="${1:?usage: run-all.sh <config.yaml[,config.yaml...]> [profile] [platform ...]}"
PROFILE="${2:-local}"
shift $(( $# >= 2 ? 2 : 1 ))
PLATFORMS=("$@")
[ ${#PLATFORMS[@]} -gt 0 ] || PLATFORMS=(fabric-cft fabric-bft drunix)

IFS=, read -r -a CONFIG_LIST <<< "$CONFIGS"
for c in "${CONFIG_LIST[@]}"; do [ -f "$c" ] || { echo "no such config: $c" >&2; exit 1; }; done
for p in "${PLATFORMS[@]}"; do [ -f "deploy/docker/$p/up.sh" ] || { echo "no deploy script for platform: $p" >&2; exit 1; }; done

CAMPAIGN_START="$(date +%Y-%m-%dT%H:%M:%S%:z)"
CAMPAIGN_DIR="$ROOT/results/_campaigns/$(date -u +%Y%m%dT%H%M%SZ)-${PROFILE}"
mkdir -p "$CAMPAIGN_DIR"
exec > >(tee -a "$CAMPAIGN_DIR/run-all.log") 2>&1

echo "==================== preflight (${PROFILE}) ===================="
pf=0
bash scripts/preflight.sh "$PROFILE" || pf=$?
[ "$pf" -ne 1 ] || { echo "preflight found blockers; not running"; exit 1; }
[ "$pf" -ne 2 ] || echo "!! preflight warnings above - numbers from this host may not be trustworthy"

BR="$ROOT/bin/benchrunner"
go build -o "$BR" ./cmd/benchrunner

isolate() {
  echo "-- isolation: prune stopped containers + drop page cache"
  docker container prune -f >/dev/null 2>&1 || true
  docker network prune -f >/dev/null 2>&1 || true
  sync
  # -n: never block an unattended run on a password prompt.
  sudo -n sh -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null \
    || echo "   (page cache not dropped: needs passwordless sudo)"
  sleep 3
}

FAILED=()
for CONFIG in "${CONFIG_LIST[@]}"; do
  for p in "${PLATFORMS[@]}"; do
    echo "==================== $p : $CONFIG ===================="
    isolate

    echo "-- deploy $p"
    if ! bash "deploy/docker/$p/up.sh" "$PROFILE"; then
      echo "!! deploy failed for $p"
      FAILED+=("$p:$CONFIG:deploy")
      bash "deploy/docker/$p/down.sh" "$PROFILE" || true
      continue
    fi

    ENVF="deploy/docker/$p/connection.env"
    if [ ! -f "$ENVF" ]; then
      echo "!! $ENVF missing - deploy did not emit connection info"
      FAILED+=("$p:$CONFIG:deploy")
      bash "deploy/docker/$p/down.sh" "$PROFILE" || true
      continue
    fi

    echo "-- run $CONFIG on $p"
    # Subshell: one platform's connection.env must not leak into the next.
    # shellcheck disable=SC1090  # generated per platform by its up.sh
    if ! ( set -a; . "$ENVF"; set +a
           "$BR" run --config "$CONFIG" --platform "$p" --profile "$PROFILE" ); then
      echo "!! run failed for $p"
      FAILED+=("$p:$CONFIG:run")
    fi

    echo "-- teardown $p"
    bash "deploy/docker/$p/down.sh" "$PROFILE" || echo "!! teardown reported an error for $p"
  done
done

echo "==================== report ===================="
"$BR" report --results-dir "$ROOT/results" --output "$CAMPAIGN_DIR/comparison.html" --since "$CAMPAIGN_START"
echo "wrote $CAMPAIGN_DIR/comparison.html (this campaign's runs only)"

if [ ${#FAILED[@]} -gt 0 ]; then
  echo "!! failures: ${FAILED[*]}"
  exit 1
fi
