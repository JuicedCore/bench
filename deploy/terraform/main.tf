# GCP infrastructure for one platform's benchmark run.
#
#   sut      one VM running the whole platform under test (Docker), plus the
#            monitoring stack (cAdvisor must see the platform's containers)
#   loadgen  one VM running benchrunner, never co-located with the platform
#
# The deploy scripts bring each platform up on a single Docker host, so the
# platform gets one large VM rather than several small ones; see
# docs/guides/gcp-deployment.md for why, and for what that means for scale-out
# designs (Fabric-X, NeuChain).
#
# Nothing has a public IP. Operators reach the VMs through IAP; the VMs reach the
# internet (image and source downloads) through Cloud NAT. The load generator
# reaches the platform's ports, Prometheus, and the Docker API (mutual TLS, set
# up by scripts/gcp-run.sh) over the private subnet only.
#
# Drive it with scripts/gcp-run.sh rather than by hand.

locals {
  name = "${var.name_prefix}-${var.platform}"

  labels = merge({
    purpose    = "blockchain-benchmark"
    platform   = var.platform
    profile    = var.profile
    managed-by = "terraform"
  }, var.labels)

  create_network = var.network == null
  network        = local.create_network ? google_compute_network.bench[0].self_link : var.network
  subnetwork     = local.create_network ? google_compute_subnetwork.bench[0].self_link : var.subnetwork

  create_sa = var.service_account_email == null
  sa_email  = local.create_sa ? google_service_account.bench[0].email : var.service_account_email

  sut_tag     = "${local.name}-sut"
  loadgen_tag = "${local.name}-loadgen"

  # The VMs provision themselves with the same installer a workstation uses,
  # then leave a marker that scripts/gcp-run.sh waits for.
  startup_script = templatefile("${path.module}/startup.sh.tftpl", {
    install_deps = file("${path.module}/../../scripts/install-deps.sh")
  })
}

check "subnetwork_with_network" {
  assert {
    condition     = var.network == null || var.subnetwork != null
    error_message = "Set subnetwork when network is set."
  }
}

# ----------------------------------------------------------------------------
# Network
# ----------------------------------------------------------------------------

resource "google_compute_network" "bench" {
  count                   = local.create_network ? 1 : 0
  name                    = local.name
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "bench" {
  count                    = local.create_network ? 1 : 0
  name                     = local.name
  network                  = google_compute_network.bench[0].self_link
  region                   = var.region
  ip_cidr_range            = var.subnet_cidr
  private_ip_google_access = true

  log_config {
    aggregation_interval = "INTERVAL_5_MIN"
    flow_sampling        = 0.1
    metadata             = "INCLUDE_ALL_METADATA"
  }
}

resource "google_compute_router" "bench" {
  count   = var.create_nat ? 1 : 0
  name    = local.name
  network = local.network
  region  = var.region
}

resource "google_compute_router_nat" "bench" {
  count                              = var.create_nat ? 1 : 0
  name                               = local.name
  router                             = google_compute_router.bench[0].name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = local.create_network ? "LIST_OF_SUBNETWORKS" : "ALL_SUBNETWORKS_ALL_IP_RANGES"

  dynamic "subnetwork" {
    for_each = local.create_network ? [1] : []
    content {
      name                    = google_compute_subnetwork.bench[0].self_link
      source_ip_ranges_to_nat = ["ALL_IP_RANGES"]
    }
  }

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}

# SSH from Google's IAP range only (plus any explicitly allowed operator CIDRs).
resource "google_compute_firewall" "ssh" {
  name          = "${local.name}-ssh"
  network       = local.network
  direction     = "INGRESS"
  source_ranges = concat(["35.235.240.0/20"], var.operator_cidrs)
  target_tags   = [local.sut_tag, local.loadgen_tag]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  log_config {
    metadata = "INCLUDE_ALL_METADATA"
  }
}

# Load generator -> platform: platform ports, Prometheus, Docker API (mTLS).
# Scoped by tag, so nothing else in a shared VPC reaches the platform.
resource "google_compute_firewall" "loadgen_to_sut" {
  name        = "${local.name}-loadgen-to-sut"
  network     = local.network
  direction   = "INGRESS"
  source_tags = [local.loadgen_tag]
  target_tags = [local.sut_tag]

  allow {
    protocol = "tcp"
  }
  allow {
    protocol = "udp"
  }
  allow {
    protocol = "icmp"
  }
}

# ----------------------------------------------------------------------------
# Identity
# ----------------------------------------------------------------------------

resource "google_service_account" "bench" {
  count        = local.create_sa ? 1 : 0
  account_id   = substr("${local.name}-vm", 0, 30)
  display_name = "Benchmark VMs (${var.platform})"
  description  = "Runs the benchmark VMs. Writes logs and metrics only."
}

resource "google_project_iam_member" "bench" {
  for_each = local.create_sa ? toset(["roles/logging.logWriter", "roles/monitoring.metricWriter"]) : toset([])
  project  = var.project
  role     = each.value
  member   = "serviceAccount:${google_service_account.bench[0].email}"
}

# ----------------------------------------------------------------------------
# VMs
# ----------------------------------------------------------------------------

resource "google_compute_instance" "sut" {
  name             = "${local.name}-sut"
  machine_type     = var.sut_machine_type
  zone             = var.zone
  min_cpu_platform = var.min_cpu_platform
  tags             = [local.sut_tag]
  labels           = merge(local.labels, { role = "sut" })

  boot_disk {
    kms_key_self_link = var.kms_key_self_link
    initialize_params {
      image = var.image
      size  = var.sut_disk_gb
      type  = var.disk_type
    }
  }

  network_interface {
    subnetwork = local.subnetwork
  }

  service_account {
    email  = local.sa_email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
    startup-script         = local.startup_script
  }

  # A benchmark VM that migrates mid-run is a different machine for part of it.
  scheduling {
    on_host_maintenance = "TERMINATE"
    automatic_restart   = false
  }

  depends_on = [google_compute_router_nat.bench]
}

resource "google_compute_instance" "loadgen" {
  name             = "${local.name}-loadgen"
  machine_type     = var.loadgen_machine_type
  zone             = var.zone
  min_cpu_platform = var.min_cpu_platform
  tags             = [local.loadgen_tag]
  labels           = merge(local.labels, { role = "loadgen" })

  boot_disk {
    kms_key_self_link = var.kms_key_self_link
    initialize_params {
      image = var.image
      size  = var.loadgen_disk_gb
      type  = var.disk_type
    }
  }

  network_interface {
    subnetwork = local.subnetwork
  }

  service_account {
    email  = local.sa_email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
    startup-script         = local.startup_script
  }

  scheduling {
    on_host_maintenance = "TERMINATE"
    automatic_restart   = false
  }

  depends_on = [google_compute_router_nat.bench]
}
