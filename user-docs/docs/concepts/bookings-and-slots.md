---
title: Bookings and Slots
---

# Bookings and Slots

Bookings are the creation mechanism for sandboxes in this branch. A booking combines a notebook name, a category, a date, and one or more contiguous slot keys.

## Category

A category describes the sandbox shape and booking rules. The current production profile includes examples such as:

| Category | Resource | Description | Access |
| --- | --- | --- | --- |
| `cpu_basic` | CPU | Standard CPU notebook | No compute role required |
| `basic` | GPU | 16 GB NVIDIA T4 GPU | Requires `compute` role and credits |
| `advance` | GPU | 40 GB NVIDIA A100 GPU | Requires `compute` role and credits |

Use `GET /v1/categories` to discover the active categories for the deployment.

## Slot Keys

Each category has slot templates. A booking request must include at least one slot key:

```json
{
  "slotKeys": ["cpu_basic_08:00"]
}
```

When multiple slot keys are provided, they must be contiguous and in chronological order. The category also controls the maximum number of contiguous slots a user can select.

## Booking Statuses

Common booking statuses include:

| Status | Meaning |
| --- | --- |
| `scheduled` | The booking is accepted and waiting for its slot |
| `ready` | The notebook is provisioned and ready to open |
| `active` | The booked session is currently active |
| `shutting_down` | The lifecycle job is ending the session |
| `completed` | The booking finished normally |
| `cancelled` | The booking was cancelled before it became active |
| `expired` | The booking expired or was reset |
