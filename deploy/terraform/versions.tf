terraform {
  required_version = ">= 1.5"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  # Remote state in GCS. Configured at init time so no bucket name is committed:
  #   terraform init -backend-config=backend.hcl
  # See backend.hcl.example. For a throwaway local trial:
  #   terraform init -backend=false   (then use -state=... or a local backend override)
  backend "gcs" {}
}

provider "google" {
  project = var.project
  region  = var.region
  zone    = var.zone

  default_labels = local.labels
}
