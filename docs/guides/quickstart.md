# Quickstart (local, ~15 min + image pull)

Prereqs: Go 1.26+, Docker + compose plugin, `git`, `curl`, `jq`, `python3`.

## 1. Build + monitoring

```
scripts/setup.sh
```

Builds `bin/benchrunner`, runs unit tests, starts Prometheus/Grafana/cAdvisor/
node_exporter. Grafana: http://localhost:3000 (admin / bench).

## 2. Prove the pipeline with the mock adapter (no network)

```
mkdir -p /tmp/bench-mock
cat > /tmp/bench-mock/run.yaml <<'EOF'
name: mock-smoke
platform: mock
workload: kv-write
profile: local
load: { mode: open-loop, target_tps: 200, hold_duration: 10s, key_space: 1000 }
metrics: { warmup: 2s, cooldown: 1s, output_dir: ./results }
adapter: { submit_ms: 1, commit_ms: 20, jitter_ms: 5 }
EOF
./bin/benchrunner run --config /tmp/bench-mock/run.yaml
cat results/mock/*/summary.txt
```

You should see confirmed TPS near 200, `invariant_ok=true`, e2e p50 ≈ 20–30 ms.

## 3. Real platform: Fabric CFT

```
./bin/benchrunner setup --platform fabric-cft --profile local
# equivalently: bash deploy/docker/fabric-cft/up.sh local

set -a; source deploy/docker/fabric-cft/connection.env; set +a

./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft
cat results/fabric-cft/*/summary.txt
```

First `setup` pulls ~1 GB of Docker images.

## 4. Drunix (needs a source checkout)

```
export BENCH_DRUNIX_REPO=/path/to/drunix     # or a git URL
./bin/benchrunner setup --platform drunix --profile local
set -a; source deploy/docker/drunix/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform drunix
```

## 5. Real methodology run

```
set -a; source deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft
python3 scripts/plot.py results/fabric-cft/<timestamp>/phases.csv
```

## 6. Compare platforms with isolation

```
scripts/run-all.sh configs/normalized/probe-sweep.yaml local fabric-cft drunix
open docs/reports/comparison.html
```

## 7. Tear down

One platform:

```
./bin/benchrunner teardown --platform fabric-cft
bash deploy/docker/monitoring/down.sh
```

Everything at once:

```
make down-all        # stop + remove all platforms + monitoring (keeps images/caches/results)
make clean           # + wipe caches, connection.env, results, generated reports (prompts)
make clean-images    # + remove the pulled platform images (~4-6 GB)
```
