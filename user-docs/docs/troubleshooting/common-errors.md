---
title: Common Errors
---

# Common Errors

## `Invalid category`

The category is not present in the active slot configuration profile. Fetch `GET /v1/categories` and use one of the returned names.

## `Selected slots must be contiguous and in chronological order`

The selected slot keys are not adjacent in the category's schedule. Select one slot or a contiguous sequence.

## `Required role 'compute' is missing for this category`

The user is booking a GPU category without the required role. Grant the role or choose a CPU category.

## `Cannot book a slot in the past`

The selected slot date is earlier than the current date in the booking timezone.

## `slotDate exceeds advance booking window`

The selected date is beyond the category's configured advance booking limit.
