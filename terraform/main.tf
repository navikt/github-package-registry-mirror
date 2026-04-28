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

# --- Dashboard ---

resource "google_monitoring_dashboard" "mirror" {
  dashboard_json = <<-EOF
    {
      "displayName": "GitHub Package Registry Mirror",
      "mosaicLayout": {
        "columns": 48,
        "tiles": [
          {
            "width": 24, "height": 16,
            "widget": {
              "title": "Cache Hit / Miss",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"logging.googleapis.com/user/mirror/cache_hit\" resource.type=\"cloud_run_revision\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_RATE", "crossSeriesReducer": "REDUCE_SUM" }
                    }},
                    "plotType": "STACKED_AREA", "legendTemplate": "Cache Hit", "targetAxis": "Y1"
                  },
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"logging.googleapis.com/user/mirror/cache_miss\" resource.type=\"cloud_run_revision\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_RATE", "crossSeriesReducer": "REDUCE_SUM" }
                    }},
                    "plotType": "STACKED_AREA", "legendTemplate": "Cache Miss", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "req/s", "scale": "LINEAR" }
              }
            }
          },
          {
            "xPos": 24, "width": 24, "height": 16,
            "widget": {
              "title": "Request Count (by response code)",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"run.googleapis.com/request_count\" resource.type=\"cloud_run_revision\" resource.labels.service_name=\"github-package-registry-mirror\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_RATE", "crossSeriesReducer": "REDUCE_SUM", "groupByFields": ["metric.labels.response_code_class"] }
                    }},
                    "plotType": "STACKED_BAR", "legendTemplate": "$${metric.labels.response_code_class}", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "req/s", "scale": "LINEAR" }
              }
            }
          },
          {
            "yPos": 16, "width": 24, "height": 16,
            "widget": {
              "title": "Request Latency (p50 / p95 / p99)",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"run.googleapis.com/request_latencies\" resource.type=\"cloud_run_revision\" resource.labels.service_name=\"github-package-registry-mirror\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_PERCENTILE_50", "crossSeriesReducer": "REDUCE_PERCENTILE_50" }
                    }},
                    "plotType": "LINE", "legendTemplate": "p50", "targetAxis": "Y1"
                  },
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"run.googleapis.com/request_latencies\" resource.type=\"cloud_run_revision\" resource.labels.service_name=\"github-package-registry-mirror\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_PERCENTILE_95", "crossSeriesReducer": "REDUCE_PERCENTILE_95" }
                    }},
                    "plotType": "LINE", "legendTemplate": "p95", "targetAxis": "Y1"
                  },
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"run.googleapis.com/request_latencies\" resource.type=\"cloud_run_revision\" resource.labels.service_name=\"github-package-registry-mirror\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_PERCENTILE_99", "crossSeriesReducer": "REDUCE_PERCENTILE_99" }
                    }},
                    "plotType": "LINE", "legendTemplate": "p99", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "ms", "scale": "LINEAR" }
              }
            }
          },
          {
            "xPos": 24, "yPos": 16, "width": 24, "height": 16,
            "widget": {
              "title": "Upstream Errors",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"logging.googleapis.com/user/mirror/upstream_error\" resource.type=\"cloud_run_revision\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_RATE", "crossSeriesReducer": "REDUCE_SUM" }
                    }},
                    "plotType": "LINE", "legendTemplate": "Upstream Errors", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "errors/s", "scale": "LINEAR" }
              }
            }
          },
          {
            "yPos": 32, "width": 24, "height": 16,
            "widget": {
              "title": "Instance Count",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"run.googleapis.com/container/instance_count\" resource.type=\"cloud_run_revision\" resource.labels.service_name=\"github-package-registry-mirror\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MAX", "crossSeriesReducer": "REDUCE_SUM", "groupByFields": ["metric.labels.state"] }
                    }},
                    "plotType": "STACKED_AREA", "legendTemplate": "$${metric.labels.state}", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "instances", "scale": "LINEAR" }
              }
            }
          },
          {
            "xPos": 24, "yPos": 32, "width": 24, "height": 16,
            "widget": {
              "title": "Health Check Failures",
              "xyChart": {
                "dataSets": [
                  {
                    "timeSeriesQuery": { "timeSeriesFilter": {
                      "filter": "metric.type=\"logging.googleapis.com/user/mirror/health_check_failure\" resource.type=\"cloud_run_revision\"",
                      "aggregation": { "alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_RATE", "crossSeriesReducer": "REDUCE_SUM" }
                    }},
                    "plotType": "LINE", "legendTemplate": "Health Check Failures", "targetAxis": "Y1"
                  }
                ],
                "yAxis": { "label": "failures/s", "scale": "LINEAR" }
              }
            }
          }
        ]
      }
    }
  EOF
}
