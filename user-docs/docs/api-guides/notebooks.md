---
title: Notebooks
---

# Notebooks

Notebook APIs can run in two modes.

When `API_BOOKINGS_ENABLED=true`, notebooks are created by bookings and lifecycle actions are booking-owned. Use booking cancel, terminate, reset, or extend endpoints instead of mutating notebooks directly.

When `API_BOOKINGS_ENABLED=false`, bookings and scheduling are disabled. Use the direct notebook endpoints to create, start, stop, inspect, list, delete, and manage notebook token sessions. Direct notebooks stay live until stopped or deleted. Direct create also accepts `fileUrl`, `gitUrl`, `gitAccessToken`, and `gitTokenSecretName`; the worker injects those runtime assets into the notebook PVC before the notebook starts, matching booking-mode behavior.

## Endpoints

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v1/notebook/list` | List notebooks for the current user |
| `GET` | `/v1/notebook/status/{notebook_name}` | Get notebook status |
| `GET` | `/v1/notebook/check-exists/{notebook_name}` | Check whether a notebook name exists |
| `GET` | `/v1/notebook/instance-types` | List GPU instance type display metadata |
| `POST` | `/v1/notebook/create` | Create a notebook directly when bookings are disabled |
| `PATCH` | `/v1/notebook/start` | Start a stopped direct notebook when bookings are disabled |
| `PATCH` | `/v1/notebook/stop` | Stop a direct notebook when bookings are disabled |
| `DELETE` | `/v1/notebook/delete` | Delete a direct notebook and PVC when bookings are disabled |
| `POST` | `/v1/notebook/{notebook_name}/notebook-token-session` | Create a token session for a direct notebook when bookings are disabled |
| `PUT` | `/v1/notebook/{notebook_name}/notebook-token-session` | Rotate a direct notebook token session when bookings are disabled |

## Important

`POST /v1/notebook/create`, `PATCH /v1/notebook/start`, `PATCH /v1/notebook/stop`, and `DELETE /v1/notebook/delete` are available only when `API_BOOKINGS_ENABLED=false`.

When `API_BOOKINGS_ENABLED=true`, use `POST /v1/bookings` to create a sandbox. Use `PATCH /v1/bookings/{id}/cancel` before a session starts, `PATCH /v1/bookings/{id}/terminate` to end an active session early, `PATCH /v1/bookings/{id}/reset` to clean up a stuck session, and `PATCH /v1/bookings/{id}/extend` to keep an active session running longer.
