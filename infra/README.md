# Infrastructure Setup
This directory contains the infrastructure configuration for deploying the Sandbox Connect API and Worker services to Kubernetes.

## Directory Structure

- `deployment/api/`: Deployment configuration for the API service
- `deployment/worker/`: Deployment configuration for the Worker service



## Deployment Steps
We are deploying in sandbox namespace

1. Build the Docker images:

```bash
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-api:latest -f infra/deployment/api/Dockerfile .
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-worker:latest -f infra/deployment/worker/Dockerfile .
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-api:latest
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-worker:latest
```

2. RBAC
apply rbac.yaml to create the required roles and rolebindings
```bash
kubectl apply -f infra/rbac.yaml
```
3. Secrets 
- Database
create the secrets directly with kubectl:
```bash
kubectl create secret generic database-creds \
  --from-literal=POSTGRES_URL="<url>"
```

- S3 
create the secret directly with kubectl:
```bash
kubectl create secret generic s3-creds \
  --from-literal=S3_ENDPOINT="<endpoint>" \
  --from-literal=S3_REGION="<region>" \
  --from-literal=S3_ACCESS_KEY="<access-key>" \
  --from-literal=S3_SECRET_KEY="<secret-key>" \
  --from-literal=S3_TEMPLATE_BUCKET_NAME="<bucket-name>"
```

- API Authentication
create the API key secret for authentication:
```bash
kubectl create secret generic api-auth \
  --from-literal=API_KEY="<your-secure-api-key>"
```


4. Deploy the api and workers:

```bash
kubectl apply -f infra/deployment/api/deployment.yaml
kubectl apply -f infra/deployment/worker/deployment.yaml
```

