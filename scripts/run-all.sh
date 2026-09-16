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
# Use configs/native/<platform>.yaml (or the directory configs/native) for
# per-platform tuned kv-write runs; those files are not crossed onto other
# platforms. Use configs/native-kv-mixed for the same levers with kv-mixed.
#
# Default platforms: fabric-cft fabric-bft drunix. fabricx works (verified on
# local-small) but compiles from source on first deploy, and neuchain needs its
# image built first (see docs/REMAINING-WORK.md); name them explicitly to include
# them.
#
# Every platform is deployed fresh for every config and torn down afterwards. The
# log and a comparison report covering only this campaign's runs go to
# results/_campaigns/<timestamp>-<profile>/. Exits non-zero if any deploy or run
# failed.
#
# Per-(platform,config) deploy/run/teardown logs and a pre-teardown container
# capture land under results/_campaigns/<...>/<platform>/<config>/, and every
# step's outcome is appended as a row to results/_campaigns/<...>/SUMMARY.tsv.
# See docs/guides/running-benchmarks.md#logs-and-failure-captures.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/campaign-lib.sh
source "$ROOT/scripts/campaign-lib.sh"
LOG_TAG=run-all
# shellcheck source=scripts/log-lib.sh
source "$ROOT/scripts/log-lib.sh"

