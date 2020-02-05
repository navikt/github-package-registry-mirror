provider "google-beta" {
  project     = "github-package-registry-mirror"
  region      = "europe-north1"
}

resource "google_storage_bucket" "mirror-cache" {
  project = "github-package-registry-mirror"
  name     = "github-package-registry-storage"
  location = "europe-north1"
}

resource "google_project_service" "cloudbuild" {
  project = "github-package-registry-mirror"
  service = "cloudbuild.googleapis.com"
}
resource "google_project_service" "run" {
  project = "github-package-registry-mirror"
  service = "run.googleapis.com"
}

resource "google_cloud_run_service" "default" {
  project = "github-package-registry-mirror"
  name = "github-package-registry-mirror"
  location = "europe-north1"

  template {
    spec {
      containers {
        image = "gcr.io/github-package-registry-mirror/github-package-registry-mirror"
      }
    }
  }

  lifecycle {
    ignore_changes = [
      traffic,
      metadata,
      template
    ]
  }
}

resource "google_cloudbuild_trigger" "build-trigger" {
  provider = google-beta

  github {
    owner = "navikt"
    name = "github-package-registry-mirror"
    push {
      branch = "master"
    }
  }

  substitutions = {
  }

  filename = "cloudbuild.yaml"
}
