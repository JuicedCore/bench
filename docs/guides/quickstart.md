# Quickstart on a Linux machine

About 15 minutes plus image pulls for the first platform.

## 1. Install and check the host

```bash
sudo scripts/install-deps.sh     # Docker + compose, Go at go.mod's version, git, jq, PyYAML, ...
newgrp docker                    # if the installer just added you to the docker group
scripts/preflight.sh local       # exit 0 ready, 2 runnable with warnings, 1 blocked
```

Supported: Debian, Ubuntu, Fedora, RHEL/Rocky/Alma, Arch. On anything else,
install the packages listed at the top of `scripts/install-deps.sh` yourself.

If `local` (16 GB) does not fit, preflight names a profile that does; use it for
every command below. `local-small` fits a 13–14 GB machine.

## 2. Build, test, and prove the pipeline without a network

```bash
make all smoke
```

`smoke` runs the mock adapter for 8 seconds. Expect confirmed TPS near 200,
`invariant_ok=true`, e2e p50 around 25 ms.

Optional monitoring (Prometheus, Grafana on http://localhost:3000, cAdvisor):

```bash
make monitoring-up
```

Without it runs still work; the per-run report just has no Prometheus charts.

## 3. One real platform

```bash
bash deploy/docker/fabric-cft/up.sh local-small
set -a; . deploy/docker/fabric-cft/connection.env; set +a
./bin/benchrunner run --config configs/normalized/quick-smoke.yaml --platform fabric-cft --profile local-small
cat results/fabric-cft/*/summary.txt
bash deploy/docker/fabric-cft/down.sh local-small
```

The first bring-up downloads the Fabric binaries and about 1 GB of images. Drunix
works the same way (`deploy/docker/drunix/up.sh`); its source is cloned at a
pinned commit automatically.

## 4. The comparison

```bash
make bench PROFILE=local-small
```

This runs quick-smoke and probe-sweep on fabric-cft, fabric-bft and drunix, each
deployed fresh and torn down in turn. It takes a little over an hour. Fabric-X is
opt-in because its first deploy compiles from source:
`make bench PROFILE=local-small PLATFORMS="fabric-cft fabric-bft drunix fabricx"`. The
comparison of this campaign's runs is written to
`results/_campaigns/<id>/comparison.html`, plus `SUMMARY.tsv` and per-step logs
(see [running-benchmarks.md#logs-and-failure-captures](running-benchmarks.md#logs-and-failure-captures)).
Choose what runs with `PLATFORMS="..."` and `CONFIGS=a.yaml,b.yaml`.

`summary.txt` in each run directory is the quickest read. A run that failed says
so: `PLATFORM FAILURE` lines, per-phase `errors`, and `HEADLINE none`. The
report lists it under "excluded" with the reason.
[interpreting-results](interpreting-results.md) explains the rest.

## 5. Clean up

```bash
make down-all        # stop every platform and monitoring (keeps images, caches, results)
make clean           # also caches, connection.env, results, reports (prompts)
make clean-images    # also the pulled platform images (~4-6 GB)
```
