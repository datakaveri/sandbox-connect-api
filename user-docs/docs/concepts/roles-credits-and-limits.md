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

Use `GET /v1/categories` to show users the active rules instead of hard-coding them into a client.
