---
title: Create a Booking
---

# Create a Booking

Create a booking to request a CPU or GPU sandbox session.

## 1. List Categories

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Choose a category name from the response, such as `cpu_basic`, `basic`, or `advance`.

If a category has `isBookable: false`, open its `launchUrl` instead of creating a booking. For example, `jupyter_lite` is a free browser-only category.

## 2. Check Slot Availability

```bash
curl "$SANDBOX_API_URL/v1/slots/available?category=cpu_basic&date=2026-04-28" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Pick one or more available slot keys. Multiple slots must be contiguous.

## 3. Create the Booking

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

The API returns booking details including the booking ID, slot start, slot end, and resource type.
