---
title: Open Your Notebook
---

# Open Your Notebook

After creating a booking, list your bookings until the active booking includes `notebookUrl`.

```bash
curl "$SANDBOX_API_URL/v1/bookings?status=active" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

You can also check a specific notebook status:

```bash
curl "$SANDBOX_API_URL/v1/notebook/status/demo-cpu-01" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Open the returned `notebookUrl` in your browser to access Jupyter.
