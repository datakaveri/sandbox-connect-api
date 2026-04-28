---
title: Open Your Notebook
---

# Open Your Notebook

After creating a booking, poll notebook status until the notebook is running.

```bash
curl "$SANDBOX_API_URL/v1/notebook/status/demo-cpu-01" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

You can also list notebooks:

```bash
curl "$SANDBOX_API_URL/v1/notebook/list" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

When the notebook is running, the list response can include `notebookUrl`. Open that URL in your browser to access Jupyter.
