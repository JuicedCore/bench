# Running a campaign on GCP

`scripts/gcp-run.sh` runs a benchmark campaign in a GCP project: for each platform
in turn it creates a fresh pair of VMs, deploys the platform, runs every config,
copies the results back, and destroys the VMs. The platforms, configs and
profiles are the same files used locally.

## Topology

```
                  operator workstation  (terraform, gcloud)
                        │  IAP TCP forwarding (SSH), no public IPs
        ┌───────────────┴────────────────────────────────┐
        │  VPC  bench-<platform>   10.42.0.0/24          │
        │                                                │
        │  ┌───────────────────┐   platform ports        │
        │  │ loadgen VM        │ ─────────────────────▶  ┌────────────────────────┐
        │  │ benchrunner       │   Prometheus :9090      │ platform (SUT) VM      │
        │  │                   │   Docker API :2376 mTLS │ platform containers    │
        │  └───────────────────┘                         │ Prometheus, cAdvisor   │
        │                                                └────────────────────────┘
        │        Cloud NAT ──▶ image and source downloads │
        └────────────────────────────────────────────────┘
```

| Role | `gcp-small` | `gcp-full` | Why separate |
| ---- | ----------- | ---------- | ------------ |
| Platform VM | `n2-standard-16` | `n2-standard-32` | runs the whole platform plus monitoring |
| Load generator VM | `n2-standard-8` | `n2-standard-16` | its CPU must never compete with the platform |

The profile's `gcp:` block names the machine types, and its `budget:` is what the
platform's containers are limited to. The budget leaves headroom on the platform
VM for the OS, Docker and the monitoring stack, so the limits are never
overcommitted. Both VMs sit in one zone.

Benchrunner on the load generator reads the platform VM's Docker API over mutual
TLS. That is how container sampling and OOM or exit detection keep working
when the load generator is a different machine. The CA key is generated on the
operator's machine for each platform run, is never uploaded, and is deleted when
the script exits. The API listens only on the platform VM's private IP, and the
firewall admits only the load generator's network tag.

### One platform VM, not several

The deploy scripts bring each platform up on a single Docker host, so every
platform is measured on one large VM. That is fair across platforms (same machine,
same budget) and is what the local numbers are measured on too. It is not how
Fabric-X (Arma) or NeuChain are meant to be deployed: both scale out across hosts,
so single-VM figures for them are not comparable to their published multi-host
ceilings. Spreading a platform across VMs needs per-platform multi-host deploy
work (overlay networking, per-host crypto distribution) that does not exist yet.

## One-time setup

### 1. Tools on the operator machine

`terraform` >= 1.5, `gcloud` (authenticated: `gcloud auth login` and
`gcloud auth application-default login`), `openssl`, `python3` with PyYAML. Go is
optional; with it, the campaign's comparison report is built locally at the end.

### 2. Project APIs

```bash
gcloud services enable compute.googleapis.com iap.googleapis.com oslogin.googleapis.com \
  iam.googleapis.com cloudresourcemanager.googleapis.com storage.googleapis.com \
  logging.googleapis.com monitoring.googleapis.com --project "$PROJECT"
```

### 3. IAM for whoever runs the campaign

| Role | For |
| ---- | --- |
| `roles/compute.instanceAdmin.v1` | create and delete the VMs |
| `roles/compute.networkAdmin` | VPC, subnet, Cloud Router and NAT |
| `roles/compute.securityAdmin` | firewall rules |
| `roles/iam.serviceAccountAdmin` + `roles/resourcemanager.projectIamAdmin` | create the VM service account and grant it log and metric writer. Not needed if you pass an existing `service_account_email` |
| `roles/iam.serviceAccountUser` | attach the service account to the VMs |
| `roles/iap.tunnelResourceAccessor` | SSH through IAP |
| `roles/compute.osAdminLogin` | log in with sudo (Docker setup, deploy scripts) |
| `roles/storage.objectAdmin` on the state bucket (and the results bucket) | Terraform state, result archive |

For CI, grant these to a dedicated service account and run the script with it.

### 4. Terraform state bucket

```bash
gcloud storage buckets create gs://my-org-terraform-state --project "$PROJECT" \
  --location us-central1 --uniform-bucket-level-access --public-access-prevention
gcloud storage buckets update gs://my-org-terraform-state --versioning
cp deploy/terraform/backend.hcl.example deploy/terraform/backend.hcl   # set bucket and prefix
```

Each platform gets its own Terraform workspace under that prefix. For a
throwaway trial, `--local-state` keeps state on your machine instead.

### 5. Settings

```bash
cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars
```

Set `project`, `region` and `zone`, and your labels. Optional settings:

- `network` / `subnetwork`: an existing Shared VPC subnet instead of a dedicated
  VPC. Set `create_nat = false` if it already has egress.
- `service_account_email`: an existing VM service account.
- `kms_key_self_link`: CMEK for the boot disks.
- `min_cpu_platform`: pins the CPU generation. Recommended for campaigns you
  intend to compare months apart.

`backend.hcl` and `terraform.tfvars` are gitignored.

### 6. Results bucket (optional)

The bucket lives in its own long-lived stack so a platform's `destroy` can never
touch it:

