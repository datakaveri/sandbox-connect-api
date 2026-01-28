# Secret Refresher

A script that creates or updates a Kubernetes Docker registry secret using AWS ECR authorization token.

## Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `SECRET_REFRESHER_SECRET_NAME` | Name of Kubernetes secret to create/update | - | Yes |
| `SECRET_REFRESHER_SECRET_NAMESPACE` | Namespace where the secret should exist | `sandbox` | No |
| `SECRET_REFRESHER_ECR_REGION` | AWS ECR region (e.g., `ap-south-1`, `us-east-1`) | - | Yes |
| `SECRET_REFRESHER_ECR_REGISTRY_URL` | ECR registry URL (e.g., `123456789.dkr.ecr.us-east-1.amazonaws.com`) | - | Yes |
| `SECRET_REFRESHER_AWS_ACCESS_KEY_ID` | AWS access key ID | - | Yes |
| `SECRET_REFRESHER_AWS_SECRET_KEY` | AWS secret access key | - | Yes |
| `SECRET_REFRESHER_K8S_CONFIG_MODE` | K8s config mode (`cluster` or `local`) | `cluster` | No |
| `SECRET_REFRESHER_K8S_CONFIG_PATH` | Path to kubeconfig (only for local mode) | - | No |

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

## Deployment

Apply all resources in order:

```bash
# 1. Apply RBAC (secret-refresher specific)
kubectl apply -f infra/cron/secret-refresher/rbac.yaml

# 2. Apply ConfigMap (update values if needed)
kubectl apply -f infra/cron/secret-refresher/configmap.yaml

# 3. Apply Secret with your real AWS credentials
# IMPORTANT: Edit infra/cron/secret-refresher/secret.yaml first with your credentials!
kubectl apply -f infra/cron/secret-refresher/secret.yaml

# 4. Create initial Job to run immediately
kubectl apply -f infra/cron/secret-refresher/job.yaml

# 5. Apply CronJob for future hourly runs
kubectl apply -f infra/cron/secret-refresher/cronjob.yaml
```

Or apply all at once:
```bash
kubectl apply -f infra/cron/secret-refresher/
```

## How It Works

1. **Initial Deployment** - Run the Job immediately to create the secret for the first time
2. **Hourly Refresh** - The CronJob runs every hour on the hour (`0 * * * *`)
3. **ECR Token Generation** - Uses AWS SDK to get a fresh ECR authorization token
4. **Secret Creation/Update** - Creates a new secret or updates an existing one
5. **Job History** - Keeps the last 3 successful and 3 failed job records

## Monitor

Check secret creation:
```bash
kubectl get secret tgdex-registry-cred -n sandbox -o jsonpath='{.data\.dockerconfigjson}' | base64 -d | jq
```

Check job logs:
```bash
kubectl logs -n sandbox -l job-name=secret-creator-*
```

Check CronJob status:
```bash
kubectl get cronjob secret-creator -n sandbox
```

## Building

```bash
docker build -t ghcr.io/datakaveri/sandbox-secret-refresher:1.0.0 -f infra/cron/secret-refresher/Dockerfile .
```
