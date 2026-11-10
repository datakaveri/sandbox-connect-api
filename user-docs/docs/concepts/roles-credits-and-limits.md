---
title: Roles, Credits, and Limits
---

# Roles, Credits, and Limits

Sandbox Connect enforces role, credit, and booking limits before accepting a booking.

## Roles

GPU categories can require a role such as `compute`. If the authenticated user does not have the required role, the API returns a forbidden response.

## Credits

Categories that require credits are checked before booking. Credit synchronization is handled by the profile credit sync cron job.

## Limits

Limits are controlled by deployment configuration and category configuration, including:

- maximum active bookings
- maximum bookings per week
- advance booking window
- minimum advance booking rule
- maximum concurrent users
- maximum contiguous slot selection

`MaxActiveBookings` controls how many live or reserved bookings a user can hold for the same category. It counts bookings with `scheduled`, `ready`, `active`, and `shutting_down` status. When this limit is reached, booking creation returns `Active booking limit exceeded`.

`MaxBookingsPerWeek` controls the weekly booking quota for the same user and category. It counts bookings with `scheduled`, `ready`, `active`, `shutting_down`, and `completed` status for the selected week. When this limit is reached, booking creation returns `Weekly booking limit exceeded`.

`cancelled` bookings do not count toward either limit. `expired` bookings do not count toward the weekly quota because `expired` can mean either no-show expiry after the booking became `ready`, or reset/cleanup of a stuck `ready` booking. Scheduled bookings reset before resources are ready become `cancelled`, not `expired`.

Users can create multiple future bookings as long as they stay within `MaxActiveBookings`, `MaxBookingsPerWeek`, slot availability, and duplicate booking rules. The previous `You already have an upcoming booking` restriction has been removed.

Use `GET /v1/categories` to show users the active rules instead of hard-coding them into a client.
