# Github Package Registry mirror

A Maven repository proxy that fetches public packages from Github Package Registry
without requiring authentication. Github requires personal access tokens to fetch
packages, even public ones — this proxy removes that requirement.

## Development

Prerequisites: [mise](https://mise.jdx.dev/)

```bash
mise install          # Install Go, golangci-lint, Gradle, Java
mise run test         # Run unit tests
mise run check        # Run gofumpt, golangci-lint, staticcheck, govulncheck
mise run run          # Start the server locally on :8080
mise run test:integration  # Run integration test (requires GITHUB_TOKEN)
```

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `STORAGE_BACKEND` | `gcs` | `gcs` or `local` |
| `STORAGE_PATH` | — | Directory for local storage backend |

## Deploying

Every commit to `master` is automatically built by Google Cloud Build and deployed
to Google Cloud Run using deploy-from-source.

The app runs on [https://github-package-registry-mirror.gc.nav.no/](https://github-package-registry-mirror.gc.nav.no/).

## Cache

Artifacts from GitHub are cached in a GCS bucket. `maven-metadata.xml` files are
refreshed after 15 minutes; all other artifacts are cached indefinitely.
