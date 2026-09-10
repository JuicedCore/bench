# GCP infrastructure for one platform's benchmark run (Phase 5).
#
# Brings up, for a single platform:
#   - N node VMs           (n2-standard-8 by default) running Docker
#   - 1 load-generator VM  (n2-standard-4) - never co-located with nodes
#   - 1 monitoring VM       (e2-standard-4) - reuse across platforms
#
# Run platforms SEQUENTIALLY (docs/decisions/adr-005-sequential-runs.md):
#   terraform apply  -var platform=fabric-cft -var profile=gcp-full
#   # ... run the benchmark from the loadgen VM ...
#   terraform destroy -var platform=fabric-cft -var profile=gcp-full
#
# The monitoring VM can be kept between platforms:
#   terraform apply -var platform=fabric-cft -var keep_monitoring=true
#
# This is infrastructure only. Deploying the blockchain itself on the node VMs is
# done by deploy/docker/<platform>/up.sh run over SSH (see
# docs/guides/gcp-deployment.md).

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

variable "project" { type = string }
variable "region" {
  type    = string
  default = "us-central1"
}
variable "zone" {
  type    = string
  default = "us-central1-a"
}

variable "platform" {
  type        = string
  description = "fabric-cft | fabric-bft | drunix | fabricx | neuchain"
}

variable "profile" {
  type    = string
  default = "gcp-full"
}

variable "node_count" {
  type    = number
  default = 4
}
variable "node_machine_type" {
  type    = string
  default = "n2-standard-8"
}
variable "loadgen_machine_type" {
  type    = string
  default = "n2-standard-4"
}
variable "monitoring_machine_type" {
  type    = string
  default = "e2-standard-4"
}
variable "node_disk_gb" {
  type    = number
  default = 200
}
variable "keep_monitoring" {
  type    = bool
  default = false
}
variable "ssh_user" {
  type    = string
  default = "bench"
}
variable "ssh_pubkey_path" {
  type    = string
  default = "~/.ssh/id_ed25519.pub"
}

provider "google" {
  project = var.project
  region  = var.region
  zone    = var.zone
}

locals {
  labels = {
    purpose  = "blockchain-benchmark"
    platform = var.platform
    profile  = var.profile
  }
  startup = <<-EOT
    #!/bin/bash
    set -e
    curl -fsSL https://get.docker.com | sh
    usermod -aG docker ${var.ssh_user} || true
    # perf: raise file limits + disable THP for consistent latency
    echo 'fs.file-max=2097152' >> /etc/sysctl.conf
    sysctl -p || true
    echo never > /sys/kernel/mm/transparent_hugepage/enabled || true
  EOT
  ssh_keys = "${var.ssh_user}:${file(pathexpand(var.ssh_pubkey_path))}"
}

resource "google_compute_network" "bench" {
  name                    = "bench-${var.platform}"
  auto_create_subnetworks = true
}

resource "google_compute_firewall" "internal" {
  name    = "bench-${var.platform}-internal"
  network = google_compute_network.bench.name
  allow { protocol = "tcp" }
  allow { protocol = "udp" }
  allow { protocol = "icmp" }
  source_ranges = ["10.128.0.0/9"]
}

resource "google_compute_firewall" "ssh" {
  name    = "bench-${var.platform}-ssh"
  network = google_compute_network.bench.name
  allow {
    protocol = "tcp"
    ports    = ["22", "3000", "9090"] # ssh + grafana + prometheus
  }
  source_ranges = ["0.0.0.0/0"] # tighten to your IP in practice
}

resource "google_compute_instance" "node" {
  count        = var.node_count
  name         = "bench-${var.platform}-node-${count.index}"
  machine_type = var.node_machine_type
  labels       = local.labels

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = var.node_disk_gb
      type  = "pd-ssd"
    }
  }
  network_interface {
    network = google_compute_network.bench.name
    access_config {}
  }
  metadata = {
    ssh-keys       = local.ssh_keys
    startup-script = local.startup
  }
}

resource "google_compute_instance" "loadgen" {
  name         = "bench-${var.platform}-loadgen"
  machine_type = var.loadgen_machine_type
  labels       = local.labels

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = 50
      type  = "pd-ssd"
    }
  }
  network_interface {
    network = google_compute_network.bench.name
    access_config {}
  }
  metadata = {
    ssh-keys       = local.ssh_keys
    startup-script = local.startup
  }
}

resource "google_compute_instance" "monitoring" {
  count        = var.keep_monitoring ? 0 : 1
  name         = "bench-${var.platform}-monitoring"
  machine_type = var.monitoring_machine_type
  labels       = local.labels

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = 100
      type  = "pd-ssd"
    }
  }
  network_interface {
    network = google_compute_network.bench.name
    access_config {}
  }
  metadata = {
    ssh-keys       = local.ssh_keys
    startup-script = local.startup
  }
}

output "node_ips" {
  value = [for n in google_compute_instance.node : n.network_interface[0].access_config[0].nat_ip]
}
output "node_internal_ips" {
  value = [for n in google_compute_instance.node : n.network_interface[0].network_ip]
}
output "loadgen_ip" {
  value = google_compute_instance.loadgen.network_interface[0].access_config[0].nat_ip
}
output "monitoring_ip" {
  value = var.keep_monitoring ? "reused" : google_compute_instance.monitoring[0].network_interface[0].access_config[0].nat_ip
}
output "next_steps" {
  value = <<-EOT
    scp the repo to the loadgen VM, then on it:
      source deploy/docker/${var.platform}/connection.env   # after deploying on the nodes
      ./bin/benchrunner run --config configs/probe-sweep.yaml --platform ${var.platform} --profile ${var.profile}
    Deploy the blockchain on the node VMs first (see docs/guides/gcp-deployment.md).
    Tear down when done:  terraform destroy -var platform=${var.platform} -var profile=${var.profile}
  EOT
}
