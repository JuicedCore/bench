# GCP infrastructure (Phase 5)

`main.tf` provisions one platform's benchmark infra: N node VMs + 1 load-generator
VM + 1 monitoring VM. Infrastructure only — the blockchain is deployed on the
node VMs by `deploy/docker/<platform>/up.sh` over SSH.

## Usage

```
cd deploy/terraform
terraform init

# one platform at a time (adr-005 - sequential runs)
terraform apply -var project=YOUR_GCP_PROJECT -var platform=fabric-cft -var profile=gcp-full

# ... deploy blockchain on nodes, run benchmark from the loadgen VM ...

terraform destroy -var project=YOUR_GCP_PROJECT -var platform=fabric-cft
```

Keep the monitoring VM between platforms with `-var keep_monitoring=true` (bring
it up once with the first platform without the flag).

## Key variables

| var | default | notes |
| --- | ------- | ----- |
| `project` | — | required |
| `platform` | — | `fabric-cft` \| `fabric-bft` \| `drunix` \| `fabricx` \| `neuchain` |
| `profile` | `gcp-full` | matches `deploy/profiles/<profile>.yaml` |
| `node_count` | 4 | blockchain node VMs |
| `node_machine_type` | `n2-standard-8` | 8 vCPU / 32 GB |
| `loadgen_machine_type` | `n2-standard-4` | separate VM, never co-located |
| `node_disk_gb` | 200 | pd-ssd |
| `keep_monitoring` | false | set true to reuse an existing monitoring VM |
| `ssh_pubkey_path` | `~/.ssh/id_ed25519.pub` | injected for `ssh_user` (`bench`) |

## Quotas

`gcp-full` peak is ~40 vCPU at a time (sequential). Request headroom
(~200 vCPU / 840 GB regional) if staging the next platform while one runs.
Tighten `google_compute_firewall.ssh.source_ranges` to your IP before real use.
