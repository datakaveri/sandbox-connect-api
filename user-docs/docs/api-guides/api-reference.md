---
title: API Reference
---

# API Reference

The generated OpenAPI files are published with this docs site:

- [Swagger YAML](/openapi/swagger.yaml)
- [Swagger JSON](/openapi/swagger.json)

The API service also serves ReDoc directly at:

```http
GET /v1/apis/
```

The OpenAPI artifacts are generated from Go Swag annotations and synced into this docs site during `npm run start` and `npm run build`.
