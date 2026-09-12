#!/usr/bin/env bash
# One-shot local setup: check tooling, build benchrunner, start monitoring.
#   scripts/setup.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "== tool check =="
missing=0
for t in go docker git curl jq python3; do
  if command -v "$t" >/dev/null 2>&1; then
    printf '  %-8s %s\n' "$t" "$($t --version 2>&1 | head -1)"
  else
    printf '  %-8s MISSING\n' "$t"; missing=1
  fi
done
docker compose version >/dev/null 2>&1 || { echo "  docker compose plugin MISSING"; missing=1; }
[ "$missing" = 0 ] || { echo "install the missing tools and re-run"; exit 1; }

echo "== build benchrunner =="
go build -o "$ROOT/bin/benchrunner" ./cmd/benchrunner
echo "  -> $ROOT/bin/benchrunner"

echo "== unit tests =="
go test ./pkg/... >/dev/null && echo "  ok"

echo "== monitoring stack =="
bash deploy/docker/monitoring/up.sh || echo "  (monitoring failed to start; runs still work, system_metrics just won't scrape)"

cat <<EOF

next:
  1. bring up a platform:   ./bin/benchrunner setup --platform fabric-cft --profile local
     (or directly:          bash deploy/docker/fabric-cft/up.sh local )
  2. load its connection:   set -a; source deploy/docker/fabric-cft/connection.env; set +a
  3. smoke test:            ./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft
  4. full methodology:      ./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft
  5. tear down:             ./bin/benchrunner teardown --platform fabric-cft
EOF
