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

1. Clone the Kubeflow manifests repository:
```bash
git clone https://github.com/kubeflow/manifests.git
cd manifests
cd checkout v1.10-branch
```

2. Configure Notebook Controller settings for auto-culling:
   Navigate to `apps/jupyter/notebook-controller/upstream/manager/params.env` and update:
```env
ENABLE_CULLING=true
CULL_IDLE_TIME=360
IDLENESS_CHECK_PERIOD=10
```

3. Configure OAuth2-Proxy settings:
   Navigate to `common/oauth2-proxy/base/oauth2_proxy.cfg` and update:
```properties
skip_provider_button = true
```

## Component Installation

Install the following components in order:
1. Istio

istio is used by most Kubeflow components to secure their traffic, enforce network authorization, and implement routing policies. If you use Cilium CNI on your cluster, you must configure it properly for Istio as shown here; otherwise, you will encounter RBAC access denied on the central dashboard.

Install Istio:
```bash
echo "Installing Istio configured with external authorization..."
kustomize build common/istio-1-24/istio-crds/base | kubectl apply -f -
kustomize build common/istio-1-24/istio-namespace/base | kubectl apply -f -
kustomize build common/istio-1-24/istio-install/overlays/oauth2-proxy | kubectl apply -f -

echo "Waiting for all Istio Pods to become ready..."
kubectl wait --for=condition=Ready pods --all -n istio-system --timeout 300s
```

2. OAuth2 Proxy

The oauth2-proxy extends your Istio Ingress-Gateway capabilities to function as an OIDC client. It supports user sessions as well as proper token-based machine-to-machine authentication.

```bash
echo "Installing oauth2-proxy..."

# Only uncomment ONE of the following overlays, as they are mutually exclusive.
# See `common/oauth2-proxy/overlays/` for more options.

# OPTION 1: works on most clusters, does NOT allow K8s service account
#           tokens to be used from outside the cluster via the Istio ingress-gateway.
#
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl apply -f -
kubectl wait --for=condition=Ready pod -l 'app.kubernetes.io/name=oauth2-proxy' --timeout=180s -n oauth2-proxy

# Option 2: works on Kind, K3D, Rancher, GKE, and many other clusters with the proper configuration, and allows K8s service account tokens to be used
#           from outside the cluster via the Istio ingress-gateway. For example, for automation with GitHub Actions.
#           In the end, you need to patch the issuer and jwksUri fields in the request authentication resource in the istio-system namespace 
#           as done in /common/oauth2-proxy/overlays/m2m-dex-and-kind/kustomization.yaml.
#           Please follow the guidelines in the section Upgrading and Extending below for patching.
#           curl --insecure -H "Authorization: Bearer `cat /var/run/secrets/kubernetes.io/serviceaccount/token`"  https://kubernetes.default/.well-known/openid-configuration
#           from a pod in the cluster should provide you with the issuer of your cluster.
# 
#kustomize build common/oauth2-proxy/overlays/m2m-dex-and-kind/ | kubectl apply -f -
#kubectl wait --for=condition=Ready pod -l 'app.kubernetes.io/name=oauth2-proxy' --timeout=180s -n oauth2-proxy
#kubectl wait --for=condition=Ready pod -l 'app.kubernetes.io/name=cluster-jwks-proxy' --timeout=180s -n istio-system

# OPTION 3: works on most EKS clusters with K8s service account
#           tokens to be used from outside the cluster via the Istio ingress-gateway.
#           You have to adjust AWS_REGION and CLUSTER_ID in common/oauth2-proxy/overlays/m2m-dex-and-eks/ first.
#
#kustomize build common/oauth2-proxy/overlays/m2m-dex-and-eks/ | kubectl apply -f -
#kubectl wait --for=condition=Ready pod -l 'app.kubernetes.io/name=oauth2-proxy' --timeout=180s -n oauth2-proxy
```

3. Dex

Dex is an OpenID Connect (OIDC) identity provider with multiple authentication backends. In this default installation, it includes a static user with the email user@example.com. By default, the user's password is 12341234. For any production Kubeflow deployment, you should change the default password by following the relevant section.

