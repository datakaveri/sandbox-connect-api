# Sandbox Connect operations

This guide covers building, deploying, verifying, and troubleshooting Sandbox Connect. It avoids
duplicating environment-variable tables; use the [configuration reference](config/README.md) for
field-level values and cross-service constraints.

## Deployment inventory

| Component | Dockerfile | Kubernetes manifest |
|---|---|---|
| API | `infra/api/Dockerfile` | `infra/api/manifest.yaml` |
| Worker | `infra/worker/Dockerfile` | `infra/worker/deployment.yaml` |
| Slot lifecycle | `infra/cron/slot-lifecycle/Dockerfile` | `infra/cron/slot-lifecycle/deployment.yaml` |
| Profile credit sync | `infra/cron/profile-credit-sync/Dockerfile` | `infra/cron/profile-credit-sync/cronjob.yaml` |
| Platform token sidecar | `infra/platform-token-sidecar/Dockerfile` | Embedded in both worker Notebook templates |

The API, worker, and slot lifecycle are Deployments. Profile credit sync is a CronJob. The sidecar
has no standalone Deployment.

## Prerequisites

- Kubernetes with the Kubeflow Notebook and Profile CRDs used by this repository.
- A writable StorageClass for managed/profile PVCs and any externally provisioned PVCs referenced
  by the worker templates.
- PostgreSQL reachable from all four database-backed components.
- Keycloak for API authentication and, when notebook downloads are enabled, the delegated
  notebook client.
- A container registry and pull credentials for private service/notebook images.
- Optional integrations required by enabled configuration: S3-compatible storage, OpenCost,
  AAA/credit API, RabbitMQ, ingress, and Istio.

Cluster bootstrap for Kubeflow and OpenCost remains in [infra/README.md](../infra/README.md).

## Authoritative configuration

Before deploying, review:

