# ----------------------------------------------------------------------------
# Required
# ----------------------------------------------------------------------------

variable "project" {
  type        = string
  description = "GCP project that hosts the benchmark VMs."
}

variable "platform" {
  type        = string
  description = "Platform under test. One stack per platform, run sequentially (adr-005)."
  validation {
    condition     = contains(["fabric-cft", "fabric-bft", "drunix", "fabricx", "neuchain", "mock"], var.platform)
    error_message = "platform must be one of fabric-cft, fabric-bft, drunix, fabricx, neuchain, mock."
  }
}

# ----------------------------------------------------------------------------
# Placement
# ----------------------------------------------------------------------------

variable "region" {
  type    = string
  default = "us-central1"
}

variable "zone" {
  type        = string
  default     = "us-central1-a"
  description = "Both VMs go in one zone so load-generator-to-platform latency is not a variable."
}

variable "name_prefix" {
  type        = string
  default     = "bench"
  description = "Prefix for every resource name."
}

variable "labels" {
  type        = map(string)
  default     = {}
  description = "Extra labels (cost-center, owner, env, ...) merged onto every resource."
}

# ----------------------------------------------------------------------------
# Machines. scripts/gcp-run.sh fills these from the profile's `gcp:` block, so
# the hardware and the resource budget cannot disagree.
# ----------------------------------------------------------------------------

variable "profile" {
  type        = string
  default     = "gcp-small"
  description = "deploy/profiles/<profile>.yaml the run uses. Recorded as a label."
}

variable "sut_machine_type" {
  type        = string
  default     = "n2-standard-16"
  description = "Platform-under-test VM. Must exceed the profile budget plus OS and monitoring headroom."
}

variable "loadgen_machine_type" {
  type        = string
  default     = "n2-standard-8"
  description = "Load-generator VM. Never co-located with the platform."
}

variable "sut_disk_gb" {
  type    = number
  default = 200
}

variable "loadgen_disk_gb" {
  type    = number
  default = 50
}

variable "disk_type" {
  type    = string
  default = "pd-ssd"
}

variable "image" {
  type        = string
  default     = "debian-cloud/debian-12"
  description = "Boot image. scripts/install-deps.sh supports Debian, Ubuntu, RHEL-family and Arch."
}

variable "min_cpu_platform" {
  type        = string
  default     = null
  description = "Pin the CPU generation (e.g. \"Intel Ice Lake\") so repeated campaigns land on the same silicon."
}

# ----------------------------------------------------------------------------
# Network. Default: a dedicated VPC with no public IPs, Cloud NAT for egress
# (image and source downloads), SSH only through IAP. Set `network` and
# `subnetwork` to place the VMs in an existing (e.g. Shared) VPC instead.
# ----------------------------------------------------------------------------

variable "network" {
  type        = string
  default     = null
  description = "Existing VPC self link. null creates a dedicated one."
}

variable "subnetwork" {
  type        = string
  default     = null
  description = "Existing subnetwork self link. Required when `network` is set."
}

variable "subnet_cidr" {
  type    = string
  default = "10.42.0.0/24"
}

variable "create_nat" {
  type        = bool
  default     = true
  description = "Create a Cloud Router + NAT. Disable if the existing network already provides egress."
}

variable "operator_cidrs" {
  type        = list(string)
  default     = []
  description = "Extra CIDRs allowed to SSH directly. Empty (recommended) means IAP only."
}

# ----------------------------------------------------------------------------
# Identity and encryption
# ----------------------------------------------------------------------------

variable "service_account_email" {
  type        = string
  default     = null
  description = "Existing service account for the VMs. null creates a least-privilege one (log + metric writer only)."
}

variable "kms_key_self_link" {
  type        = string
  default     = null
  description = "Cloud KMS key for the boot disks (CMEK). null uses Google-managed keys."
}
