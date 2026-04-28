---
title: Notebook Stuck Opening
---

# Notebook Stuck Opening

A notebook can stay in `opening` while the worker or Kubernetes is still provisioning resources.

## User Checks

1. Confirm the booking exists with `GET /v1/bookings`.
2. Check notebook status with `GET /v1/notebook/status/{notebook_name}`.
3. Wait until the scheduled slot start time if the booking is still scheduled.

## Operator Checks

If the notebook remains stuck after the slot starts:

1. Inspect worker logs.
2. Check the notebook events array in PostgreSQL.
3. Confirm the PVC exists in the user's namespace.
4. Confirm the Kubeflow Notebook custom resource exists.
5. Check Kubernetes scheduling events for resource pressure or image pull errors.

If the database shows `notebook-applied` but Kubernetes does not contain the notebook, the state can become `orphaned` and should be investigated by an operator.
