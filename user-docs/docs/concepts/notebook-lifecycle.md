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
7. The booking lifecycle expires, terminates, or cleans up resources when the session ends.

## Why Bookings Own Creation

Bookings prevent users from creating notebooks outside the scheduling model. This lets the platform enforce:

- slot capacity
- advance booking windows
- active booking limits
- GPU role checks
- credit gating
- no-show and shutdown grace periods
- automatic cleanup
