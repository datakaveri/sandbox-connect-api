# Sandbox Connect API

A backend service for managing Jupyter notebooks in Kubernetes with a RESTful API interface.

## Table of Contents

- [Overview](#overview)
- [Documentation](#documentation)
- [System Architecture](#system-architecture)
- [Deployment Modes (standalone / behind dx-gateway-go)](#deployment-modes)
- [Installation](#installation)
- [API Documentation](#api-documentation)
  - [Notebook Endpoints](#notebook-endpoints)
  - [Request and Response Examples](#request-and-response-examples)
- [Notebook Download Token Sessions](#notebook-download-token-sessions)
- [Notebook Status Categories](#notebook-status-categories)

## Overview

Sandbox Connect provides APIs and background controllers for managing Jupyter notebooks in a
Kubernetes cluster. It supports scheduled bookings and a direct-notebook mode.

The deployable components are the API, worker, slot-lifecycle controller, profile-credit-sync
CronJob, and platform-token sidecar. PostgreSQL coordinates state, while Kubernetes and Kubeflow
host the notebook workloads.

## Documentation

- [Documentation index](docs/README.md)
- [Architecture](docs/architecture.md)
- [Operations guide](docs/operations.md)
- [Configuration reference](docs/config/README.md)
- [Generated OpenAPI specification](docs/swagger.yaml)
- [OpenAPI 3 operation manifest (gateway)](openapi/openapi.yaml)
- [Deployment modes and gateway integration](docs/config/gateway.md)
- [Mode-specific Postman collections](postman/README.md)

## System Architecture

The API records user intent in PostgreSQL. In booking mode, slot lifecycle creates and advances
notebook work at the configured times. The worker claims pending notebook rows, resolves the
selected CPU/GPU template and storage policies, then creates the Kubeflow resources. The API
combines database events with live Kubernetes state when reporting notebook status.

See [docs/architecture.md](docs/architecture.md) for component boundaries, lifecycle diagrams,
storage ownership, token sessions, and concurrency behavior.

## Deployment Modes

The API runs **standalone** (the default — reached directly, verifies each bearer itself) or
**behind `dx-gateway-go`**, with the Go fleet's authentication and authorization: JWT validated at
the edge, a per-operation manifest enforced by the gateway, a verified workload credential on
every hop and the projected `X-Subject-*` identity. The choice is configuration only:

| `API_AUTH_MODE` | Behaviour |
|---|---|
| `standalone` (default) | unchanged behaviour |
| `gateway` | accepted only through the gateway (`WORKLOAD_VERIFIER_*` required); the local email/KYC/client checks still run on the forwarded bearer |
| `hybrid` | gateway hop when a workload credential is present, otherwise standalone — for migration |

The gateway loads [`openapi/openapi.yaml`](openapi/openapi.yaml) as this service's operation
manifest: every operation declares its `x-dx-authz` policy, and an undeclared operation is denied.
It is generated from the swag annotations by `./scripts/generate-openapi.sh`, and a conformance
test keeps it identical to the router. Full guide: [docs/config/gateway.md](docs/config/gateway.md).

Building needs `dx-common-go` beside this repo (`replace => ../dx-common-go`), as for every Go
fleet service. The Dockerfiles clone it in a `common` stage (`DX_COMMON_REF`, default `dev`).

## Installation

### Prerequisites

- Go 1.26.5 or a compatible newer version
- `dx-common-go` cloned beside this repo (the orchestrator's `make dev-clone` does this)
- PostgreSQL database
- Kubernetes cluster (or access to one)
- Docker (for containerized deployment)

### Setup

1. Clone the repository

```bash
git clone https://github.com/datakaveri/sandbox-connect-api.git
cd sandbox-connect-api
```

2. Copy `.env.all.example` to `.env` and configure all required variables

```bash
cp .env.all.example .env
```

3. Initialize the database

```bash
psql -U <username> -d <database_name> -f db.sql
```

4. Run the API server

```bash
go run ./cmd/api/
```

5. In a separate terminal, run the worker

```bash
go run ./cmd/worker/
```

## API Documentation

The service exposes generated ReDoc API reference at:

```text
/v1/apis/
```

Regenerate OpenAPI artifacts after changing Swag annotations or API response types. Every
operation needs `@ID` and `@x-dx-authz`; the script also rebuilds `openapi/openapi.yaml`:

```bash
./scripts/generate-openapi.sh
```

User-facing docs and tutorials live in `user-docs/`. They are authored separately from the generated API reference and sync `docs/swagger.yaml` plus `docs/swagger.json` into the docs site before start/build.

Postman collections and isolated example environments are available under [`postman/`](postman/README.md). Import the collection/environment pair for the configured `API_BOOKINGS_ENABLED` mode. These files are maintained manually when routes or request models change.

### Notebook Endpoints

When `API_BOOKINGS_ENABLED=true`, CPU/GPU sandboxes are created via `POST /v1/bookings` and
booking lifecycle endpoints. When it is false, the direct notebook create/start/stop/delete routes
are enabled instead.

| Endpoint | Method | Description | Success Response |
|----------|--------|-------------|------------------|
| `/v1/bookings` | POST | Create a CPU/GPU slot booking (notebook name + category + slot) | 201 Created |
| `/v1/notebook/list` | GET | List all notebooks (optionally filter by date range) | 200 OK |
| `/v1/notebook/check-exists/{notebook_name}` | GET | Check if notebook exists | 200 OK |
| `/v1/notebook/status/{notebook_name}` | GET | Get notebook status | 200 OK |
| `/v1/profile/create` | POST | Create a new Kubeflow user profile/namespace | 201 Created |

## Notebook Download Token Sessions

CPU notebook downloads use a delegated Keycloak session; browser refresh tokens are never sent to
or stored by Sandbox Connect.

Configuration is documented in one place per responsibility:

- [Canonical Keycloak and API setup](docs/config/api.md#canonical-keycloak-setup) covers the client
  import, secret rotation, Standard Token Exchange, browser audience mapper, scopes, and session
  lifetimes.
- [Platform token sidecar configuration](docs/config/platform-token-sidecar.md) covers refresh,
  identity validation, token publication, and readiness.
- [Worker configuration](docs/config/worker.md) covers the CPU/GPU Notebook template wiring and
  per-notebook session values.

At runtime, the frontend creates a token session with an authenticated bodyless `POST` to the
booking or direct-notebook token-session endpoint. The API returns `200` only after the delegated
access token is usable in the notebook; a `503` with `Retry-After` means the frontend must retry
before opening the notebook URL. The sidecar later persists refresh-token rotation with `PUT` to
the same endpoint.

## Notebook Status Categories

When listing notebooks, the API categorizes them into different groups based on their status:

### Running

Notebooks in the `running` category are fully deployed and ready to use. These notebooks:
- Have `notebook-applied` as their latest event
- Exist in Kubernetes
- Have `readyReplicas` set to 1 or more in their status

Users can connect to and use these notebooks.

### Stopped

Notebooks in the `stopped` category are valid notebooks that have been temporarily stopped by the user. These notebooks:
- Have `notebook-applied` as their latest event
- Exist in Kubernetes
- Have the `kubeflow-resource-stopped` annotation

These notebooks can be restarted using the start endpoint.

### Creating

Notebooks in the `creating` category are still in the process of being created or are waiting for resources. A notebook is categorized as creating if:
- Its latest event is not `notebook-applied` and not one of the failure events
- Its latest event is `notebook-applied` but it doesn't have `readyReplicas` set to 1 or more
- Its latest event is `notebook-applied` but there was an error parsing its Kubernetes spec

### Failed

Notebooks in the `failed` category encountered errors during creation. A notebook is categorized as failed if its latest event is one of:
- `pvc-apply-failed`: PVC apply failed
- `pvc-creation-failed`: PVC creation failed
- `pvc-upload-failed`: PVC upload failed
- `pvc-upload-apply-failed`: PVC upload apply failed
- `notebook-apply-failed`: Notebook apply failed

Failed notebooks indicate that something went wrong during the creation process and manual intervention may be required.

### Orphaned

Notebooks in the `orphaned` category represent an inconsistency between the database and Kubernetes. A notebook is categorized as orphaned if:
- Its latest event is `notebook-applied` (indicating it should exist in Kubernetes)
- BUT it cannot be found in the Kubernetes cluster

This should not happen under normal circumstances and may indicate:
- The notebook was deleted directly from Kubernetes without updating the database
- There was a communication issue with Kubernetes
- There was a database inconsistency

Orphaned notebooks should be investigated and cleaned up.

### Event Flow

The typical event flow for a notebook is:
1. `scheduled`: The notebook creation request has been scheduled
2. `picked`: The worker has picked up the notebook creation request
3. `pvc-applied`: PVC manifest has been applied to Kubernetes
4. `notebook-applied`: Notebook manifest has been applied to Kubernetes

After `notebook-applied`, the notebook will be in the `creating` category until Kubernetes reports that it's ready (`readyReplicas` = 1), at which point it moves to the `running` category.
