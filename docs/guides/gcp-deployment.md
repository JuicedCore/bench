# GCP deployment (Phase 5)

> Status: **planned**. `deploy/terraform/` is a stub. This guide records the
> intended shape so Phase 1–4 work does not paint it into a corner.

## Topology per platform

| Role | Machine type | Count |
| ---- | ------------ | ----- |
| Blockchain nodes | `n2-standard-8` (8 vCPU / 32 GB) | 4 |
| Load generator | `n2-standard-4` | 1 (separate VM — never co-located) |
| Monitoring | `e2-standard-4` | 1 (shared across all platform runs) |

Profiles: `gcp-small` (single beefy VM per platform, fast iteration) and
`gcp-full` (the table above, for quotable numbers).

## Rules

- **Sequential runs** ([adr-005](../decisions/adr-005-sequential-runs.md)) — one
  platform at a time on identical machine types, full isolation between.
- Load generator on its own VM so its CPU never competes with the platform.
- Same `benchrunner` binary, same configs as local — only `deploy/` differs
  (Terraform + remote Docker / GKE instead of local Compose).
- The monitoring VM stays up across the whole campaign; Prometheus federates each
  platform's exporters.

## Intended flow

```
cd deploy/terraform
terraform apply -var platform=fabric-cft -var profile=gcp-full
# terraform brings up node VMs + loadgen VM, installs Docker, runs deploy/docker/<p>/up.sh remotely
ssh loadgen "cd bench && source connection.env && ./benchrunner run --config configs/normalized/probe-sweep.yaml --platform fabric-cft --profile gcp-full"
scp loadgen:bench/results/... ./results/
terraform destroy -var platform=fabric-cft
```

## Quotas

`gcp-full` peak is ~40 vCPU at a time (sequential), but request headroom:
~200 vCPU / 840 GB region quota if you want to stage the next platform while one
runs.

## Cost control

- `terraform destroy` between platforms; do not leave 4× `n2-standard-8` idle.
- `gcp-small` for methodology debugging; `gcp-full` only for the final campaign.
