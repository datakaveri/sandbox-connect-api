# Profile Credit Sync

This service synchronizes Kubeflow profile credits and usage data. It processes profiles that have valid UUIDs as their 
namespace.

## Features

- Syncs credit usage data for Kubeflow profiles
- Only processes profiles with valid UUID identifiers
- Runs as a scheduled cron job

## Configuration

The service requires the following environment variables:

- `POSTGRES_URL`: Database connection string
- `OPENCOST_URL`: OpenCost API endpoint
- `NAMESPACE_CREDIT_SYNC_BATCH_SIZE`: Batch size for processing profiles
- `NAMESPACE_CREDIT_SYNC_MAX_RETRIES`: Maximum number of retries for processing profiles

## Running the Service

```bash
go run main.go
```

## Schedule

The service runs on a configured schedule to regularly sync credit usage data.
