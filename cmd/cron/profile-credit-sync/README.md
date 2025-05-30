# Profile Credit Sync

This service synchronizes Kubeflow profile credits and usage data from OpenCost to the database. It processes namespace usage data and updates the corresponding profile records.


## Running Locally

```bash
go run main.go
```

## Docker

Build and run using Docker:

```bash
# Build
docker build -f infra/cron/profile-credit-sync/Dockerfile -t profile-credit-sync .

# Run
docker run --env-file .env profile-credit-sync
```

## Kubernetes Deployment

This service is designed to run as a CronJob in Kubernetes to regularly sync credit usage data.
