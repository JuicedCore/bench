# Long-lived resources shared by every benchmark campaign. Apply once, separately
# from the per-platform stack in ../ (which is created and destroyed per platform
# run and must never own anything that outlives it).
#
#   cd deploy/terraform/shared
#   terraform init -backend-config=../backend.hcl -backend-config="prefix=bench/shared"
#   terraform apply -var project=... -var results_bucket=...

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
  backend "gcs" {}
}

variable "project" {
  type = string
}

variable "region" {
  type    = string
  default = "us-central1"
}

variable "results_bucket" {
  type        = string
  description = "Bucket that archives every run's results/ directory."
}

variable "kms_key_self_link" {
  type        = string
  default     = null
  description = "Cloud KMS key for the bucket (CMEK). null uses Google-managed keys."
}

variable "results_retention_days" {
  type        = number
  default     = 0
  description = "Delete archived results older than this. 0 keeps them."
}

variable "labels" {
  type    = map(string)
  default = {}
}

provider "google" {
  project = var.project
  region  = var.region
  default_labels = merge({
    purpose    = "blockchain-benchmark"
    managed-by = "terraform"
  }, var.labels)
}

resource "google_storage_bucket" "results" {
  name                        = var.results_bucket
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  versioning {
    enabled = true
  }

  dynamic "encryption" {
    for_each = var.kms_key_self_link == null ? [] : [1]
    content {
      default_kms_key_name = var.kms_key_self_link
    }
  }

  dynamic "lifecycle_rule" {
    for_each = var.results_retention_days > 0 ? [1] : []
    content {
      condition {
        age = var.results_retention_days
      }
      action {
        type = "Delete"
      }
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

output "results_bucket_url" {
  value = google_storage_bucket.results.url
}
