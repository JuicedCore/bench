# GCP infrastructure

Two stacks:

| Path | Lifetime | Contents |
| ---- | -------- | -------- |
| `deploy/terraform/` | one platform run, one workspace per platform | platform VM, load-generator VM, VPC + subnet, Cloud Router + NAT, IAP-only SSH and load-generator-to-platform firewall rules, VM service account |
| `deploy/terraform/shared/` | long-lived | results bucket (versioned, uniform access, public access prevented, `prevent_destroy`) |

Drive the per-platform stack with `scripts/gcp-run.sh` (or `make gcp`). It sets
`platform`, `profile` and the machine types from `deploy/profiles/<profile>.yaml`,
waits for provisioning, deploys, runs, collects results and destroys. Full guide,
IAM roles and org-policy notes: [docs/guides/gcp-deployment.md](../../docs/guides/gcp-deployment.md).

Files:

- `versions.tf`: Terraform >= 1.5, `hashicorp/google ~> 6.0`, GCS backend
  (configured at init from `backend.hcl`)
- `variables.tf`: every setting, with descriptions
- `main.tf`: resources
- `startup.sh.tftpl`: VM startup script; embeds `scripts/install-deps.sh`
- `outputs.tf`: VM names and private IPs, IAP SSH commands
- `backend.hcl.example`, `terraform.tfvars.example`: copy and fill in; the real
  files are gitignored
- `.terraform.lock.hcl`: provider checksums for linux/darwin amd64/arm64; commit
  updates to it

By hand, for inspection:

```bash
cd deploy/terraform
terraform init -backend-config=backend.hcl
terraform workspace select -or-create fabric-cft
terraform plan -var platform=fabric-cft
```

Validate without credentials: `make lint`.