- [cross-service constraints](config/README.md#cross-service-fields);
- [API configuration](config/api.md);
- [worker configuration](config/worker.md) and the
  [Notebook template contract](../infra/worker/README.md);
- [slot lifecycle configuration](config/slot-lifecycle.md);
- [profile credit sync configuration](config/profile-credit-sync.md); and
- [platform token sidecar configuration](config/platform-token-sidecar.md).

For delegated notebook downloads, complete the
[canonical Keycloak setup](config/api.md#canonical-keycloak-setup) before launching notebooks.

## Deployment order

### 1. Database

Apply the schema from the repository root:

```bash
psql "$POSTGRES_URL" -f db.sql
```

Use the same database for the API, worker, slot lifecycle, and profile credit sync.

### 2. Namespace and RBAC

```bash
kubectl create namespace sandbox
kubectl apply -f infra/rbac.yaml
```

The manifests use ServiceAccount `notebook-manager-sa`. Review the cluster-scoped permissions
before applying them in a new environment.

### 3. Secrets

Create or update the environment-specific Secrets before workloads:

| Secret | Consumers | Source/template |
|---|---|---|
| `database-creds` | API, worker, slot lifecycle, profile credit sync | Environment-managed PostgreSQL DSN |
| `api-creds` | API | `infra/api/secret.yaml` |
| `s3-creds` | Worker | Environment-managed S3 credentials |
| `profile-credit-sync-keycloak-creds` | Profile credit sync | `infra/cron/profile-credit-sync/secret.yaml` |
| Registry pull secret | Service and notebook pods | Name must match API and worker template configuration |

Never commit filled Secret manifests. The notebook-client secret generated after Keycloak import
belongs in `api-creds`.

### 4. ConfigMaps and worker templates

Review every placeholder, hostname, image, StorageClass, PVC, and feature flag before applying:

```bash
kubectl apply -f infra/api/configmap.yaml
kubectl apply -f infra/worker/configmap.yaml
kubectl apply -f infra/cron/slot-lifecycle/configmap.yaml
kubectl apply -f infra/cron/profile-credit-sync/configmap.yaml
```

CPU and GPU Notebook templates are versioned with the worker image contract. Roll them forward or
back together.

### 5. Workloads

```bash
kubectl apply -f infra/api/manifest.yaml
kubectl apply -f infra/worker/deployment.yaml
kubectl apply -f infra/cron/slot-lifecycle/deployment.yaml
kubectl apply -f infra/cron/profile-credit-sync/cronjob.yaml
```

If upgrading a cluster that still has the retired slot-lifecycle CronJob, remove that old
CronJob after confirming the Deployment is healthy so only one lifecycle controller is active.

### 6. Ingress and mesh policy

Choose the ingress manifest appropriate for the environment:

- `infra/api/ingress.yaml`
- `infra/api/ingress-kubeflow.yaml`

When notebook namespaces use Istio enforcement, apply and adapt
`infra/istio/notebook-token-ready-authorizationpolicy.yaml`; see
[infra/istio/README.md](../infra/istio/README.md).

## Build and publish images

Use an immutable tag derived from the commit being built:

```bash
REGISTRY=ghcr.io/datakaveri
VERSION=1.0.0
COMMIT_SHA=$(git rev-parse --short=8 HEAD)

docker build --file infra/api/Dockerfile \
  --tag "$REGISTRY/sandbox-connect-api:$VERSION-$COMMIT_SHA" .

docker build --file infra/worker/Dockerfile \
  --tag "$REGISTRY/sandbox-connect-worker:$VERSION-$COMMIT_SHA" .

docker build --file infra/cron/slot-lifecycle/Dockerfile \
  --tag "$REGISTRY/sandbox-connect-slot-lifecycle:$VERSION-$COMMIT_SHA" .

docker build --file infra/cron/profile-credit-sync/Dockerfile \
  --tag "$REGISTRY/sandbox-credit-sync:$VERSION-$COMMIT_SHA" .

docker build --file infra/platform-token-sidecar/Dockerfile \
  --tag "$REGISTRY/platform-token-sidecar:$VERSION-$COMMIT_SHA" .
```

Push the exact tags referenced by the manifests/templates. The API image also contains the
JupyterLite build; changing JupyterLite packages or bundled content requires rebuilding only the
API image.

`Jenkinsfile` is the current multi-service CI/CD definition. Branch rules in that file determine
which builds deploy automatically; do not infer deployment behavior from old GitHub workflow
names.

## Local development

Requirements are Go 1.24.2 or a compatible newer Go toolchain, PostgreSQL, and access to a
Kubernetes cluster containing the required Kubeflow CRDs.

```bash
cp .env.all.example .env
docker compose up -d
psql "$POSTGRES_URL" -f db.sql

go run ./cmd/api
go run ./cmd/worker
go run ./cmd/cron/slot-lifecycle
go run ./cmd/cron/profile-credit-sync
```

Set each component's Kubernetes mode/path fields for local kubeconfig access. Profile credit sync
runs once and exits; slot lifecycle is long-running.

## Verification

```bash
kubectl get deployments,cronjobs,pods -n sandbox
kubectl rollout status deployment/sandbox-api -n sandbox
kubectl rollout status deployment/sandbox-worker -n sandbox
kubectl rollout status deployment/slot-lifecycle -n sandbox

kubectl logs deployment/sandbox-api -n sandbox --tail=100
kubectl logs deployment/sandbox-worker -n sandbox --tail=100
kubectl logs deployment/slot-lifecycle -n sandbox --tail=100

curl -fsS https://<api-host>/v1/health
```

For a full functional check:

1. authenticate through the configured browser client;
2. create a profile;
3. create a booking or direct notebook, according to `API_BOOKINGS_ENABLED`;
4. confirm the worker applies required PVCs and the Kubeflow Notebook;
5. confirm the API reports the expected runtime state;
6. when platform tokens are enabled, create a token session and confirm sidecar readiness; and
7. terminate/delete the notebook and verify managed cleanup preserves external PVCs.

## Troubleshooting

| Symptom | First checks |
|---|---|
| API returns 401 for every request | Realm, client ID, and pinned `API_KEYCLOAK_PUBLIC_KEY`; see `config/api.md` |
| Worker fails at startup | Both mounted Notebook template paths and strict template validation |
| Booking remains `scheduled` | Slot lifecycle Deployment health, database DSN, and matching `SLOT_CONFIG_PROFILE` |
| Booking remains `ready` | Worker logs, Notebook pod events, image pulls, required PVC availability |
| Notebook token session returns 503 | Sidecar logs, readiness port/policy, Secret projection, Keycloak exchange settings |
| Long-running notebook loses file access | Keycloak session lifetime, refresh response, and rotated-token PUT reachability |
| Runtime injection fails | Workspace policy, PVC access, S3/Git source, helper-pod scheduling |
| Credit sync fails or overlaps | CronJob events/logs, `concurrencyPolicy`, OpenCost/AAA reachability, failed request table |

Use component logs together with the relevant configuration reference. Avoid fixing a
cross-service mismatch in only one manifest.
