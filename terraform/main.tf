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
  project  = local.project
  name     = "github-package-registry-storage"
  location = local.region
}

resource "google_project_service" "cloudbuild" {
  project = local.project
  service = "cloudbuild.googleapis.com"
}

resource "google_project_service" "run" {
  project = local.project
  service = "run.googleapis.com"
}

resource "google_cloud_run_service" "default" {
  project  = local.project
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
  project  = local.project
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
  project  = local.project
  location = "EU"
}

resource "google_cloudbuild_trigger" "build-trigger" {
  project = local.project

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

resource "google_project_service" "artifactregistry" {
  project = local.project
  service = "artifactregistry.googleapis.com"
}

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

  depends_on = [google_project_service.run]
}
