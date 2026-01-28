# Secret Creator

A script that creates or updates a Kubernetes Docker registry secret using AWS ECR authorization token.

## Secret Type

The secret is created/updated as type `docker-registry` (DockerConfigJson) with ECR authorization token:

```json
{
  "auths": {
    "<ECR_REGISTRY_URL>": {
      "auth": "<base64(AWS:ECR_TOKEN)>"
    }
  }
}
```

## Usage

1. Update AWS credentials in `infra/cron/secret-refresher/secret.yaml`

2. Apply all resources:
```bash
kubectl apply -f infra/cron/secret-refresher/configmap.yaml
kubectl apply -f infra/cron/secret-refresher/secret.yaml
kubectl apply -f infra/cron/secret-refresher/cronjob.yaml
```

Or apply all at once:
```bash
kubectl apply -f infra/cron/secret-refresher/
```

3. The CronJob runs every hour and keeps 3 successful and 3 failed job history records. The script gets a fresh ECR authorization token and creates/updates the docker-registry secret.

## Building

```bash
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-secret-refresher:latest -f infra/cron/secret-refresher/Dockerfile .
```
