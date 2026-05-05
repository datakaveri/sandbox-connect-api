---
title: Bookings
---

# Bookings

Bookings are the main user workflow for creating sandbox notebooks.

## Endpoints

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `POST` | `/v1/bookings` | Create a CPU or GPU booking |
| `GET` | `/v1/bookings` | List current user's bookings |
| `PATCH` | `/v1/bookings/{id}/cancel` | Cancel a scheduled booking |
| `PATCH` | `/v1/bookings/{id}/extend` | Extend an active booking by one slot |
| `PATCH` | `/v1/bookings/{id}/terminate` | End an active session early |
| `PATCH` | `/v1/bookings/{id}/reset` | Reset a stuck booking and clean resources |

## Create Request

```json
{
  "notebookName": "demo-cpu-01",
  "category": "cpu_basic",
  "slotDate": "2026-04-28",
  "slotKeys": ["cpu_basic_08:00"]
}
```

## Rules

- `notebookName` must be valid and unique for the user namespace.
- `category` must exist in the active slot configuration profile.
- `slotDate` must be `YYYY-MM-DD`.
- `slotKeys` must be valid for the selected category.
- Multiple slot keys must be contiguous and chronological.
- GPU categories can require the `compute` role and credit eligibility.

## Opening a Notebook

`GET /v1/bookings` includes `notebookUrl` only for active bookings whose notebook has been provisioned. Use that URL to open the session.
