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
  "pending": [],
  "failed": []
}
```



