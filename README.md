# Github Package Registry mirror

This is a simple Maven repository/proxy that fetches public packages from Github
and serves them here. The reason is that Github required authentication (personal access tokens)
when fetching packages, even if the packages are public. This may be a temporary problem -
in which case this proxy is a temporary solution.

### Deploying the app

Every commit to the `master`-branch will automatically be built by Google Cloud Build,
and the resulting Docker image is deployed as a serverless function on Google Cloud Run.

The app runs on [https://github-package-registry-mirror.gc.nav.no/](https://github-package-registry-mirror-sr4qwz23da-ew.a.run.app/).

## Cache

Artifacts from GitHub are cached in an object store.
