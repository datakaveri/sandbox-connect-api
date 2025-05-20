# Sandbox Backend Services

A backend service for managing Jupyter notebooks in Kubernetes.

## Table of Contents

- [Overview](#overview)
- [Setup](#setup)
- [API Documentation](#api-documentation)
  - [Notebook Endpoints](#notebook-endpoints)
- [Example Usage](#example-usage)
  - [Create Notebook](#create-notebook)
  - [Check Notebook Exists](#check-notebook-exists)
  - [Stop Notebook](#stop-notebook)
  - [Start Notebook](#start-notebook)
  - [Delete Notebook](#delete-notebook)
  - [List Notebooks](#list-notebooks)
- [Notebook Status Categories](#notebook-status-categories)
- [Development](#development)

## Overview

This service provides a REST API for managing Jupyter notebooks in a Kubernetes cluster. It allows users to create, start, stop, delete, and list notebooks.

## Setup

1. Clone the repository
2. Configure the `.env` file with your database and Kubernetes configuration
3. Run the service:

```bash
go run ./cmd/api/*.go
```

## API Documentation

### Notebook Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/notebook/create` | POST | Create a new notebook |
| `/notebook/check-exists` | POST | Check if a notebook exists |
| `/notebook/stop` | POST | Stop a running notebook |
| `/notebook/start` | POST | Start a stopped notebook |
| `/notebook/delete` | POST | Delete a notebook |
| `/notebook/list` | POST | List all notebooks in a namespace |
| `/pvc/check-exists` | POST | Check if a PVC exists |


## Example Usage

### Create Notebook

Creates a new notebook in the specified namespace.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/create \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-notebook",
    "namespace": "default",
    "storageSizeInGi": 10.0,
    "PVCName": "my-pvc",
    "cpu": {
      "request": 1.0,
      "limit": 2.0
    },
    "memoryInGi": {
      "request": 2.0,
      "limit": 4.0
    },
    "gpu": {
      "type": "nvidia.com/gpu",
      "limit": 1
    },
    "templateName": "ai"
  }'
```

**Response (201 Created):**

```json
{
  "message": "notebook creation is in process"
}
```



### Check Notebook Exists

Checks if a notebook with the given name exists in the specified namespace.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/check-exists \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-notebook",
    "namespace": "default"
  }'
```

**Response (200 OK):**

```json
{
  "exists": true
}
```



### Stop Notebook

Stops a running notebook.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/stop \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-notebook",
    "namespace": "default"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Notebook stopped successfully"
}
```



### Start Notebook

Starts a stopped notebook.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/start \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-notebook",
    "namespace": "default"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Notebook started successfully"
}
```



### Delete Notebook

Deletes a notebook.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/delete \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-notebook",
    "namespace": "default"
  }'
```

**Response (200 OK):**

```json
{
  "message": "Notebook deleted successfully"
}
```



### List Notebooks

Lists all notebooks in a namespace, categorized by status.

**Request:**

```bash
curl -X POST http://localhost:3000/notebook/list \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "default"
  }'
```

**Response (200 OK):**

```json
{
  "successful": [],
  "stopped": [],
  "pending": [],
  "failed": [],
  "orphaned": []
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
- `pvc-apply-failed`: PVC application failed
- `pvc-creation-failed`: PVC creation failed
- `pvc-upload-failed`: PVC upload failed
- `pvc-upload-apply-failed`: PVC upload application failed
- `notebook-apply-failed`: Notebook application failed

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
1. `picked`: The worker has picked up the notebook creation request
2. `pvc-applied`: PVC manifest has been applied to Kubernetes
3. `pvc-created`: PVC has been created in Kubernetes
4. `notebook-applied`: Notebook manifest has been applied to Kubernetes

After `notebook-applied`, the notebook will be in the `pending` category until Kubernetes reports that it's ready (`readyReplicas` = 1), at which point it moves to the `successful` category.

