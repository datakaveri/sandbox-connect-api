---
title: Slots and Calendar
---

# Slots and Calendar

Use slot APIs to show users which sessions can be booked before they submit a booking request.

## Available Slots

```http
GET /v1/slots/available?category=cpu_basic&date=2026-04-28
```

Returns slot-level availability for a category on a date.

## Monthly Calendar

```http
GET /v1/slots/calendar?category=cpu_basic&month=2026-04
```

Returns day-level availability totals for a category and month.

## Client Recommendation

Build booking UI from these APIs:

1. Fetch `GET /v1/categories`.
2. Let the user choose a category.
3. Fetch `GET /v1/slots/calendar`.
4. Let the user choose a date.
5. Fetch `GET /v1/slots/available`.
6. Submit selected slot keys with `POST /v1/bookings`.