```bash
cd deploy/terraform/shared
terraform init -backend-config=../backend.hcl -backend-config="prefix=bench/shared"
terraform apply -var project="$PROJECT" -var results_bucket=my-org-bench-results
```

It has uniform access, public access prevention, versioning, optional CMEK and
retention, and `prevent_destroy`.

### Organization policies

The defaults already satisfy the common constraints:

| Constraint | How |
| ---------- | --- |
| `compute.vmExternalIpAccess` | no VM has an external IP |
| `compute.requireOsLogin` | `enable-oslogin=TRUE`, project SSH keys blocked |
| `compute.requireShieldedVm` | Secure Boot, vTPM, integrity monitoring on |
| `gcp.restrictNonCmekServices` | set `kms_key_self_link` |
| `iam.automaticIamGrantsForDefaultServiceAccounts` | the default compute SA is not used |
| `compute.disableSerialPortAccess` | serial port is not used |

The VMs need egress to `download.docker.com`, `go.dev`, `github.com`,
`proxy.golang.org`, Docker Hub and `ghcr.io` (images and pinned sources). If
egress is restricted to an allowlist, open those.

## Running

```bash
make gcp-plan GCP_PROFILE=gcp-full PLATFORMS="fabric-cft fabric-bft drunix"   # dry run
make gcp      GCP_PROFILE=gcp-full PLATFORMS="fabric-cft fabric-bft drunix"
```

Or directly:

```bash
scripts/gcp-run.sh --profile gcp-full \
  --platforms "fabric-cft fabric-bft drunix" \
  --configs "configs/normalized/quick-smoke.yaml configs/normalized/probe-sweep.yaml" \
  --results-bucket my-org-bench-results
```

For each platform the script:

1. applies the stack in the platform's workspace
2. waits for both VMs to finish `scripts/install-deps.sh --tune` (their startup
   script)
3. ships the working tree, records the hardware (`lscpu`, memory, kernel, Docker
   version), and runs `preflight.sh --remote-loadgen` on the platform VM
4. enables the mutual-TLS Docker API and starts the monitoring stack
5. for each config: deploys the platform, copies `connection.env` and the crypto
   material it names to the load generator, runs benchrunner, and tears the
   platform down
6. pulls `results/` back and destroys the stack

Failed deploys or runs are recorded and the campaign continues; the script exits
non-zero at the end if anything failed. `--keep` leaves the last platform's VMs up
for inspection; `terraform destroy` is printed for you.

### Outputs

- `results/<platform>/<timestamp>/`: identical to a local run. The manifest's
  `harness_git_sha` is the commit you ran from, with `-dirty` if the working tree
  had uncommitted changes.
- `results/_campaigns/<id>/`: the campaign log, each platform's hardware record,
  and `comparison.html` for this campaign's runs only.
- `results/_campaigns/<id>/SUMMARY.tsv`: one row per platform×config with
  status and failure reason.
- `results/_campaigns/<id>/<platform>/<config>/{deploy,run,teardown}.log` and
  `capture/`: per-step logs and the pre-teardown diagnostic capture, taken on
  the platform VM and pulled back before it is destroyed.
- `results/_campaigns/<id>/<vm>-install.log`: saved if a VM fails provisioning.
- `gs://<results-bucket>/<id>/results/` if `--results-bucket` was given.

See [running-benchmarks.md#logs-and-failure-captures](running-benchmarks.md#logs-and-failure-captures)
for the layout and a triage recipe.

### Looking at a live run

```bash
gcloud compute ssh bench-fabric-cft-sut --zone us-central1-a --tunnel-through-iap
gcloud compute start-iap-tunnel bench-fabric-cft-sut 3000 --local-host-port=localhost:3000 --zone us-central1-a
# Grafana on http://localhost:3000
```

## Cost and quota

VMs exist only while their platform runs. With the default quick-smoke plus
probe-sweep configs, a platform takes roughly 45–75 minutes including
provisioning and image pulls; the first Fabric-X build adds 15–20 minutes. A
three-platform `gcp-full` campaign is therefore on the order of 48 vCPU for 3–4
hours, plus SSD disk and NAT. Price it with the
[pricing calculator](https://cloud.google.com/products/calculator) for your region
and discounts.

Quota needed at any one time: 48 N2 vCPUs for `gcp-full` (24 for `gcp-small`), 2
instances, 1 Cloud NAT, and SSD persistent disk of 350 GB (250 GB).

If the script is killed hard (SIGKILL, lost laptop), the trap cannot run. Find
leftovers by label:

```bash
gcloud compute instances list --filter="labels.purpose=blockchain-benchmark" --project "$PROJECT"
terraform -chdir=deploy/terraform workspace select <platform> && terraform -chdir=deploy/terraform destroy -var project="$PROJECT" -var platform=<platform>
```

## Fairness on GCP

- Sequential platforms on identical, freshly created machine types
  ([adr-005](../decisions/adr-005-sequential-runs.md)). A fresh VM per platform
  rules out leftovers from the previous platform: page cache, images, kernel
  state.
- `on_host_maintenance = TERMINATE`: a VM is never live-migrated partway through
  a run.
- Both VMs in one zone; latency is measured on the load generator's clock
  throughout (T1, T2 and T3 are all observed there), so clock skew between the
  VMs does not enter any latency figure.
- `min_cpu_platform` pins the silicon when campaigns must be compared across time.
