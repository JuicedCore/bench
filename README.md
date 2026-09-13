# Unified Blockchain Benchmark Harness

Fair, platform-agnostic throughput and latency benchmarking of permissioned
blockchains with different transaction models:

| Platform | Model | Consensus |
| -------- | ----- | --------- |
| **Hyperledger Fabric** (`fabric-cft`, `fabric-bft`) | EOV + chaincode | Raft / SmartBFT |
| **Drunix** (NPCI fork of Fabric 2.5.x) | EOV + chaincode | Raft (CFT only) |
| **Fabric-X** (`fabricx`) | EOV, namespace read/write sets | Arma (sharded BFT) |
| **NeuChain** (`neuchain`) | Ordering-free execute-validate | deterministic |

One adapter interface, one load generator, one metrics pipeline. Normalized
workloads give the cross-platform comparison; platform-native workloads give
ceilings. Every methodology decision is written down in [`docs/`](docs/README.md).

It runs the same way on a Linux workstation and on GCP: the cloud VMs are
provisioned by the same installer, run the same deploy scripts, and use the same
configs and profiles.

## Status

| Platform | Live-verified | Notes |
| -------- | ------------- | ----- |
| `fabric-cft` | ✅ smoke + probe-sweep (2.5.16) | local-small: knee ~1000 TPS, hold 894 TPS |
| `fabric-bft` | ✅ smoke + probe-sweep (3.1.5, 4 orderers) | local-small: knee ~500 TPS, hold 450 TPS |
| `drunix` | ✅ smoke; probe-sweep to ~500 TPS | a Committing Peer exited under 2000 TPS on local-small (cause not yet captured); runs on YugabyteDB, so normalized runs carry a state-DB caveat |
| `fabricx` | ⚠️ deploy and adapter written, never run live | [docs/REMAINING-WORK.md §4](docs/REMAINING-WORK.md) lists what the first bring-up must confirm |
| `neuchain` | ⚠️ adapter unit-tested; server image not built | needs a 45–90 min C++ build (`deploy/docker/neuchain/build.sh`) |
| GCP | ✅ Terraform validated, installer tested on 5 distros | not yet applied against a real project |

Details: [docs/REMAINING-WORK.md](docs/REMAINING-WORK.md).

## On a Linux machine

```bash
git clone <this repo> bench && cd bench
sudo scripts/install-deps.sh        # Docker + compose, Go (from go.mod), jq, PyYAML...
newgrp docker                       # or log out and in, if you were just added to the group
scripts/preflight.sh local          # can THIS host run the profile? names one that fits if not
make all smoke                      # build, unit tests, a mock benchmark with no network
```

`install-deps.sh` supports Debian, Ubuntu, Fedora, RHEL/Rocky/Alma and Arch, and
is safe to re-run. `preflight.sh` checks tooling and whether the host has the
CPU, RAM and disk the profile budgets: a run on an over-committed host measures
swap, not the platform.

Run the comparison: each platform deployed fresh for each config, strictly one at
a time, torn down after:

```bash
make bench PROFILE=local-small      # quick-smoke + probe-sweep on fabric-cft, fabric-bft, drunix
# or
scripts/run-all.sh configs/normalized/quick-smoke.yaml,configs/normalized/probe-sweep.yaml \
    local-small fabric-cft fabric-bft drunix
```

Results land in `results/<platform>/<timestamp>/` (`summary.txt`, `result.json`,
`manifest.json`, `phases.csv`, a per-run HTML report). The campaign log and a
comparison of just that campaign's runs go to `results/_campaigns/<id>/`.

One platform by hand:

```bash
bash deploy/docker/fabric-cft/up.sh local-small
set -a; . deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft --profile local-small
bash deploy/docker/fabric-cft/down.sh local-small
```

The Fabric-family platforms share ports, so only one can be up at a time;
`run-all.sh` sequences them. Walkthrough: [docs/guides/quickstart.md](docs/guides/quickstart.md).

## On GCP

