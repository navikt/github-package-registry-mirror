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
    "secretmanager.googleapis.com",
  ])
  service = each.value
}

resource "google_cloud_run_domain_mapping" "default" {
  location = local.region
  name     = "github-package-registry-mirror.gc.nav.no"

  metadata {
    namespace = local.project
  }

  spec {
    route_name = "github-package-registry-mirror"
  }

  lifecycle {
    ignore_changes = [
      metadata,
    ]
  }
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

# --- Secret Manager ---

resource "google_secret_manager_secret" "github_token" {
  secret_id = "github-token"

  replication {
    user_managed {
      replicas {
        location = local.region
      }
    }
  }

  depends_on = [google_project_service.apis]
}

# --- Cloud Run service account ---

resource "google_service_account" "cloud_run" {
  account_id   = "cloud-run-mirror"
  display_name = "Cloud Run mirror service"
  depends_on   = [google_project_service.apis]
}

resource "google_secret_manager_secret_iam_member" "cloud_run_token_access" {
  secret_id = google_secret_manager_secret.github_token.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.cloud_run.email}"
}

resource "google_storage_bucket_iam_member" "cloud_run_cache_access" {
  bucket = google_storage_bucket.mirror-cache.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.cloud_run.email}"
}

# --- Cloud Build ---

resource "google_cloudbuild_trigger" "build-trigger" {
  location        = local.region
  service_account = google_service_account.cloudbuild.id

  github {
    owner = "navikt"
    name  = "github-package-registry-mirror"
    push {
      branch = "main"
    }
  }

  substitutions = {}

  filename = "cloudbuild.yaml"
}

# --- Log-based metrics ---

locals {
  service_filter = <<-EOT
    resource.type="cloud_run_revision"
    resource.labels.service_name="github-package-registry-mirror"
  EOT
}

resource "google_logging_metric" "cache_hit" {
  name   = "mirror/cache_hit"
  filter = <<-EOT
    ${local.service_filter}
    jsonPayload.msg="serving from cache"
    jsonPayload.hit=true
  EOT

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

resource "google_logging_metric" "cache_miss" {
  name   = "mirror/cache_miss"
  filter = <<-EOT
    ${local.service_filter}
    jsonPayload.msg="serving from cache"
    jsonPayload.hit=false
  EOT

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

resource "google_logging_metric" "upstream_error" {
  name   = "mirror/upstream_error"
  filter = <<-EOT
    ${local.service_filter}
    severity=ERROR
    jsonPayload.msg=("failed to fetch artifact" OR "could not fetch artifact" OR "artifact too large" OR "failed to fetch and cache artifact")
  EOT

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

resource "google_logging_metric" "health_check_failure" {
  name   = "mirror/health_check_failure"
  filter = <<-EOT
    ${local.service_filter}
    severity=ERROR
    jsonPayload.msg=~"^health check failed"
  EOT

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}
