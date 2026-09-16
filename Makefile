# Unified Blockchain Benchmark Harness

GO       ?= go
BIN      := bin/benchrunner
PKG      := ./...

# Campaign settings, overridable: make bench PROFILE=local-small PLATFORMS="fabric-cft drunix"
PROFILE   ?= local
PLATFORMS ?= fabric-cft fabric-bft drunix
CONFIGS   ?= configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml
SINCE     ?= 24h
PROJECT   ?=
GCP_PROFILE ?= gcp-small

.PHONY: all build test vet fmt tidy smoke clean clean-images monitoring-up monitoring-down \
        chaincode integration up-all down-all help deps preflight bench report runbook images-export images-import lint \
        gcp-plan gcp

all: build test vet ## build + test + vet

build: ## build the benchrunner CLI
	GOFLAGS=-buildvcs=false $(GO) build -o $(BIN) ./cmd/benchrunner

test: ## run unit tests
	$(GO) test $(PKG)

vet: ## go vet
	$(GO) vet $(PKG)

fmt: ## gofmt -s -w (tracked files only - never the upstream sources under .cache/)
	gofmt -s -w $(shell git ls-files '*.go')

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

deps: ## install Docker, Go, and tools on this Linux host (sudo)
	sudo scripts/install-deps.sh

preflight: ## check this host can run PROFILE
	bash scripts/preflight.sh $(PROFILE)

bench: ## deploy, run CONFIGS on PLATFORMS sequentially, report (PROFILE, PLATFORMS, CONFIGS)
	bash scripts/run-all.sh $(CONFIGS) $(PROFILE) $(PLATFORMS)

report: build ## comparison report of runs since SINCE (date, RFC3339, or duration)
	./$(BIN) report --results-dir results --output docs/reports/comparison.html --since $(SINCE)
	@echo "wrote docs/reports/comparison.html"

runbook: build ## rebuild results/index.html: one page per run (also rebuilt after every run)
	./$(BIN) runbook --results-dir results
	@echo "open results/index.html"

images-export: ## save images a clone cannot pull (bench/neuchain:ev) to images/; ALL=1 adds pinned public images
	bash scripts/images.sh export $(if $(ALL),--all) images

images-import: ## load images/ saved by images-export on another machine
	bash scripts/images.sh import images

lint: ## shellcheck + terraform fmt/validate (uses local tools, else their Docker images)
	@if command -v shellcheck >/dev/null; then SC=shellcheck; else SC="docker run --rm -v $(CURDIR):/mnt -w /mnt koalaman/shellcheck:stable"; fi; \
	  $$SC -S warning $$(git ls-files '*.sh')
	@if command -v terraform >/dev/null; then \
	  terraform -chdir=deploy/terraform fmt -check -recursive && \
	  terraform -chdir=deploy/terraform init -backend=false -input=false >/dev/null && terraform -chdir=deploy/terraform validate; \
	else \
	  docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR):/repo -w /repo/deploy/terraform --entrypoint sh hashicorp/terraform:1.9 -c \
	    'terraform fmt -check -recursive && terraform init -backend=false -input=false >/dev/null && terraform validate; rc=$$?; rm -rf .terraform; exit $$rc'; \
	fi

gcp-plan: ## print what a GCP campaign would do (PROJECT, GCP_PROFILE, PLATFORMS)
	bash scripts/gcp-run.sh --dry-run --profile $(GCP_PROFILE) --platforms "$(PLATFORMS)" $(if $(PROJECT),--project $(PROJECT))

gcp: ## run a GCP campaign: fresh VMs per platform, destroyed after (PROJECT, GCP_PROFILE, PLATFORMS)
	bash scripts/gcp-run.sh --profile $(GCP_PROFILE) --platforms "$(PLATFORMS)" $(if $(PROJECT),--project $(PROJECT))

integration: build ## run integration tests (needs BENCH_ADAPTER_* env from a live network)
	$(GO) test -tags integration -run Integration -v ./pkg/adapters/...

monitoring-up: ## start Prometheus/Grafana/cAdvisor/node_exporter
	bash deploy/docker/monitoring/up.sh

monitoring-down:
	bash deploy/docker/monitoring/down.sh

up-all: build ## monitoring + one Fabric-family net + fabricx/neuchain if their images exist
	bash scripts/up-all.sh $(PROFILE) $(FABRIC)

down-all: ## stop + remove every platform + monitoring (keeps images/caches/results)
	bash scripts/down-all.sh $(PROFILE)

clean: ## FULL local wipe (containers, volumes, caches, connection.env, results); prompts
	bash scripts/clean.sh

clean-images: ## clean + also remove pulled platform images (~4-6 GB)
	bash scripts/clean.sh --images

clean-build: ## just remove local build artifacts (bin/, generated reports)
	rm -rf bin docs/reports/*.html docs/reports/*.png

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'
