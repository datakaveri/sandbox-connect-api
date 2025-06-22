# Profile Credit Sync

This service synchronizes Kubeflow profile credits and usage data from OpenCost to the database. It processes namespace usage data and updates the corresponding profile records.

## Running Locally

```bash
go run main.go
```

## Kubernetes Deployment

This service is designed to run as a CronJob in Kubernetes to regularly sync credit usage data.

### Prerequisites

1. Ensure the required secrets are created:
   - `database-creds` with PostgreSQL connection details
   - ConfigMap `profile-credit-sync-config` with application configuration

2. Deploy the CronJob:

```bash
kubectl apply -f infra/cron/profile-credit-sync/configmap.yaml
kubectl apply -f infra/cron/profile-credit-sync/cronjob.yaml
```

The CronJob uses the image `ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron:latest` and runs every 15 minutes by default.
