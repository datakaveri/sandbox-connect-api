---
title: API Reference
---

# API Reference

Use this page as the entry point for API reference access.

The generated OpenAPI files are published with this docs site:

- [Swagger YAML](pathname:///openapi/swagger.yaml)
- [Swagger JSON](pathname:///openapi/swagger.json)

The API service also serves ReDoc directly at:

```http
GET /v1/apis/
```

If you want a browsable reference UI instead of a file download, open the ReDoc endpoint served by the API deployment.

The OpenAPI artifacts are generated from Go Swag annotations and synced into this docs site during `npm run start` and `npm run build`.
