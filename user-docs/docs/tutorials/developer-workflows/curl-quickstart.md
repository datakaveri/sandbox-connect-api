---
title: curl Quickstart
---

# curl Quickstart

This is the shortest command sequence for testing the booking flow from a terminal.

```bash
export SANDBOX_API_URL="https://sandbox.example.com"
export ACCESS_TOKEN="<your-access-token>"
```

```bash
curl "$SANDBOX_API_URL/v1/health"
```

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

```bash
curl "$SANDBOX_API_URL/v1/slots/available?category=cpu_basic&date=2026-04-28" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

```bash
curl -X POST "$SANDBOX_API_URL/v1/bookings" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "notebookName": "curl-demo-01",
    "category": "cpu_basic",
    "slotDate": "2026-04-28",
    "slotKeys": ["cpu_basic_08:00"]
  }'
```

```bash
curl "$SANDBOX_API_URL/v1/notebook/status/curl-demo-01" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```
