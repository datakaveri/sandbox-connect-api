---
title: Bookings and Slots
---

# Bookings and Slots

Bookings are the creation mechanism for sandboxes in this branch. A booking combines a notebook name, a category, a date, and one or more contiguous slot keys.

## Category

A category describes the sandbox shape and booking rules. The current production profile includes examples such as:

| Category | Resource | Description | Access |
| --- | --- | --- | --- |
| `jupyter_lite` | Browser | Free JupyterLite environment running in the browser with WebAssembly | No booking or compute role required |
| `cpu_basic` | CPU | Standard CPU notebook | No compute role required |
| `basic` | GPU | 16 GB NVIDIA T4 GPU | Requires `compute` role and credits |
| `advance` | GPU | 40 GB NVIDIA A100 GPU | Requires `compute` role and credits |

Use `GET /v1/categories` to discover the active categories for the deployment.

Categories with `isBookable: false`, such as `jupyter_lite`, do not use slot or booking APIs. Open their `launchUrl` directly.

## Slot Keys

Each bookable CPU/GPU category has slot templates. A booking request must include at least one slot key:

```json
{
  "slotKeys": ["cpu_basic_08:00"]
}
```

When multiple slot keys are provided, they must be contiguous and in chronological order. The category also controls the maximum number of contiguous slots a user can select.

## Booking Statuses

A booking normally moves through `scheduled`, `ready`, `active`, `shutting_down`, and `completed`. It can also end as `cancelled` or `expired`.

| Status | Meaning |
| --- | --- |
| `scheduled` | Your booking is accepted and waiting for its slot start time. You can cancel it before resources are prepared. |
| `ready` | The platform has linked notebook resources and is waiting for the notebook to become runnable. If it stays here too long, it may expire as a no-show. |
| `active` | The notebook session is running. This is the state where `GET /v1/bookings` can include `notebookUrl`. |
| `shutting_down` | The session is close to its end time. Save work and prepare to stop using the notebook. |
| `completed` | The session ended normally at the slot end time or was terminated early. It remains visible in booking history. |
| `cancelled` | The booking ended before notebook resources were ready, usually because you cancelled it or reset a scheduled booking. |
| `expired` | A ready booking did not become active within the no-show grace period, or a ready booking was reset during cleanup. Scheduled bookings that are reset become `cancelled`, not `expired`. |

`scheduled`, `ready`, `active`, and `shutting_down` count as current bookings while they are in progress. `cancelled` and `expired` do not consume current booking capacity.
