---
title: Launch a CPU Sandbox
---

# Launch a CPU Sandbox

This tutorial creates a CPU notebook through the booking lifecycle.

## 1. Configure Your Shell

```bash
export SANDBOX_API_URL="https://sandbox.example.com"
export ACCESS_TOKEN="<your-access-token>"
```

## 2. Find the CPU Category

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Look for a CPU category such as `cpu_basic`.

## 3. Check Availability

```bash
curl "$SANDBOX_API_URL/v1/slots/available?category=cpu_basic&date=2026-04-28" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Choose an available slot key from the response.

## 4. Book the Sandbox

```bash
curl -X POST "$SANDBOX_API_URL/v1/bookings" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "notebookName": "cpu-tutorial-01",
    "category": "cpu_basic",
    "slotDate": "2026-04-28",
    "slotKeys": ["cpu_basic_08:00"]
  }'
```

## 5. Open the Notebook

```bash
curl "$SANDBOX_API_URL/v1/notebook/list" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

When the notebook is running, open the `notebookUrl` returned in the list response.
