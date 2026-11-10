---
title: Statuses and Events
---

# Statuses and Events

Notebook status is derived from database events and Kubernetes state.

| Status | Meaning |
| --- | --- |
| `opening` | Provisioning is in progress or Kubernetes is not ready yet |
| `running` | The notebook exists and has ready replicas |
| `stopped` | The notebook has been stopped |
| `failed` | Provisioning failed |
| `orphaned` | Database and Kubernetes state do not agree |

## Common Events

| Event | Meaning |
| --- | --- |
| `picked` | A worker picked the notebook request |
| `pvc-applied` | Persistent volume claim was applied |
| `notebook-applied` | Kubeflow Notebook custom resource was applied |
| `pvc-apply-failed` | PVC apply failed |
| `notebook-apply-failed` | Notebook apply failed |

Use:

```http
GET /v1/notebook/status/{notebook_name}
```

to check the current state of a notebook.
