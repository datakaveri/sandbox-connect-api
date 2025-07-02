# Infrastructure Setup
This directory contains the infrastructure configuration for deploying the Sandbox Connect API, Worker services, and Cron jobs to Kubernetes.

# Deploying OpenCost

## Overview
OpenCost provides real-time cost monitoring for Kubernetes workloads.

## Installation Steps

1. Add OpenCost Helm repository:
```bash
helm repo add opencost-charts https://opencost.github.io/opencost-helm-chart
helm repo update
```

2. Create `values.yaml` with the following configuration:
```yaml
opencost:
  exporter:
    env:
      - name: PROMETHEUS_SERVER_ENDPOINT
        value: "http://prometheus-server.mon-stack.svc.cluster.local:80"
    resources:
      requests:
        memory: "7.5Gi"
        cpu: "3"
      limits:
        memory: "8Gi"
        cpu: "4"
  prometheus:
    internal:
      enabled: false
    external:
      enabled: true
  ui:
    enabled: true
    resources:
      requests:
        memory: "512Mi"
        cpu: "100m"
      limits:
        memory: "1Gi"
        cpu: "500m"
```

Deploy 
```bash
helm install opencost opencost-charts/opencost --namespace opencost --create-namespace -f values.yaml
```

# Deploying Kubeflow

## Prerequisites
- Kubernetes cluster
- Cert Manager v1.17.2 (already installed)
- Git

## Pre-Installation Configuration

1. Configure Notebook Controller settings for auto-culling:
   Navigate to `manifests/apps/jupyter/notebook-controller/upstream/manager/params.env` and update:
```env
ENABLE_CULLING=true
CULL_IDLE_TIME=360
IDLENESS_CHECK_PERIOD=10
```

2. Configure OAuth2-Proxy settings:
   Navigate to `manifests/common/oauth2-proxy/base/oauth2_proxy.cfg` and update:
```cfg
skip_provider_button = true
```
## Installation Steps

1. Clone the manifests repository:
```bash
git clone https://github.com/kubeflow/manifests.git
```

2. Install components in the following order:
   - Istio
   - OAuth2 Proxy
   - Dex
   - Kubeflow Namespace
   - Network Policies
   - Kubeflow Roles
   - Kubeflow Istio Resources
   - Notebooks
   - Profiles & KFAM
   - User Namespaces

For detailed installation instructions for each component, refer to the [Readme of Kubeflow manifests](https://github.com/kubeflow/manifests?tab=readme-ov-file#install-individual-components).

## Keycloak Integration
For connecting Keycloak with Kubeflow, follow the [Dex configuration guide](https://github.com/kubeflow/manifests/blob/ad65081672344022dccb1356c830b789c6060d31/common/dex/README.md).

## Database Setup

The project uses PostgreSQL as its database. The schema includes tables for managing notebooks, user profiles, and failed aaa requests.

1. Create the database in your PostgreSQL instance
2. Apply the schema from `db.sql` in the root directory:
```bash
psql -U <username> -d <database_name> -f db.sql
```

The schema includes:
- `notebooks`: Manages Kubernetes notebook instances
- `profiles`: Stores user profiles and credit information
- `failed_aaa_requests`: Tracks failed accounting requests
- Necessary indexes and triggers for performance optimization

## deploying sandbox servies 
1. Build and pull Docker images:

```bash
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-api:tgdex-1.0.0 -f infra/api/Dockerfile .
docker build -t ghcr.io/datakaveri/tgdex-sandbox-connect-worker:tgdex-1.0.0 -f infra/worker/Dockerfile .
docker build -t ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron:tgdex-1.0.0 -f infra/cron/profile-credit-sync/Dockerfile .
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-api:tgdex-1.0.0
docker push ghcr.io/datakaveri/tgdex-sandbox-connect-worker:tgdex-1.0.0
docker push ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron:tgdex-1.0.0
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
kubectl apply -f infra/cron/profile-credit-sync/secret.yaml
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
kubectl apply -f infra/api/manifest.yaml
kubectl apply -f infra/worker/deployment.yaml
kubectl apply -f infra/cron/profile-credit-sync/cronjob.yaml
```

6. Deploy Ingress
```bash
kubectl apply -f infra/api/ingress.yaml
```