[ $# -ge 1 ] || die "usage: run-all.sh <config.yaml[,config.yaml...]> [profile] [platform ...]"
CONFIGS="$1"
PROFILE="${2:-local}"
shift $(( $# >= 2 ? 2 : 1 ))
PLATFORMS=("$@")
[ ${#PLATFORMS[@]} -gt 0 ] || PLATFORMS=(fabric-cft fabric-bft drunix)

CONFIG_LIST=()
IFS=, read -r -a _cfg_parts <<< "$CONFIGS"
for part in "${_cfg_parts[@]}"; do
  if [ -d "$part" ]; then
    shopt -s nullglob
    _yaml_files=( "$part"/*.yaml )
    shopt -u nullglob
    [ ${#_yaml_files[@]} -gt 0 ] || die "no yaml configs in directory $part" "configs live in configs/normalized/, configs/native/, and configs/native-kv-mixed/"
    CONFIG_LIST+=("${_yaml_files[@]}")
  else
    CONFIG_LIST+=("$part")
  fi
done
for c in "${CONFIG_LIST[@]}"; do [ -f "$c" ] || die "no such config: $c" "configs live in configs/normalized/, configs/native/, and configs/native-kv-mixed/ (see CONFIGS.md)"; done
for p in "${PLATFORMS[@]}"; do
  [ -f "deploy/docker/$p/up.sh" ] || die "no deploy script for platform: $p" \
    "available: $(ls deploy/docker/*/up.sh | cut -d/ -f3 | grep -v monitoring | tr '\n' ' ')"
done
[ -f "deploy/profiles/${PROFILE}.yaml" ] || die "no such profile: ${PROFILE}" \
  "available: $(ls deploy/profiles/*.yaml | xargs -n1 basename | sed 's/\.yaml$//' | tr '\n' ' ')"
need go; need docker

CAMPAIGN_START="$(date +%Y-%m-%dT%H:%M:%S%:z)"
CAMPAIGN_DIR="$ROOT/results/_campaigns/$(date -u +%Y%m%dT%H%M%SZ)-${PROFILE}"
mkdir -p "$CAMPAIGN_DIR"
exec > >(tee -a "$CAMPAIGN_DIR/run-all.log") 2>&1

log "campaign dir: $CAMPAIGN_DIR"
echo "==================== preflight (${PROFILE}) ===================="
pf=0
bash scripts/preflight.sh "$PROFILE" || pf=$?
case "$pf" in
  0) ;;
  2) warn "preflight warnings above - numbers from this host may not be trustworthy" ;;
  1) die "preflight found blockers (above); not running" ;;
  *) die "preflight itself crashed (exit $pf) - not running on an unchecked host" "run: bash scripts/preflight.sh $PROFILE" ;;
esac

BR="$ROOT/bin/benchrunner"
# Cross-user WSL checkouts trip git's "dubious ownership" VCS stamp; build without it.
GOFLAGS="${GOFLAGS:--buildvcs=false}" go build -o "$BR" ./cmd/benchrunner \
  || die "go build of benchrunner failed (compiler output above)"

isolate() {
  echo "-- isolation: prune stopped containers + drop page cache"
  docker container prune -f >/dev/null 2>&1 || warn "docker container prune failed; leftover containers may share the host"
  docker network prune -f >/dev/null 2>&1 || true   # fails harmlessly while a network is in use
  sync
  # -n: never block an unattended run on a password prompt.
  sudo -n sh -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null \
    || echo "   (page cache not dropped: needs passwordless sudo)"
  sleep 3
}

FAILED=()
for CONFIG in "${CONFIG_LIST[@]}"; do
  cfg_norm="$(yaml_top "$CONFIG" normalized)"
  cfg_plat="$(yaml_top "$CONFIG" platform)"
  if [ "$cfg_norm" = "false" ]; then
    export BENCH_NORMALIZED=false
  else
    export BENCH_NORMALIZED=true
  fi
  for p in "${PLATFORMS[@]}"; do
    if [ "$cfg_norm" = "false" ] && [ -n "$cfg_plat" ] && [ "$cfg_plat" != "$p" ]; then
      continue
    fi
    echo "==================== $p : $CONFIG ===================="
    isolate

    DIR="$(step_dir "$CAMPAIGN_DIR" "$p" "$CONFIG")"
    SINCE="$(date --rfc-3339=seconds)"

    echo "-- deploy $p"
    if ! bash "deploy/docker/$p/up.sh" "$PROFILE" 2>&1 | tee "$DIR/deploy.log"; then
      warn "deploy failed for $p: $(reason_of "$DIR/deploy.log")"
      FAILED+=("$p:$CONFIG:deploy")
      bash scripts/capture.sh "$DIR/capture" "$SINCE"
      record "$CAMPAIGN_DIR" "$p" "$CONFIG" deploy deploy-failed "$(reason_of "$DIR/deploy.log")" "$DIR"
      bash "deploy/docker/$p/down.sh" "$PROFILE" 2>&1 | tee "$DIR/teardown.log" || true
      continue
    fi

    ENVF="deploy/docker/$p/connection.env"
    if [ ! -f "$ENVF" ]; then
      warn "$ENVF missing - deploy did not emit connection info"
      FAILED+=("$p:$CONFIG:deploy")
      bash scripts/capture.sh "$DIR/capture" "$SINCE"
      record "$CAMPAIGN_DIR" "$p" "$CONFIG" deploy deploy-failed "deploy did not emit connection.env" "$DIR"
      bash "deploy/docker/$p/down.sh" "$PROFILE" 2>&1 | tee "$DIR/teardown.log" || true
      continue
    fi

    echo "-- run $CONFIG on $p"
    # Subshell: one platform's connection.env must not leak into the next.
    set +e
    # shellcheck disable=SC1090  # generated per platform by its up.sh
    ( set -a; . "$ENVF"; set +a
      "$BR" run --config "$CONFIG" --platform "$p" --profile "$PROFILE" ) 2>&1 | tee "$DIR/run.log"
    rc=${PIPESTATUS[0]}
    set -e
    case "$rc" in
      0) if grep -q 'FAILED RUN' "$DIR/run.log"; then
           warn "run for $p completed but is not a measurement: $(reason_of "$DIR/run.log")"
           FAILED+=("$p:$CONFIG:no-measurement")
           record "$CAMPAIGN_DIR" "$p" "$CONFIG" run no-measurement "$(reason_of "$DIR/run.log")" "$DIR"
         else
           record "$CAMPAIGN_DIR" "$p" "$CONFIG" run ok "" "$DIR"
         fi ;;
      3) warn "platform container failed during run for $p"
         FAILED+=("$p:$CONFIG:container")
         record "$CAMPAIGN_DIR" "$p" "$CONFIG" run container-failed "$(reason_of "$DIR/run.log")" "$DIR" ;;
      *) warn "run failed for $p (exit $rc): $(reason_of "$DIR/run.log")"
         FAILED+=("$p:$CONFIG:run")
         record "$CAMPAIGN_DIR" "$p" "$CONFIG" run run-failed "$(reason_of "$DIR/run.log")" "$DIR" ;;
    esac

    echo "-- capture $p"
    bash scripts/capture.sh "$DIR/capture" "$SINCE"

    echo "-- teardown $p"
    bash "deploy/docker/$p/down.sh" "$PROFILE" 2>&1 | tee "$DIR/teardown.log" \
      || warn "teardown reported an error for $p (see $DIR/teardown.log); the next platform may start on a dirty host"
  done
done

echo "==================== report ===================="
if "$BR" report --results-dir "$ROOT/results" --output "$CAMPAIGN_DIR/comparison.html" --since "$CAMPAIGN_START"; then
  log "wrote $CAMPAIGN_DIR/comparison.html (this campaign's runs only)"
else
  warn "comparison report failed (above); per-run results are still under results/<platform>/"
  FAILED+=("report")
fi

print_summary "$CAMPAIGN_DIR"

if [ ${#FAILED[@]} -gt 0 ]; then
  warn "failures: ${FAILED[*]}"
  warn "triage: column -t -s \$'\\t' $CAMPAIGN_DIR/SUMMARY.tsv; then docs/README.md#troubleshooting"
  exit 1
fi
log "campaign finished with no failures"
