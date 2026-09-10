# Unified Blockchain Benchmark Harness

GO       ?= go
BIN      := bin/benchrunner
PKG      := ./...

.PHONY: all build test vet fmt tidy smoke clean clean-images monitoring-up monitoring-down \
        chaincode integration up-all down-all help

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

up-all: build ## monitoring + one Fabric-family net + fabricx/neuchain if their images exist
	bash scripts/up-all.sh local $(FABRIC)

down-all: ## stop + remove every platform + monitoring (keeps images/caches/results)
	bash scripts/down-all.sh local

clean: ## FULL local wipe (containers, volumes, caches, connection.env, results); prompts
	bash scripts/clean.sh

clean-images: ## clean + also remove pulled platform images (~4-6 GB)
	bash scripts/clean.sh --images

clean-build: ## just remove local build artifacts (bin/, generated reports)
	rm -rf bin docs/reports/*.html docs/reports/*.png

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'
