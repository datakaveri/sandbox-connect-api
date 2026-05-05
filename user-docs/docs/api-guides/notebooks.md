---
title: Notebooks
---

# Notebooks

Notebook APIs operate on notebooks created by bookings.
Notebook lifecycle actions are booking-owned: use booking cancel, terminate, reset, or extend endpoints instead of mutating notebooks directly.

## Endpoints

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v1/notebook/list` | List notebooks for the current user |
| `GET` | `/v1/notebook/status/{notebook_name}` | Get notebook status |
| `GET` | `/v1/notebook/check-exists/{notebook_name}` | Check whether a notebook name exists |
| `GET` | `/v1/notebook/instance-types` | List GPU instance type display metadata |

## Important

`POST /v1/notebook/create` is not available in this branch. Use `POST /v1/bookings` to create a sandbox.

`PATCH /v1/notebook/start`, `PATCH /v1/notebook/stop`, and `DELETE /v1/notebook/delete` are not available. Use `PATCH /v1/bookings/{id}/cancel` before a session starts, `PATCH /v1/bookings/{id}/terminate` to end an active session early, `PATCH /v1/bookings/{id}/reset` to clean up a stuck session, and `PATCH /v1/bookings/{id}/extend` to keep an active session running longer.
