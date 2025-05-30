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

2. Copy `.env.api.example` to `.env` for the API server
```bash
cp .env.api.example .env
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

### Notebook Endpoints

| Endpoint | Method | Description | Success Response |
|----------|--------|-------------|------------------|
| `/notebook/create` | POST | Create a new notebook  | 201 Created |
| `/notebook/stop` | PATCH | Stop a running notebook  | 200 OK |
| `/notebook/start` | PATCH | Start a stopped notebook  | 200 OK |
| `/notebook/delete` | DELETE | Delete a notebook  | 200 OK |
| `/notebook/list` | GET | List all notebooks (optionally filter by date range) | 200 OK |
| `/notebook/check-exists/{notebook_name}` | GET | Check if notebook exists  | 200 OK |
| `/notebook/status/{notebook_name}` | GET | Get notebook status  | 200 OK |
| `/profile/create` | POST   | Create a new Kubeflow user profile/namespace | 201 Created      |

### Request and Response Examples

#### Create Notebook

Creates a new notebook in the specified namespace.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/create \
  -H "Content-Type: application/json" \
  -H "Authorization: your_api_key" \
  -d '{
    "name": "my-notebook",
    "type": "cpu"
  }'
```

**Response (201 Created):**

```json
{
  "message": "notebook creation is in process"
}
```

#### List Notebooks

Lists all notebooks for the authenticated user. You can filter by creation date range, limit the number of results, and paginate through results.

**Query Parameters:**

| Parameter | Type | Description | Example |
|-----------|------|-------------|----------|
| `filter`  | Array | Optional. Date range filter in format `[d1,d2]` where d1 and d2 are dates in YYYY-MM-DD format | `filter=[2024-01-01,2024-01-31]` |
| `limit`   | Integer | Optional. Maximum number of notebooks to return | `limit=10` |
| `offset`  | Integer | Optional. Number of notebooks to skip for pagination | `offset=20` |

**Request Example:**

```bash
# Example with all parameters
curl -X GET "http://localhost:3000/notebook/list?filter=[2024-01-01,2024-01-31]&limit=10&offset=20" \
  -H "Authorization: your_api_key"
```

**Response (200 OK):**

```json
{
  "notebooks": [
    {
      "id": 123,
      "name": "my-notebook",
      "namespace": "test-user",
      "storageSize": "10Gi",
      "pvcName": "my-notebook-pvc",
      "cpuRequest": 1,
      "cpuLimit": 2,
      "memoryRequest": "2Gi",
      "memoryLimit": "4Gi",
      "gpuType": "nvidia",
      "gpuCount": 1,
      "events": ["scheduled", "picked", "pvc-applied", "notebook-applied"],
      "status": "running"
    }
  ],
  "next_offset": -1
}
```

---

#### Check Notebook Exists

Checks if a notebook with the given name exists.

**Request:**

```bash
curl -X GET http://localhost:3000/notebook/check-exists/my-notebook \
  -H "Authorization: your_api_key"
```

**Response (200 OK):**

```json
{
  "exists": true
}
```

#### Check Notebook Status

Checks the current status of a notebook.

**Request:**

```bash
curl -X GET http://localhost:3000/notebook/status/my-notebook \
  -H "Authorization: keycloak_token"
```

**Response (200 OK):**

```json
{
  "id": 123,
  "name": "my-notebook",
  "namespace": "test-user",
  "storageSize": "10Gi",
  "pvcName": "my-notebook-pvc",
  "cpuRequest": 1,
  "cpuLimit": 2,
  "memoryRequest": "2Gi",
  "memoryLimit": "4Gi",
  "gpuType": "nvidia",
  "gpuCount": 1,
  "events": ["scheduled", "picked", "pvc-applied", "notebook-applied"],
  "status": "running"
}
```

#### Stop Notebook

Stops a running notebook.

**Request:**

```bash
curl -X PATCH http://localhost:3000/notebook/stop \
  -H "Content-Type: application/json" \
  -H "Authorization: keycloak_token" \
  -d '{
    "name": "my-notebook"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Notebook stopped successfully"
}
```

#### Start Notebook

Starts a stopped notebook.

**Request:**

```bash
curl -X PATCH http://localhost:3000/notebook/start \
  -H "Content-Type: application/json" \
  -H "Authorization: keycloak_token" \
  -d '{
    "name": "my-notebook"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Notebook started successfully"
}
```

#### Delete Notebook

Deletes a notebook.

**Request:**

```bash
curl -X DELETE http://localhost:3000/notebook/delete \
  -H "Content-Type: application/json" \
  -H "Authorization: keycloak_token" \
  -d '{
    "name": "my-notebook"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Delete request accepted"
}
```


#### Create Profile

Creates a new Kubeflow user profile, which in turn creates a new namespace in Kubernetes.

**Request:**

```bash
curl -X POST http://localhost:3000/profile/create \
  -H "Content-Type: application/json" \
  -H "Authorization: keycloak_token" 
```

**Response (201 Created):**

```json
{
  "message": "Kubeflow Profile created successfully"
}
```

## Notebook Status Categories

When listing notebooks, the API categorizes them into different groups based on their status:

### Successful

Notebooks in the `successful` category are fully deployed and ready to use. These notebooks:
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

### Pending

Notebooks in the `pending` category are still in the process of being created or are waiting for resources. A notebook is categorized as pending if:
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

After `notebook-applied`, the notebook will be in the `pending` category until Kubernetes reports that it's ready (`readyReplicas` = 1), at which point it moves to the `successful` category.
