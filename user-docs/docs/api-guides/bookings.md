---
title: Bookings
---

# Bookings

Bookings are the main user workflow for creating sandbox notebooks.

Booking APIs are available when `API_BOOKINGS_ENABLED=true`. When `API_BOOKINGS_ENABLED=false`, booking and scheduling routes return a structured `404` response; notebook token-session routes remain available for existing booking-owned notebooks, and direct notebooks should use the direct notebook token-session endpoints.

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
  "slotKeys": ["cpu_basic_08:00"],
  "fileUrl": "https://example.com/data/input.csv",
  "gitUrl": "https://github.com/datakaveri/example-notebooks.git",
  "gitAccessToken": "github_pat_..."
}
```

`fileUrl`, `gitUrl`, and `gitAccessToken` are optional. When provided, the worker downloads the file and/or clones the GitHub repository into the notebook PVC before the Kubeflow Notebook starts. `gitAccessToken` is only needed for private GitHub repositories.

When `gitAccessToken` is supplied, the API creates or updates an internal Kubernetes Secret in the user's namespace and stores only that Secret name with the booking metadata. Operators can alternatively pass `gitTokenSecretName` to reference a pre-created Secret containing key `token`.

## Rules

- `notebookName` must be valid and unique for the user namespace.
- `category` must exist in the active slot configuration profile.
- `slotDate` must be `YYYY-MM-DD`.
- `slotKeys` must be valid for the selected category.
- Multiple slot keys must be contiguous and chronological.
- GPU categories can require the `compute` role and credit eligibility.
- `fileUrl` and `gitUrl` must be HTTPS URLs without embedded credentials.
- `gitUrl` must be a GitHub URL in v1.
- `gitAccessToken` and `gitTokenSecretName` require `gitUrl`; use only one of them.
- `gitTokenSecretName`, when used directly, must reference an existing Secret in the user namespace.

## Opening a Notebook

`GET /v1/bookings` includes `notebookUrl` only for active bookings whose notebook has been provisioned. Use that URL to open the session.

## Status-Based Actions

| Current status | User action |
| --- | --- |
| `scheduled` | Cancel if you no longer need the slot. |
| `ready` | Wait for the notebook to become active, or terminate/reset if it is stuck. |
| `active` | Open the notebook URL, extend to the next slot if available, or terminate early. |
| `shutting_down` | Save work. You can still terminate early, but extension is no longer available. |
| `completed`, `cancelled`, `expired` | No further user action is available; these are history states. |
