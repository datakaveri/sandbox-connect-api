---
title: Launch Jupyter Lite
---

# Launch Jupyter Lite

Jupyter Lite is the free browser-only sandbox mode. It runs in the user's browser with WebAssembly and stores notebooks/settings in browser-local storage.

## 1. List Categories

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Find the `jupyter_lite` category. It has `isBookable: false`, `resourceType: "browser"`, and a `launchUrl`.

## 2. Open the Launch URL

Open the returned `launchUrl`, usually:

```text
/jupyterlite/lab/index.html
```

Do not call slot, calendar, or booking endpoints for this category.

## Persistence

Jupyter Lite files are stored by the browser. They are not synced to Sandbox Connect storage, Kubernetes PVCs, or user profiles.
