# Sandbox Connect API

A backend service for managing Jupyter notebooks in Kubernetes with a RESTful API interface.

## Table of Contents

- [Overview](#overview)
- [System Architecture](#system-architecture)
- [Installation](#installation)
- [API Documentation](#api-documentation)
  - [Notebook Endpoints](#notebook-endpoints)
  - [Request and Response Examples](#request-and-response-examples)
- [Notebook Status Categories](#notebook-status-categories)

## Overview

Sandbox Connect API provides a RESTful API for managing Jupyter notebooks in a Kubernetes cluster. It allows users to create, start, stop, delete, and list notebooks. The system consists of two main components:

1. **API Server**: Handles HTTP requests and communicates with the database
2. **Worker**: Processes notebook creation requests and interacts with Kubernetes

## System Architecture

The system is designed with the following components:

- **API Server**: Handles HTTP requests, validates user input, and communicates with the database
- **Worker**: Monitors the database for new notebook requests and creates the necessary Kubernetes resources
- **PostgreSQL Database**: Stores notebook configurations and states
- **Kubernetes**: Hosts the Jupyter notebook instances

## Installation

### Prerequisites

- Go 1.21 or higher
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

4. Initialize the database
```bash
psql -U <username> -d <database_name> -f db.sql
```

5. Run the API server
```bash
go run ./cmd/api/
```

6. In a separate terminal, run the worker
```bash
go run ./cmd/worker/
```

## API Documentation

The service exposes generated ReDoc API reference at:

```text
/v1/apis/
```

Regenerate OpenAPI artifacts after changing Swag annotations or API response types:

```bash
./scripts/generate-openapi.sh
```

User-facing docs and tutorials live in `user-docs/`. They are authored separately from the generated API reference and sync `docs/swagger.yaml` plus `docs/swagger.json` into the docs site before start/build.

### Notebook Endpoints

CPU/GPU sandboxes are **created via bookings** (`POST /v1/bookings`); the worker provisions the notebook at the scheduled time. Direct `POST /v1/notebook/create` is not supported.

| Endpoint | Method | Description | Success Response |
|----------|--------|-------------|------------------|
| `/v1/bookings` | POST | Create a CPU/GPU slot booking (notebook name + category + slot) | 201 Created |
| `/notebook/stop` | PATCH | Stop a running notebook  | 200 OK |
| `/notebook/start` | PATCH | Start a stopped notebook  | 200 OK |
| `/notebook/delete` | DELETE | Delete a notebook  | 200 OK |
| `/notebook/list` | GET | List all notebooks (optionally filter by date range) | 200 OK |
| `/notebook/check-exists/{notebook_name}` | GET | Check if notebook exists  | 200 OK |
| `/notebook/status/{notebook_name}` | GET | Get notebook status  | 200 OK |
| `/profile/create` | POST   | Create a new Kubeflow user profile/namespace | 201 Created      |

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

### creating 

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
