---
sidebar_position: 1
title: Quick Start Guide
---

# Quick Start Guide

Sandbox Connect lets users reserve CPU or GPU notebook sessions and open them as managed Jupyter sandboxes. It also exposes Jupyter Lite as a free browser-only option. In this branch, CPU/GPU users do not create notebooks directly. They create a booking, and the booking lifecycle provisions, starts, expires, and cleans up the notebook resources.

## Get Started in Less Than 5 Minutes

1. Get an access token from your identity provider.
2. Create your Kubeflow profile if it does not already exist.
3. List available sandbox categories.
4. For bookable CPU/GPU categories, check slot availability for the category and date you want.
5. Create a booking with a notebook name, category, date, and slot key.
6. Poll notebook status until the notebook is running.
7. Open the returned notebook URL.

For `jupyter_lite`, skip the booking flow and open the category `launchUrl` directly.

## Creation Flow

Use bookings as the notebook creation path:

```http
POST /v1/bookings
```

Direct notebook creation with `POST /v1/notebook/create` is not available in this branch. That endpoint existed in `stable/v2.2`, where users could create notebooks immediately. In the current booking-based flow, bookings own scheduling and notebook lifecycle management.

## Example Booking Request

```bash
curl -X POST "$SANDBOX_API_URL/v1/bookings" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "notebookName": "demo-cpu-01",
    "category": "cpu_basic",
    "slotDate": "2026-04-28",
    "slotKeys": ["cpu_basic_08:00"]
  }'
```

## What to Read Next

- [Bookings and Slots](./concepts/bookings-and-slots.md)
- [Create a Booking](./getting-started/create-a-booking.md)
- [Notebook Lifecycle](./concepts/notebook-lifecycle.md)
- [API Reference](./api-guides/api-reference.md)
