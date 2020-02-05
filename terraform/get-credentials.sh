#!/bin/bash
gcloud iam service-accounts --project github-package-registry-mirror keys create service-account-credentials.json --iam-account="terraformer@github-package-registry-mirror.iam.gserviceaccount.com"

