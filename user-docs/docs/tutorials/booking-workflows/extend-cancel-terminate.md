---
title: Extend, Cancel, and Terminate
---

# Extend, Cancel, and Terminate

Booking actions depend on the current booking status.

## List Bookings

```bash
curl "$SANDBOX_API_URL/v1/bookings" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Use the returned `id` for follow-up actions.

## Cancel a Scheduled Booking

```bash
curl -X PATCH "$SANDBOX_API_URL/v1/bookings/123/cancel" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Use cancel only for scheduled bookings.

## Extend an Active Booking

```bash
curl -X PATCH "$SANDBOX_API_URL/v1/bookings/123/extend" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

The next contiguous slot must be available. Extending 20:00–00:00 continues into
00:00–04:00 on the next day (IST), while keeping the original booking date.
An active booking can be extended once, within the category’s contiguous-slot limit.

## Terminate an Active Session

```bash
curl -X PATCH "$SANDBOX_API_URL/v1/bookings/123/terminate" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Termination ends the session early and performs best-effort cleanup of linked notebook resources.
