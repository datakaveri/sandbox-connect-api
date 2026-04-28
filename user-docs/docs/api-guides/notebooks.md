---
title: Notebooks
---

# Notebooks

Notebook APIs operate on notebooks created by bookings.

## Endpoints

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v1/notebook/list` | List notebooks for the current user |
| `GET` | `/v1/notebook/status/{notebook_name}` | Get notebook status |
| `GET` | `/v1/notebook/check-exists/{notebook_name}` | Check whether a notebook name exists |
| `PATCH` | `/v1/notebook/start` | Start a stopped notebook |
| `PATCH` | `/v1/notebook/stop` | Stop a running notebook |
| `DELETE` | `/v1/notebook/delete` | Delete a notebook |
| `GET` | `/v1/notebook/instance-types` | List GPU instance type display metadata |

## Important

`POST /v1/notebook/create` is not available in this branch. Use `POST /v1/bookings` to create a sandbox.

## Stop Request

```json
{
  "name": "demo-cpu-01"
}
```

The same request shape is used for start and delete operations.
