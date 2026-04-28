terraform {
  required_version = ">= 1.8.0"

  backend "gcs" {
    bucket                      = "github-package-registry-mirror-tfstate"
    impersonate_service_account = "terraformer@github-package-registry-mirror.iam.gserviceaccount.com"
  }

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 7.0"
    }
  }
}

locals {
  project = "github-package-registry-mirror"
  region  = "europe-north1"
}

provider "google" {
  project                     = local.project
  region                      = local.region
  impersonate_service_account = "terraformer@github-package-registry-mirror.iam.gserviceaccount.com"
}

provider "google-beta" {
  project                     = local.project
  region                      = local.region
  impersonate_service_account = "terraformer@github-package-registry-mirror.iam.gserviceaccount.com"
}

# --- Existing resources (preserve in state) ---

resource "google_storage_bucket" "mirror-cache" {
  name     = "github-package-registry-storage"
  location = local.region
}

resource "google_project_service" "apis" {
  for_each = toset([
    "cloudbuild.googleapis.com",
    "iam.googleapis.com",
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
  ])
  service = each.value
}

resource "google_cloud_run_service" "default" {
  name     = "github-package-registry-mirror"
  location = local.region

  template {
    spec {
      containers {
        image = "eu.gcr.io/github-package-registry-mirror/github-package-registry-mirror"
      }
    }
  }

  lifecycle {
    ignore_changes = [
      traffic,
      metadata,
      template,
    ]
  }
}

resource "google_cloud_run_domain_mapping" "default" {
  location = local.region
  name     = "github-package-registry-mirror.gc.nav.no"

  metadata {
    namespace = local.project
  }

  spec {
    route_name = google_cloud_run_service.default.name
  }

  lifecycle {
    ignore_changes = [
      metadata,
    ]
  }
}

resource "google_container_registry" "registry" {
  location = "EU"
}

resource "google_service_account" "cloudbuild" {
  account_id   = "cloudbuild-deployer"
  display_name = "Cloud Build deployer"
  depends_on   = [google_project_service.apis]
}

resource "google_project_iam_member" "cloudbuild" {
  for_each = toset([
    "roles/cloudbuild.builds.builder",
    "roles/run.admin",
    "roles/iam.serviceAccountUser",
    "roles/artifactregistry.admin",
    "roles/logging.logWriter",
  ])
  project = local.project
  role    = each.value
  member  = "serviceAccount:${google_service_account.cloudbuild.email}"
}

resource "google_cloudbuild_trigger" "build-trigger" {
  service_account = google_service_account.cloudbuild.id

  github {
    owner = "navikt"
    name  = "github-package-registry-mirror"
    push {
      branch = "master"
    }
  }

  substitutions = {}

  filename = "cloudbuild.yaml"
}

# --- New resources ---

resource "google_cloud_run_v2_service" "go-rewrite" {
  name                = "github-package-registry-mirror-v2"
  location            = local.region
  deletion_protection = false

  template {
    containers {
      image = "us-docker.pkg.dev/cloudrun/container/hello"
    }
  }

  lifecycle {
    ignore_changes = [
      template,
      traffic,
    ]
  }

  depends_on = [google_project_service.apis]
}
