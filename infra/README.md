# Infrastructure Setup
This directory contains the infrastructure configuration for deploying the Sandbox Connect API, Worker services, and Cron jobs to Kubernetes.


## Deployment Steps
We are deploying in sandbox namespace

1. Build and pull Docker images:

```bash
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-api:latest -f infra/api/Dockerfile .
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-worker:latest -f infra/worker/Dockerfile .
docker build -t ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron:latest -f infra/cron/profile-credit-sync/Dockerfile .
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-api:latest
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-worker:latest
docker push ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron:latest
```

2. RBAC Setup
Apply rbac.yaml to create the required roles and rolebindings:
```bash
kubectl apply -f infra/rbac.yaml
```

3. Secrets Setup

a. Docker Registry Secret:
```bash
kubectl create secret docker-registry tgdex-registry-cred \
  --docker-server=<url> \
  --docker-username=<username> \
  --docker-password=<password> \
  -n sandbox
```

b. Database Credentials:
```bash
kubectl create secret generic database-creds \
  --from-literal=POSTGRES_URL="<url>" \
  -n sandbox
```

c. S3 Credentials:
```bash
kubectl create secret generic s3-creds \
  --from-literal=S3_ENDPOINT="<endpoint>" \
  --from-literal=S3_REGION="<region>" \
  --from-literal=S3_ACCESS_KEY="<access-key>" \
  --from-literal=S3_SECRET_KEY="<secret-key>" \
  --from-literal=S3_TEMPLATE_BUCKET_NAME="<bucket-name>" \
  -n sandbox
```


e. Keycloak Credentials:
Before applying the Keycloak credentials, make sure to edit the `infra/api/secret.yaml` file and replace the following values:
- `<username>`: Your Keycloak admin username
- `<password>`: Your Keycloak admin password

Then apply the secret:
```bash
kubectl apply -f infra/api/secret.yaml
```

4. Service Configuration
Apply the configuration maps for API and cron jobs:
```bash
kubectl apply -f infra/api/configmap.yaml
kubectl apply -f infra/cron/profile-credit-sync/configmap.yaml
```

5. Deploy Services
Deploy the API, workers, and cron jobs:
```bash
kubectl apply -f infra/api/deployment.yaml
kubectl apply -f infra/worker/deployment.yaml
kubectl apply -f infra/cron/profile-credit-sync/cronjob.yaml
```

