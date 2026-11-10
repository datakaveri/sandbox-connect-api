---
title: Notebook Lifecycle
---

# Notebook Lifecycle

Notebook lifecycle is managed asynchronously.

## Lifecycle Overview

1. The client creates a booking with `POST /v1/bookings`.
2. The API validates category rules, slot rules, access, credits, and notebook name.
3. The booking is stored in PostgreSQL.
4. Worker and lifecycle jobs provision Kubernetes resources at the scheduled time.
5. The notebook transitions through provisioning events.
6. The user opens the notebook URL when status becomes `running`.
7. The booking lifecycle completes, expires, terminates, or cleans up resources when the session ends.

## Expired Bookings

A booking becomes `expired` only after it has reached `ready` and then cannot continue as an active session. In the current implementation this happens in two cases:

- No-show expiry: the notebook is provisioned and the booking is `ready`, but the user does not open/start it before the configured no-show grace period ends. The lifecycle job deletes the notebook/PVC and marks the booking `expired`.
- Ready-booking reset: `PATCH /v1/bookings/{id}/reset` is called for a stuck `ready` booking. The API cleans up resources and marks the booking `expired`.

A `scheduled` booking that is cancelled or reset before resources are ready becomes `cancelled`, not `expired`. A session that becomes active and ends normally becomes `completed`.

## Why Bookings Own Creation

Bookings prevent users from creating notebooks outside the scheduling model. This lets the platform enforce:

- slot capacity
- advance booking windows
- active booking limits
- GPU role checks
- credit gating
- no-show and shutdown grace periods
- automatic cleanup
