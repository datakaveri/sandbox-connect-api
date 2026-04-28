---
title: Authentication
---

# Authentication

Most API routes require a bearer token.

```http
Authorization: Bearer <access_token>
```

The API validates Keycloak JWT claims and uses the authenticated subject as the user namespace. Role claims are used for access checks such as GPU compute access.

## Environment Variables for Examples

```bash
export SANDBOX_API_URL="https://sandbox.example.com"
export ACCESS_TOKEN="<your-access-token>"
```