```bash
cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars   # project, labels, ...
cp deploy/terraform/backend.hcl.example      deploy/terraform/backend.hcl        # state bucket
make gcp-plan GCP_PROFILE=gcp-full          # prints the plan, touches nothing
make gcp      GCP_PROFILE=gcp-full          # runs it
```

Per platform, `scripts/gcp-run.sh` creates a platform VM and a separate
load-generator VM (no public IPs, IAP-only SSH, Cloud NAT, OS Login, Shielded VM,
dedicated least-privilege service account), deploys and runs every config, pulls
the results back, and destroys the VMs, also on failure or Ctrl-C. Setup,
required IAM roles, org-policy compatibility and cost:
[docs/guides/gcp-deployment.md](docs/guides/gcp-deployment.md).

## Profiles

A profile (`deploy/profiles/`) is the resource budget the platform gets. Runs are
comparable within a profile, never across profiles.

| Profile | Where | Platform budget | Load generator |
| ------- | ----- | --------------- | -------------- |
| `local-small` | 13–14 GB workstation | 8 CPU / 8 GB | 2 CPU, same host |
| `local` | 16 GB workstation | 11 CPU / 11 GB | same host |
| `gcp-small` | `n2-standard-16` | 13 CPU / 52 GB | `n2-standard-8` VM |
| `gcp-full` | `n2-standard-32` | 28 CPU / 112 GB | `n2-standard-16` VM |

The budget is the same total for every platform. CPU is split evenly across a
platform's containers; memory by one role-weight table shared by all platforms
(`deploy/docker/lib.sh`), because an even split OOM-killed the busiest
containers. The manifest records every container's limits.

## What makes a result trustworthy

- **T1/T2/T3**: submit entered / platform acknowledged / committed in a block.
  Throughput counts T3; latency is T3 − *scheduled* send, so a backlog cannot
  hide overload ([metrics-methodology](docs/architecture/metrics-methodology.md)).
- **Probe and sweep**: floor latency, step up to saturation, hold at 90% of the
  measured knee.
- **Fairness by construction**: normalized runs share state DB, orderer batch
  parameters, seed, windows and total hardware; native metrics are never compared
  ([fairness-guarantees](docs/architecture/fairness-guarantees.md)).
- **Failures are failures**: a container that exits or is OOM-killed stops the
  run and excludes it from the comparison, and each phase records why its
  transactions failed.
- **Reproducible**: upstream sources are pinned by commit or tag, and every run
  writes a manifest with every fairness lever, versions, the harness commit, and
  caveats.

## Layout

```
cmd/benchrunner/     CLI: run | suite | report | setup | teardown | list
pkg/adapters/        PlatformAdapter interface; fabric, drunix, fabricx, neuchain, mock
pkg/workloads/       normalized workloads: kv-write, kv-read, kv-mixed, transfer
pkg/loadgen/         key distributions + open/closed-loop generator (coordinated-omission safe)
pkg/metrics/         per-tx collector, HDR histograms, container sampler + failure detection
pkg/harness/         run config, profiles, engine, manifest, reporter, comparison report
chaincodes/kvstore/  Go chaincode for the Fabric-family normalized workloads
configs/normalized/  the cross-platform run definitions
deploy/docker/       per-platform up.sh/down.sh, shared lib.sh, monitoring stack
deploy/profiles/     resource budgets (and GCP machine types)
deploy/terraform/    GCP stack per platform run; shared/ for the results bucket
scripts/             install-deps, preflight, run-all, gcp-run, setup, up/down-all, clean
docs/                architecture, platforms, workloads, ADRs, guides, REMAINING-WORK
```

## Development

```bash
make all      # build + test + vet
make lint     # shellcheck + terraform fmt/validate (falls back to their Docker images)
make help     # every target
```

CI (`.github/workflows/ci.yml`) runs the Go checks, a mock benchmark, shellcheck,
Terraform validation, and the installer in clean Debian, Ubuntu, Fedora, Rocky
and Arch containers.
