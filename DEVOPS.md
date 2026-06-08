# Sandbox Connect API — DevOps Setup Guide

This guide covers deploying the slot booking system in a new environment. It is written for the DevOps team and focuses on prerequisites, configuration, and deployment order.

---

## Table of Contents

1. [System Overview](#system-overview)
2. [Prerequisites](#prerequisites)
3. [Database Setup](#database-setup)
4. [Kubernetes Namespace & RBAC](#kubernetes-namespace--rbac)
5. [Secrets](#secrets)
6. [Deploying Services](#deploying-services)
   - [API Server](#1-api-server)
   - [Worker](#2-worker)
   - [Slot Lifecycle](#3-slot-lifecycle)
   - [Profile Credit Sync](#4-profile-credit-sync-cron)
7. [Environment Variable Reference](#environment-variable-reference)
8. [Local Development](#local-development)
9. [Building Docker Images](#building-docker-images)
10. [Verifying the Deployment](#verifying-the-deployment)

---

## System Overview

Four services make up the slot booking system:

| Service | Kind | Purpose |
|---|---|---|
| `api` | Deployment | REST API — booking management, notebook operations, auth |
| `worker` | Deployment | Async notebook provisioner — creates K8s PVCs and Kubeflow Notebook CRDs |
| `slot-lifecycle` | Deployment | Lifecycle state machine — advances bookings through scheduled → ready → active → completed |
| `profile-credit-sync` | CronJob | Syncs user credit usage with OpenCost + AAA every 15 minutes |

All services share a single PostgreSQL database. Only the API is externally reachable; the rest run internally.

**Slot booking flow:**

```
User books a slot (API)
  → booking inserted (status: scheduled)
  → slot-lifecycle fires at slot_start → creates notebook record (status: ready)
  → worker picks up notebook → creates PVC + Kubeflow Notebook CRD in K8s
  → slot-lifecycle detects notebook running → marks booking active
  → slot-lifecycle fires at slot_end → stops notebook, marks completed
  → slot-lifecycle fires after grace period → deletes K8s resources, marks cleaned up
```

---

## Prerequisites

- Kubernetes cluster with:
  - [Kubeflow](https://www.kubeflow.org/docs/components/notebooks/) installed (`kubeflow.org/v1beta1/notebooks` CRD must exist)
  - A `StorageClass` available for PVCs (e.g. `ebs-csi-storage-class`)
  - Nginx Ingress controller (for API ingress)
- PostgreSQL 14+ accessible from the cluster
- `kubectl` configured with cluster access
- Container registry credentials (ECR or private registry) if using private notebook images
- Keycloak realm + client set up for JWT auth
- (Optional) RabbitMQ for audit logging
- (Optional) OpenCost + AAA API for credit sync

---

## Database Setup

Run the schema file once against your PostgreSQL instance:

```bash
psql $POSTGRES_URL -f db.sql
```

This creates four tables: `notebooks`, `profiles`, `bookings`, `failed_aaa_requests`, along with all indexes and triggers.

**Connection string format:**
```
postgresql://<user>:<password>@<host>:<port>/<dbname>?sslmode=disable
```

---

## Kubernetes Namespace & RBAC

All resources live in the `sandbox` namespace.

```bash
kubectl create namespace sandbox
kubectl apply -f infra/rbac.yaml
```

`rbac.yaml` creates:
- `ServiceAccount`: `notebook-manager-sa` — used by all four services
- `ClusterRole`: permissions for `kubeflow.org/notebooks`, `profiles`, `pods`, `PVCs`, `secrets`
- `ClusterRoleBinding`: binds the role to `notebook-manager-sa`

---

## Secrets

Create the database secret (used by all services):

```bash
kubectl create secret generic database-creds \
  --namespace sandbox \
  --from-literal=POSTGRES_URL="postgresql://<user>:<password>@<host>:<port>/<dbname>?sslmode=disable"
```

Create the container registry pull secret:

```bash
# For ECR
kubectl create secret docker-registry tgdex-registry-cred \
  --namespace sandbox \
  --docker-server=<ecr-account>.dkr.ecr.<region>.amazonaws.com \
  --docker-username=AWS \
  --docker-password=$(aws ecr get-login-password --region <region>)

# For a private registry
kubectl create secret docker-registry tgdex-registry-cred \
  --namespace sandbox \
  --docker-server=<registry-url> \
  --docker-username=<username> \
  --docker-password=<password>
```

For S3 (used by the worker):

```bash
kubectl create secret generic s3-creds \
  --namespace sandbox \
  --from-literal=S3_ENDPOINT="<endpoint>" \
  --from-literal=S3_REGION="<region>" \
  --from-literal=S3_ACCESS_KEY="<access-key>" \
  --from-literal=S3_SECRET_KEY="<secret-key>" \
  --from-literal=S3_TEMPLATE_BUCKET_NAME="<bucket>"
```

For Keycloak service account (used by profile-credit-sync):

```bash
kubectl create secret generic profile-credit-sync-keycloak-creds \
  --namespace sandbox \
  --from-literal=PROFILE_CREDIT_SYNC_KEYCLOAK_USERNAME="<username>" \
  --from-literal=PROFILE_CREDIT_SYNC_KEYCLOAK_PASSWORD="<password>"
```

---

## Deploying Services

Apply in this order. Each service depends on the database secret existing.

### 1. API Server

Edit `infra/api/configmap.yaml` with your environment values, then:

```bash
kubectl apply -f infra/api/configmap.yaml
kubectl apply -f infra/api/manifest.yaml
kubectl apply -f infra/api/ingress.yaml      # if using ingress
```

**Health check:**
```bash
curl https://<api-host>/v1/health
```

### 2. Worker

Edit `infra/worker/configmap.yaml` with your notebook images and instance types, then:

```bash
kubectl apply -f infra/worker/configmap.yaml
kubectl apply -f infra/worker/deployment.yaml
```

The worker polls the database every second. No external traffic — it communicates only with Postgres and the K8s API.

### 3. Slot Lifecycle

```bash
kubectl apply -f infra/cron/slot-lifecycle/configmap.yaml
kubectl apply -f infra/cron/slot-lifecycle/deployment.yaml
```

Runs as a long-lived **Deployment** (`replicas: 1`). Ticks every 5 seconds (configurable via `SLOT_LIFECYCLE_TICK_INTERVAL_SECS`). Has a liveness probe that checks `/tmp/slot-lifecycle-alive` was written within 90 seconds.

> **Note:** There is also a legacy `cronjob.yaml` in the same directory. Do **not** apply both — use `deployment.yaml` only. If you previously ran the CronJob, delete it first:
> ```bash
> kubectl delete cronjob slot-lifecycle -n sandbox
> ```

### 4. Profile Credit Sync Cron

```bash
kubectl apply -f infra/cron/profile-credit-sync/configmap.yaml
kubectl apply -f infra/cron/profile-credit-sync/secret.yaml
kubectl apply -f infra/cron/profile-credit-sync/cronjob.yaml
```

Runs every 15 minutes. Uses `ConcurrencyPolicy: Forbid`.

---

## Environment Variable Reference

### API Server

| Variable | Required | Default | Description |
|---|---|---|---|
| `API_POSTGRES_URL` | yes | — | PostgreSQL connection string |
| `API_ADDRESS` | yes | — | Listen address e.g. `0.0.0.0:3000` |
| `API_VERSION` | yes | — | API version prefix e.g. `v1` |
| `API_KEYCLOAK_URL` | yes | — | Keycloak auth URL |
| `API_KEYCLOAK_REALM` | yes | — | Keycloak realm name |
| `API_KEYCLOAK_CLIENT_ID` | yes | — | Keycloak client ID |
| `API_KEYCLOAK_PUBLIC_KEY` | yes | — | RSA public key for JWT validation |
| `API_KYC_ENABLED` | yes | — | Enable KYC check (`true`/`false`) |
| `API_KUBEFLOW_URL` | yes | — | Kubeflow dashboard URL |
| `SLOT_CONFIG_PROFILE` | yes | — | Slot config profile name (`production`) |
| `API_KUBE_CONFIG_MODE` | no | `cluster` | `cluster` (in-cluster) or `local` |
| `API_KUBE_CONFIG_PATH` | no | `""` | Path to kubeconfig (local mode only) |
| `API_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `API_CORS_ORIGINS` | no | `*` | Allowed CORS origins |
| `API_RATE_LIMIT` | no | `80` | Requests per window |
| `API_RATE_WINDOW_SECS` | no | `30` | Rate limit window in seconds |
| `API_MAX_RUNNING_CPU` | no | — | Max concurrent running CPU notebooks per user |
| `API_MAX_RUNNING_GPU` | no | — | Max concurrent running GPU notebooks per user |
| `API_MAX_TOTAL_CPU` | no | — | Max total CPU notebooks per user |
| `API_MAX_TOTAL_GPU` | no | — | Max total GPU notebooks per user |
| `API_REGISTRY_SECRET_TYPE` | no | `none` | `ecr`, `private-registry`, or `none` |
| `RABBITMQ_HOST` | no | — | RabbitMQ host for audit logging |

### Worker

| Variable | Required | Default | Description |
|---|---|---|---|
| `WORKER_POSTGRES_URL` | yes | — | PostgreSQL connection string |
| `WORKER_MAX_CONCURRENT_WORKER` | yes | — | Goroutine concurrency (e.g. `5`) |
| `WORKER_CPU_NOTEBOOK_IMAGE` | yes | — | Container image for CPU notebooks |
| `WORKER_GPU_NOTEBOOK_IMAGE` | yes | — | Container image for GPU notebooks |
| `WORKER_INIT_CONTAINER_IMAGE` | yes | — | Init container image |
| `WORKER_S3_ENDPOINT` | yes | — | S3 endpoint URL |
| `WORKER_S3_REGION` | yes | — | AWS region |
| `WORKER_S3_ACCESS_KEY` | yes | — | AWS access key |
| `WORKER_S3_SECRET_KEY` | yes | — | AWS secret key |
| `WORKER_S3_TEMPLATE_BUCKET_NAME` | yes | — | S3 bucket for notebook templates |
| `WORKER_STORAGE_CLASS_NAME` | yes | — | K8s storage class for PVCs |
| `WORKER_CPU_NODE_INSTANCE_TYPES` | yes | — | Comma-separated CPU instance types |
| `WORKER_GPU_NODE_INSTANCE_TYPES` | yes | — | Comma-separated GPU instance types |
| `WORKER_KUBE_CONFIG_MODE` | no | `cluster` | `cluster` or `local` |
| `WORKER_IMAGE_PULL_ENABLED` | no | `false` | Pull images via ECR credentials |
| `WORKER_ECR_SECRET_NAME` | no | — | K8s secret name for ECR pull |

### Slot Lifecycle

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLOT_LIFECYCLE_POSTGRES_URL` | yes | — | PostgreSQL connection string |
| `SLOT_CONFIG_PROFILE` | no | `production` | Slot config profile name |
| `SLOT_LIFECYCLE_K8S_CONFIG_MODE` | no | `cluster` | `cluster` or `local` |
| `SLOT_LIFECYCLE_K8S_CONFIG_PATH` | no | `""` | Path to kubeconfig (local mode only) |
| `SLOT_LIFECYCLE_LOG_FORMAT` | no | `json` | `json` (production) or `text` (local dev) |
| `SLOT_LIFECYCLE_LOG_LEVEL` | no | `info` | `debug` or `info` |
| `SLOT_LIFECYCLE_BATCH_SIZE` | no | `50` | Max bookings processed per tick |
| `SLOT_LIFECYCLE_TICK_INTERVAL_SECS` | no | `5` | Seconds between lifecycle runs |
| `SLOT_LIFECYCLE_RUN_TIMEOUT_SECS` | no | `45` | Hard timeout per run (must be < tick interval is not required, but keep it reasonable) |

### Profile Credit Sync

| Variable | Required | Default | Description |
|---|---|---|---|
| `PROFILE_CREDIT_SYNC_POSTGRES_URL` | yes | — | PostgreSQL connection string |
| `PROFILE_CREDIT_SYNC_OPENCOST_URL` | yes | — | OpenCost API endpoint |
| `PROFILE_CREDIT_SYNC_AAA_URL` | yes | — | AAA (credit) API endpoint |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_URL` | yes | — | Keycloak auth URL |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_REALM` | yes | — | Keycloak realm |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_CLIENT_ID` | yes | — | Keycloak client ID |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_USERNAME` | yes | — | Service account username |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_PASSWORD` | yes | — | Service account password |
| `PROFILE_CREDIT_SYNC_K8S_CONFIG_MODE` | no | `cluster` | `cluster` or `local` |
| `PROFILE_CREDIT_SYNC_LOG_LEVEL` | no | `info` | `debug` or `info` |
| `PROFILE_CREDIT_SYNC_MAX_PROFILE_CAN_SYNC_AT_ONCE` | no | `50` | Sync batch size |

---

## Local Development

**Requirements:** Go 1.24+, access to a PostgreSQL instance, a kubeconfig pointing at a K8s cluster with Kubeflow.

1. Copy the env template and fill in values:
   ```bash
   cp .env.all.example .env
   # edit .env
   ```

2. Set `*_KUBE_CONFIG_MODE=local` and `*_KUBE_CONFIG_PATH=/home/<you>/.kube/config` for all services in `.env`.

3. Run each service:
   ```bash
   # API
   go run ./cmd/api/

   # Worker
   go run ./cmd/worker/

   # Slot lifecycle (text logs for readability)
   SLOT_LIFECYCLE_LOG_FORMAT=text go run ./cmd/cron/slot-lifecycle/

   # Profile credit sync (runs once and exits)
   go run ./cmd/cron/profile-credit-sync/
   ```

4. (Optional) Start a local Postgres with Docker Compose:
   ```bash
   docker compose up -d
   psql $POSTGRES_URL -f db.sql
   ```

---

## Building Docker Images

Each service has its own Dockerfile under `infra/`:

```bash
# API
docker build -f infra/api/Dockerfile -t <registry>/sandbox-connect-api:<tag> .

# Worker
docker build -f infra/worker/Dockerfile -t <registry>/sandbox-connect-worker:<tag> .

# Slot lifecycle
docker build -f infra/cron/slot-lifecycle/Dockerfile -t <registry>/slot-lifecycle:<tag> .

# Profile credit sync
docker build -f infra/cron/profile-credit-sync/Dockerfile -t <registry>/profile-credit-sync:<tag> .
```

The API image is a multi-stage build. In addition to the Go builder and Alpine runtime stages, `infra/api/Dockerfile` includes a `python:3.12-slim AS jupyterlite-builder` stage for JupyterLite. That stage installs `jupyterlite-core`, `jupyterlite-pyodide-kernel`, and `jupyter-server`, copies bundled workspace files from `jupyterlite-content/files`, and runs `jupyter lite build --contents=/lite/files --output-dir=/app/jupyterlite`.

The final API runtime image copies those generated static assets into `/app/jupyterlite`. The API deployment serves them using:

```yaml
API_JUPYTERLITE_BASE_URL: "/jupyterlite"
API_JUPYTERLITE_STATIC_DIR: "/app/jupyterlite"
```

Ingress or gateway routing must send the JupyterLite base path to the API service. If the public URL uses a prefix such as `/api/jupyterlite/lab/index.html`, configure the gateway to strip `/api` before forwarding, or set `API_JUPYTERLITE_BASE_URL` to the path that the API receives. The API route is protected by bearer-token authentication; no separate JupyterLite auth service is deployed. For app-launched browser tabs, the frontend must first call `POST /v1/jupyterlite/session` with the normal bearer token and `credentials: "include"`; the API sets an HttpOnly cookie that browser navigations and JupyterLite asset requests can send automatically. Direct opens without that cookie still return `401 Unauthorized`.

JupyterLite is therefore deployed with the API image, not as a separate notebook, worker, or cron image. If DevOps changes JupyterLite notebooks, sample files, Pyodide version, or JupyterLite package versions, rebuild and push the API image and roll out the API deployment. Worker and cron images do not need to be rebuilt for JupyterLite content changes.

For local asset generation, `scripts/build-jupyterlite.sh` runs the same JupyterLite build into a local `jupyterlite/` directory. Production deployment should still use the assets generated inside `infra/api/Dockerfile`.

All non-API service images are two-stage builds: `golang:1.24-alpine` for building, `alpine:3.18` for runtime. Update the `image:` field in the relevant deployment/cronjob YAML before applying.

---

## Verifying the Deployment

```bash
# Check all pods are running
kubectl get pods -n sandbox

# API health
kubectl exec -n sandbox deploy/sandbox-api -- curl -s http://localhost:3000/v1/health

# Slot lifecycle is ticking (liveness file updated every 5s)
kubectl exec -n sandbox deploy/slot-lifecycle -- cat /tmp/slot-lifecycle-alive

# Slot lifecycle logs (last 50 lines)
kubectl logs -n sandbox deploy/slot-lifecycle --tail=50

# Worker logs
kubectl logs -n sandbox deploy/sandbox-worker --tail=50

# Trigger a credit sync run manually
kubectl create job --from=cronjob/profile-credit-sync manual-credit-sync -n sandbox
```

**Expected slot-lifecycle log output (idle system):**

```
time=... level=INFO msg="slot lifecycle run complete" ... active_selected=0 ...
****
time=... level=INFO msg="slot lifecycle run complete" ...
```

Each `****` marks the end of one 5-second tick. If `active_to_shutting_down` or `slot_end_to_completed` counters are non-zero, the lifecycle is processing bookings.

---

## Common Issues

| Symptom | Likely cause |
|---|---|
| Worker pods stuck in `Pending` | `notebook-manager-sa` missing or RBAC not applied |
| Slot lifecycle pod restarts | Liveness probe failing — check if K8s API is reachable from the pod |
| Bookings stuck in `scheduled` | Slot lifecycle not running, or `SLOT_CONFIG_PROFILE` mismatch |
| Bookings stuck in `ready` | Worker not running, or notebook image pull failing |
| `unknown gpu category` in slot lifecycle logs | `SLOT_CONFIG_PROFILE` value doesn't match a defined profile in `gpuconfig` package |
| API returns 401 | `API_KEYCLOAK_PUBLIC_KEY` is wrong or expired |
