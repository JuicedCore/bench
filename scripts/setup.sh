#!/usr/bin/env bash
# One-shot local setup: preflight the host, build benchrunner, start monitoring.
#
#   scripts/setup.sh [profile]        # default: local
#
# On a host smaller than 16 GB, preflight will refuse `local` and name a profile
# that fits (typically local-small); pass it here.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Tooling AND host-capacity checks live in preflight.sh so run-all.sh and a fresh
# clone can use the same gate. Exit 2 means runnable-but-degraded, which is fine
# for setup; only a hard blocker (exit 1) stops us.
PROFILE="${1:-local}"
# `|| pf=$?` keeps set -e from aborting on preflight's deliberate exit 2.
pf=0
bash "$ROOT/scripts/preflight.sh" "$PROFILE" || pf=$?
[ "$pf" -ne 1 ] || { echo; echo "fix the blockers above, then re-run"; exit 1; }
echo

echo "== build benchrunner =="
go build -o "$ROOT/bin/benchrunner" ./cmd/benchrunner
echo "  -> $ROOT/bin/benchrunner"

echo "== unit tests =="
go test ./pkg/... >/dev/null && echo "  ok"

echo "== monitoring stack =="
bash deploy/docker/monitoring/up.sh || echo "  (monitoring failed to start; runs still work, system_metrics just won't scrape)"

cat <<EOF

next (profile: ${PROFILE}):
  1. bring up a platform:   ./bin/benchrunner setup --platform fabric-cft --profile ${PROFILE}
     (or directly:          bash deploy/docker/fabric-cft/up.sh ${PROFILE} )
  2. load its connection:   set -a; source deploy/docker/fabric-cft/connection.env; set +a
  3. smoke test:            ./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft --profile ${PROFILE}
  4. full methodology:      ./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft --profile ${PROFILE}
  5. tear down:             ./bin/benchrunner teardown --platform fabric-cft
EOF