Install Dex:
```bash
echo "Installing Dex..."
kustomize build common/dex/overlays/oauth2-proxy | kubectl apply -f -
kubectl wait --for=condition=Ready pods --all --timeout=180s -n auth
```

4. Kubeflow Namespace

Create the namespace where the Kubeflow components will reside. This namespace is named kubeflow.

Install the Kubeflow namespace:
```bash
kustomize build common/kubeflow-namespace/base | kubectl apply -f -
```

5. Network Policies

Install network policies:
```bash
kustomize build common/networkpolicies/base | kubectl apply -f -
```

6. Kubeflow Roles

Create the Kubeflow ClusterRoles: kubeflow-view, kubeflow-edit, and kubeflow-admin. Kubeflow components aggregate permissions to these ClusterRoles.

Install Kubeflow roles:
```bash
kustomize build common/kubeflow-roles/base | kubectl apply -f -
```

7. Kubeflow Istio Resources

Create the Kubeflow Gateway kubeflow-gateway and ClusterRole kubeflow-istio-admin.

Install Kubeflow Istio resources:
```bash
kustomize build common/istio-1-24/kubeflow-istio-resources/base | kubectl apply -f -
```

8. Notebooks

Install the Notebook Controller and Jupyter Web Application:
```bash
# Install Notebook Controller
kustomize build apps/jupyter/notebook-controller/upstream/overlays/kubeflow | kubectl apply -f -

# Install Jupyter Web Application
kustomize build apps/jupyter/jupyter-web-app/upstream/overlays/istio | kubectl apply -f -
```

9. Profiles & KFAM

Install the Profile Controller and the Kubeflow Access-Management (KFAM):
```bash
kustomize build apps/profiles/upstream/overlays/kubeflow | kubectl apply -f -
```

10. User Namespaces

Finally, create a new namespace for the default user (named kubeflow-user-example-com):
```bash
kustomize build common/user-namespace/base | kubectl apply -f -
```

11. create ingress for notebooks
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: istio-gateway-ingress
  namespace: istio-system
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/rewrite-target: /
    nginx.ingress.kubernetes.io/enable-modsecurity: "false"
    nginx.ingress.kubernetes.io/enable-cors: "true"
    nginx.ingress.kubernetes.io/cors-allow-origin: "http://localhost:8080,http://localhost:5173,http://localhost:5174,http://localhost:4200,http://localhost:4007,https://staging.catalogue.tgdex.iudx.io,https://catalogue.tgdex.iudx.io,https://tgdex.telangana.gov.in"
    nginx.ingress.kubernetes.io/cors-allow-methods: "GET, POST, PUT, DELETE, OPTIONS"
    nginx.ingress.kubernetes.io/cors-allow-headers: "Content-Type, Authorization, X-Request-ID"
    nginx.ingress.kubernetes.io/cors-expose-headers: "X-Request-ID, X-Execution-Time"
    nginx.ingress.kubernetes.io/server-snippet: add_header Content-Security-Policy "default-src 'self'; img-src 'self' data:; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com data:; frame-ancestors 'self'; form-action 'self';" always;
    nginx.ingress.kubernetes.io/global-rate-limit: "1000"
    nginx.ingress.kubernetes.io/global-rate-limit-key: $server_name
    nginx.ingress.kubernetes.io/global-rate-limit-window: 1s
    nginx.ingress.kubernetes.io/limit-burst-multiplier: "1"
    nginx.ingress.kubernetes.io/limit-connections: "150"
    nginx.ingress.kubernetes.io/limit-rps: "100"
spec:
  ingressClassName: nginx
  rules:
  - host: sandbox.tgdex.telangana.gov.in
    http:
      paths:
        - path: /
          pathType: Prefix
          backend:
            service:
              name: istio-ingressgateway
              port:
                number: 80
  tls:
  - hosts:
    - sandbox.tgdex.telangana.gov.in
    secretName: kd-tls-cert
```

## Keycloak Integration
For connecting Keycloak with Kubeflow, follow the [./keycloak-dex-integeration.md](./keycloak-dex-integration.md).

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

