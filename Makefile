# Unified Blockchain Benchmark Harness

GO       ?= go
BIN      := bin/benchrunner
PKG      := ./...

.PHONY: all build test vet fmt tidy smoke clean monitoring-up monitoring-down \
        chaincode integration help

all: build test vet ## build + test + vet

build: ## build the benchrunner CLI
	$(GO) build -o $(BIN) ./cmd/benchrunner

test: ## run unit tests
	$(GO) test $(PKG)

vet: ## go vet
	$(GO) vet $(PKG)

fmt: ## gofmt -s -w
	gofmt -s -w $(shell find . -name '*.go' -not -path './*/vendor/*')

tidy: ## go mod tidy (root + chaincode)
	$(GO) mod tidy
	cd chaincodes/kvstore && $(GO) mod tidy

chaincode: ## build the kvstore chaincode module
	cd chaincodes/kvstore && $(GO) build ./...

smoke: build ## run a no-network mock benchmark
	@mkdir -p /tmp/bench-mock
	@printf 'name: mock-smoke\nplatform: mock\nworkload: kv-mixed\nprofile: local\nload: {mode: open-loop, target_tps: 200, hold_duration: 8s, key_space: 2000, read_write_ratio: 0.4}\nmetrics: {warmup: 2s, cooldown: 1s, output_dir: /tmp/bench-mock/results}\nadapter: {submit_ms: 1, commit_ms: 20, jitter_ms: 5, conflict_rate: 0.03}\n' > /tmp/bench-mock/run.yaml
	./$(BIN) run --config /tmp/bench-mock/run.yaml
	@cat /tmp/bench-mock/results/mock/*/summary.txt

integration: build ## run integration tests (needs BENCH_ADAPTER_* env from a live network)
	$(GO) test -tags integration -run Integration -v ./pkg/adapters/...

monitoring-up: ## start Prometheus/Grafana/cAdvisor/node_exporter
	bash deploy/docker/monitoring/up.sh

monitoring-down:
	bash deploy/docker/monitoring/down.sh

clean:
	rm -rf bin results/*/ docs/reports/*.html docs/reports/*.png
	find deploy/docker -maxdepth 2 -name connection.env -delete

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'
