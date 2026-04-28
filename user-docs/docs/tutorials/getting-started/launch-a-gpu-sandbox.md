---
title: Launch a GPU Sandbox
---

# Launch a GPU Sandbox

GPU sandboxes follow the same booking flow as CPU sandboxes, but the selected category can require the `compute` role and credit eligibility.

## 1. List GPU Categories

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Choose a GPU category such as `basic` or `advance`.

## 2. Check Slots

```bash
curl "$SANDBOX_API_URL/v1/slots/available?category=basic&date=2026-04-28" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

## 3. Create a GPU Booking

```bash
curl -X POST "$SANDBOX_API_URL/v1/bookings" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "notebookName": "gpu-tutorial-01",
    "category": "basic",
    "slotDate": "2026-04-28",
    "slotKeys": ["basic_08:00"]
  }'
```

If the request is rejected with a role or credit error, confirm that the user has the required compute access and available credits.
