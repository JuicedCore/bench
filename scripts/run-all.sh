#!/usr/bin/env bash
# Run one config across several platforms SEQUENTIALLY with full inter-run
# isolation (docs/architecture/fairness-guarantees.md, adr-005).
#
#   scripts/run-all.sh <config.yaml> [profile] [platform ...]
#
# Pass a config from configs/normalized/ for a comparison run: those files are
# platform-agnostic by construction (one file, all five platforms), which is what
# makes running the same bytes against each platform meaningful. A config from
# configs/native/ is per-platform and belongs to one platform only.
#
# Default platforms: fabric-cft fabric-bft drunix - the ones with working
# deploys today. fabricx and neuchain accept the same normalized configs but are
# blocked at the platform level (see docs/REMAINING-WORK.md); add them here once
# their networks come up.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

CONFIG="${1:?usage: run-all.sh <config.yaml> [profile] [platform ...]}"
PROFILE="${2:-local}"
shift $(( $# >= 2 ? 2 : 1 )) || true
PLATFORMS=("$@")
[ ${#PLATFORMS[@]} -gt 0 ] || PLATFORMS=(fabric-cft fabric-bft drunix)

BR="$ROOT/bin/benchrunner"
[ -x "$BR" ] || go build -o "$BR" ./cmd/benchrunner

isolate() {
  echo "-- isolation: prune + drop caches"
  docker system prune -f >/dev/null 2>&1 || true
  sync
  sudo sh -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null || sync
  sleep 3
}

for p in "${PLATFORMS[@]}"; do
  echo "==================== $p ===================="
  isolate

  echo "-- deploy $p"
  bash "deploy/docker/$p/up.sh" "$PROFILE"

  ENVF="deploy/docker/$p/connection.env"
  [ -f "$ENVF" ] || { echo "no $ENVF - deploy did not emit connection info"; exit 1; }
  set -a; # shellcheck disable=SC1090
  source "$ENVF"; set +a

  echo "-- run $CONFIG on $p"
  "$BR" run --config "$CONFIG" --platform "$p" --profile "$PROFILE" \
    || echo "!! run failed for $p (continuing)"

  echo "-- teardown $p"
  bash "deploy/docker/$p/down.sh" "$PROFILE"
done

echo "==================== report ===================="
"$BR" report --results-dir "$ROOT/results" --output "$ROOT/docs/reports/comparison.html"
echo "wrote docs/reports/comparison.html"
