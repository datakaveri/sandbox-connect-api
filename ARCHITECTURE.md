# Architecture: Sandbox Connect API

A detailed architecture analysis of the Sandbox Connect API system — a Go-based backend for managing Jupyter notebooks on Kubernetes (Kubeflow) with credit/billing integration.

## Table of Contents

- [High-Level Architecture](#high-level-architecture)
- [Services](#services)
  - [API Server](#api-server)
  - [Worker](#worker)
  - [Cron Job](#cron-job)
  - [Shared Packages](#shared-packages)
- [Notebook Lifecycle](#notebook-lifecycle)
  - [Phase 1: API Request](#phase-1-api-request)
  - [Phase 2: Worker Processing](#phase-2-worker-processing)
  - [Phase 3: Kubernetes Resource Creation](#phase-3-kubernetes-resource-creation)
- [Status State Machine](#status-state-machine)
- [Start / Stop / Delete Operations](#start--stop--delete-operations)
- [Concurrency and Locking Strategy](#concurrency-and-locking-strategy)
- [Error Handling and Retry Logic](#error-handling-and-retry-logic)
- [Database Schema](#database-schema)
- [Infrastructure and Deployment](#infrastructure-and-deployment)
- [Key Design Decisions](#key-design-decisions)
- [Directory Structure](#directory-structure)

---

## High-Level Architecture

The system follows an **asynchronous, event-driven architecture** with three main services:

```
┌─────────────┐    HTTP/REST     ┌─────────────────┐
│   Client    │ ───────────────▶ │   API Server    │
└─────────────┘                  │   (cmd/api/)    │
                                 └───────┬─────────┘
                                         │ INSERT (status: scheduled)
                                         ▼
                                 ┌─────────────────┐
                                 │   PostgreSQL     │
                                 │   Database       │
                                 └───────┬─────────┘
                                         │ POLL (picked_at IS NULL)
                                         ▼
                                 ┌─────────────────┐     ┌──────────────────┐
                                 │   Worker         │────▶│  Kubernetes      │
                                 │  (cmd/worker/)   │     │  (Kubeflow)      │
                                 └─────────────────┘     └──────────────────┘
                                                                  │
                                                         ┌────────┘
                                                         ▼
                                 ┌─────────────────────────────────┐
                                 │   Cron Job (every 15 min)       │
                                 │  (cmd/cron/profile-credit-sync) │
                                 │                                 │
                                 │  OpenCost ─▶ AAA API (credits)  │
                                 └─────────────────────────────────┘
```

---

## Services

### API Server

**Location:** `cmd/api/`

The REST API server handles all client-facing operations. Key features:

- **Authentication** via Keycloak JWT tokens
- **Rate limiting** (80 requests per 30 seconds)
- **CORS** support for cross-origin requests
- **Audit logging** via optional RabbitMQ integration
- **Swagger/OpenAPI** documentation

**API Endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/notebook/create` | POST | Create a new notebook |
| `/v1/notebook/start` | PATCH | Start a stopped notebook |
| `/v1/notebook/stop` | PATCH | Stop a running notebook |
| `/v1/notebook/delete` | DELETE | Delete a notebook |
| `/v1/notebook/list` | GET | List all user notebooks |
| `/v1/notebook/check-exists/{name}` | GET | Check if notebook exists |
| `/v1/notebook/status/{name}` | GET | Get notebook status |
| `/v1/profile/create` | POST | Create Kubeflow profile |
| `/v1/health` | GET | Health check |

**Key files:**

| File | Purpose |
|------|---------|
| `main.go` | Server entry point, initialization |
| `router.go` | HTTP route definitions |
| `handlers.go` | Request handlers for all endpoints |
| `middleware.go` | Auth, rate limiting, CORS middleware |
| `k8s.go` | Kubernetes operations (start/stop/delete) |
| `ecr.go` | AWS ECR image pull secret management |
| `audit.go` / `audit_middleware.go` | Audit logging via RabbitMQ |
| `types.go` | API request/response data types |
| `utils.go` | Utility functions, state determination |

### Worker

**Location:** `cmd/worker/`

The background worker processes notebook creation requests asynchronously. It runs as a **goroutine pool** with configurable concurrency (`MAX_CONCURRENT_WORKER`).

**Key files:**

| File | Purpose |
|------|---------|
| `main.go` | Worker entry point, goroutine pool management |
| `spawner.go` | Notebook creation orchestration logic |
| `k8s.go` | Kubernetes resource creation (PVC, Notebook CRD) |
| `db.go` | Database operations (fetch pending, update status) |
| `types.go` | Worker-specific data types |
| `utils.go` | Retry helpers |

### Cron Job

**Location:** `cmd/cron/profile-credit-sync/`

Runs every 15 minutes to synchronize user credit usage. Responsibilities:

- Fetch cost data from **OpenCost**
- Deduct credits via the **AAA API**
- Restrict GPU notebook creation when balance is low
- Retry previously failed credit deduction requests
- Sync Kubeflow profiles between Kubernetes and the database

**Key files:**

| File | Purpose |
|------|---------|
| `main.go` | Cron job entry point |
| `sync.go` | Credit synchronization logic |
| `db_operations.go` | Profile database operations |
| `k8s.go` | Kubernetes profile management |

### Shared Packages

**Location:** `pkg/`

| Package | Purpose |
|---------|---------|
| `pkg/db/` | PostgreSQL connection pool (pgx/v5) |
| `pkg/k8s/` | Kubernetes client wrapper and utilities |
| `pkg/s3/` | AWS S3 client for notebook template storage |
| `pkg/utils/` | Exponential backoff retry, URL helpers, general utilities |
| `pkg/constants/` | Shared constants across services |

---

## Notebook Lifecycle

### Phase 1: API Request

When a client sends `POST /v1/notebook/create`, the API handler performs:

1. **Authenticate** — Validate JWT token, extract user ID and namespace
2. **Validate input** — Check notebook name (4–50 chars, lowercase alphanumeric + hyphens), type (`cpu` or `gpu`), GPU access permissions
3. **Profile setup** — Auto-create Kubeflow Profile CRD and DB profile if the user is new
4. **ECR secret update** — Refresh the ECR image pull secret in the user's namespace (if enabled)
5. **Concurrency lock** — Begin a DB transaction and lock the user's profile row with `FOR UPDATE NOWAIT` (returns HTTP 429 if lock is held)
6. **Enforce resource limits** — Query DB + Kubernetes for running/total CPU/GPU notebook counts; reject if limits are exceeded
7. **Insert record** — Write a notebook row to the `notebooks` table with initial event `['scheduled']`
8. **Return 201** — Respond immediately with "Notebook creation is in process"

### Phase 2: Worker Processing

Each worker goroutine follows this loop:

1. **Poll** — Every 1 second, query for notebooks where `picked_at IS NULL` using `FOR UPDATE SKIP LOCKED` (so multiple workers safely pick different notebooks)
2. **Mark as picked** — Set `picked_at = NOW()`, append `"picked"` to the events array
3. **Create PVC** — Create a `PersistentVolumeClaim` named `{notebook-name}-pvc` with exponential backoff retry
4. **Create Notebook CRD** — Create the `kubeflow.org/v1beta1 Notebook` custom resource
5. **Update status** — Append `"pvc-applied"` then `"notebook-applied"` to the events array
6. **Cleanup on failure** — If any step fails, a deferred cleanup deletes both the Notebook CRD and PVC to prevent orphaned resources

### Phase 3: Kubernetes Resource Creation

#### PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {notebook-name}-pvc
  namespace: {user-namespace}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: {STORAGE_CLASS_NAME}
  resources:
    requests:
      storage: {configured storage size}
```

#### Notebook CRD

```yaml
apiVersion: kubeflow.org/v1beta1
kind: Notebook
metadata:
  name: {notebook-name}
  namespace: {user-namespace}
  labels:
    app: {notebook-name}
spec:
  template:
    spec:
      initContainers:
        - name: init-demo
          # Copies demo files (demo.ipynb, requirements.txt, etc.) from S3
      containers:
        - name: {notebook-name}
          image: {CPU or GPU notebook image}
          resources:
            requests: { cpu, memory }
            limits: { cpu, memory, gpu (if GPU type) }
          volumeMounts:
            - name: workspace
              mountPath: /home/jovyan
      volumes:
        - name: workspace
          persistentVolumeClaim:
            claimName: {notebook-name}-pvc
      serviceAccountName: default-editor
      nodeSelector: # GPU instance type or CPU affinity rules
```

---

## Status State Machine

### Internal Event Flow

```
scheduled ──▶ picked ──▶ pvc-applied ──▶ notebook-applied
                │              │
                │              ▼
                │        pvc-apply-failed ──▶ CLEANUP
                ▼
          notebook-apply-failed ──▶ CLEANUP
```

### Runtime State Derivation

Once a notebook reaches `notebook-applied`, its runtime state is derived by combining DB events with live Kubernetes state:

| State | Condition |
|-------|-----------|
| **opening** | Not yet applied, or applied but `readyReplicas == 0` |
| **running** | Applied + `readyReplicas > 0` + no stopped annotation |
| **stopped** | Has `kubeflow-resource-stopped` annotation on the CRD |
| **failed** | Any failure event present in the DB events array |
| **orphaned** | Applied in DB but Notebook CRD not found in Kubernetes |

---

## Start / Stop / Delete Operations

### Stop (`PATCH /v1/notebook/stop`)

1. Validate notebook exists and has reached `notebook-applied`
2. Add annotation `kubeflow-resource-stopped: <RFC3339 timestamp>` to the Notebook CRD
3. Kubeflow controller automatically scales replicas to 0

### Start (`PATCH /v1/notebook/start`)

1. Validate notebook exists and has reached `notebook-applied`
2. Update ECR secret if needed
3. Lock profile row and re-check resource limits and GPU credit balance
4. Remove `kubeflow-resource-stopped` annotation from the Notebook CRD
5. Kubeflow controller automatically scales replicas back up

### Delete (`DELETE /v1/notebook/delete`)

1. Validate notebook exists
2. Check state: must be `notebook-applied` or failed
3. Delete the record from the database
4. If not in a failed state:
   - Delete the Notebook CRD from Kubernetes (background propagation)
   - Delete the PVC from Kubernetes (background propagation)
5. Handle `NotFound` errors gracefully (resource may already be gone)

---

## Concurrency and Locking Strategy

The system uses **two complementary database locking patterns**:

### Profile-Level Lock (`FOR UPDATE NOWAIT`)

Used in create and start handlers to prevent two concurrent requests from the same user (e.g., creating two notebooks simultaneously). Returns HTTP 429 if the lock is already held.

### Notebook Picking Lock (`FOR UPDATE SKIP LOCKED`)

Used by the worker to safely distribute pending notebooks across multiple goroutines. Each worker picks a different notebook without conflicts.

### Additional Protections

- **PostgreSQL unique constraints** on `(name, namespace)` and `(pvc_name, namespace)` prevent duplicate notebooks (returns HTTP 409)
- **Kubernetes operations** handle `AlreadyExists` errors idempotently (stop retrying)

---

## Error Handling and Retry Logic

### Retry Configuration

| Context | Strategy | Initial Delay | Max Delay | Max Attempts | Factor |
|---------|----------|---------------|-----------|--------------|--------|
| Worker K8s ops | Exponential backoff | 5s | 1 min | 5 | 2.0x |
| Worker DB ops | Exponential backoff | 5s | 1 min | 5 | 2.0x |
| API K8s ops | Linear retry | — | — | — | — |
| API DB ops | Linear retry | — | — | — | — |

### Retry Control

Each retryable operation returns a signal:

- `RetryContinue` — Retry on error (transient failure)
- `RetryStop` — Stop retrying (e.g., `AlreadyExists`, `NotFound`)

### Cleanup on Failure

The worker uses a deferred cleanup pattern:

1. A `failed` flag is set to `true` at the start
2. On successful completion, `failed` is set to `false`
3. If the function exits with `failed == true`, cleanup deletes:
   - The Notebook CRD (if it exists)
   - The PVC (if it exists)

This ensures no orphaned Kubernetes resources are left behind.

---

## Database Schema

### Tables

#### `notebooks`

| Column | Type | Description |
|--------|------|-------------|
| `id` | SERIAL | Primary key |
| `user_id` | UUID | User identifier |
| `name` | VARCHAR | Notebook name (unique per namespace) |
| `namespace` | VARCHAR | User's Kubeflow namespace |
| `pvc_name` | VARCHAR | PVC name (unique per namespace) |
| `storage_size` | VARCHAR | PVC storage size |
| `cpu_request` / `cpu_limit` | VARCHAR | CPU resource requests/limits |
| `memory_request` / `memory_limit` | VARCHAR | Memory resource requests/limits |
| `gpu_type` / `gpu_request` / `gpu_limit` | VARCHAR (nullable) | GPU resources |
| `template_name` | VARCHAR (nullable) | Optional notebook template |
| `created_at` | TIMESTAMP | Creation timestamp |
| `picked_at` | TIMESTAMP (nullable) | When worker picked it up (`NULL` = pending) |
| `events` | TEXT[] | Array of status events (default: `['scheduled']`) |

**Indexes:**
- `idx_notebooks_user_id` — Fast lookup by user
- `idx_notebooks_namespace` — Fast lookup by namespace
- `idx_notebooks_created_at` — Ordering by creation time
- `idx_notebooks_picked_at_null` — **Partial index** for efficient pending-notebook queries

#### `profiles`

Stores user profiles with namespace, credit information, and GPU creation permissions.

#### `failed_aaa_requests`

Stores failed credit deduction requests for retry by the cron job.

---

## Infrastructure and Deployment

Everything runs in the Kubernetes namespace `sandbox`.

### Kubernetes Resources

| Component | Resource Type | Details |
|-----------|---------------|---------|
| API Server | Deployment + Service + Ingress | 1 replica, Nginx ingress with TLS (Let's Encrypt) |
| Worker | Deployment | 1 replica, long-running goroutine pool |
| Cron Job | CronJob | Every 15 minutes, `concurrencyPolicy: Forbid` |
| RBAC | ServiceAccount + ClusterRole + ClusterRoleBinding | `notebook-manager-sa` with access to Kubeflow notebooks/profiles, pods, PVCs, secrets |

### Container Images

Published to GitHub Container Registry (GHCR):

- `ghcr.io/datakaveri/tgdex-sandbox-connect-api`
- `ghcr.io/datakaveri/tgdex-sandbox-connect-worker`
- `ghcr.io/datakaveri/tgdex-sandbox-credit-sync-cron`

### CI/CD

GitHub Actions workflows (`.github/workflows/`):
- `publish-api-image.yaml` — Builds and pushes the API Docker image
- `publish-worker-image.yaml` — Builds and pushes the Worker Docker image

### External Dependencies

- **PostgreSQL** — Primary data store
- **Keycloak** — Authentication (JWT tokens)
- **OpenCost** — Kubernetes cost monitoring
- **AAA API** — Credit/billing management
- **AWS S3** — Notebook template storage
- **AWS ECR** — Container image registry
- **RabbitMQ** — Optional audit logging

---

## Key Design Decisions

1. **Asynchronous notebook creation** — Decouples the API response from the slow Kubernetes resource creation process, giving users immediate feedback (HTTP 201) while the worker handles the heavy lifting.

2. **Event sourcing for status** — The `events` array in the database tracks every status transition, providing a full audit trail of the notebook lifecycle.

3. **Exponential backoff retries** — Both Kubernetes and database operations use configurable retry with exponential backoff (5 attempts, 2x factor, 5s initial, 1 min max), making the system resilient to transient failures.

4. **Deferred cleanup** — The worker always cleans up partially created resources on failure, preventing orphaned PVCs and Notebook CRDs in the cluster.

5. **Database-level concurrency control** — `FOR UPDATE NOWAIT` on profiles and `FOR UPDATE SKIP LOCKED` on notebooks provide strong concurrency guarantees without application-level distributed locks.

6. **Credit-based billing** — The cron job monitors real-time costs via OpenCost and deducts credits through the AAA API, automatically restricting GPU access when a user's balance is low.

7. **Partial index for polling** — The `idx_notebooks_picked_at_null` partial index ensures the worker's polling query remains fast regardless of total notebook count.

---

## Directory Structure

```
sandbox-connect-api/
├── cmd/
│   ├── api/                          # REST API server
│   │   ├── main.go                   # Entry point
│   │   ├── router.go                 # Route definitions
│   │   ├── handlers.go               # HTTP handlers
│   │   ├── middleware.go             # Auth, rate limiting, CORS
│   │   ├── k8s.go                    # Kubernetes operations
│   │   ├── ecr.go                    # AWS ECR integration
│   │   ├── audit.go                  # Audit service
│   │   ├── audit_middleware.go       # Audit middleware
│   │   ├── types.go                  # Data types
│   │   ├── constants.go             # Constants
│   │   ├── utils.go                  # Utilities
│   │   ├── rabbitmq.go              # RabbitMQ client
│   │   └── utils_test.go            # Tests
│   ├── worker/                       # Background worker
│   │   ├── main.go                   # Entry point, goroutine pool
│   │   ├── spawner.go                # Notebook creation logic
│   │   ├── k8s.go                    # K8s resource creation
│   │   ├── db.go                     # Database operations
│   │   ├── types.go                  # Data types
│   │   ├── constants.go             # Constants
│   │   └── utils.go                  # Utilities
│   └── cron/
│       └── profile-credit-sync/      # Credit sync cron job
│           ├── main.go               # Entry point
│           ├── sync.go               # Credit sync logic
│           ├── db_operations.go      # DB operations
│           ├── k8s.go                # K8s profile management
│           ├── types.go              # Data types
│           ├── constants.go         # Constants
│           └── utils.go              # Utilities
├── pkg/                              # Shared packages
│   ├── constants/                    # Shared constants
│   ├── db/                           # PostgreSQL connection pool
│   ├── k8s/                          # Kubernetes client
│   ├── s3/                           # AWS S3 client
│   └── utils/                        # Retry logic, URL helpers
├── infra/                            # Kubernetes manifests
│   ├── api/                          # API deployment
│   ├── worker/                       # Worker deployment
│   ├── cron/profile-credit-sync/     # Cron job deployment
│   └── rbac.yaml                     # RBAC permissions
├── docs/                             # Swagger/OpenAPI specs
├── db.sql                            # Database schema
├── docker-compose.yaml               # Local dev (PostgreSQL)
├── go.mod / go.sum                   # Go dependencies
├── .env.all.example                  # Environment variables template
└── .github/workflows/                # CI/CD pipelines
```
