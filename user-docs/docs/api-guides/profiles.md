---
title: Profiles
---

# Profiles

Profiles connect authenticated users to Kubeflow namespaces.

## Create Profile

```http
POST /v1/profile/create
```

```json
{
  "userId": "00000000-0000-0000-0000-000000000000",
  "email": "user@example.com"
}
```

Profile creation is usually part of onboarding. Once the profile exists, notebook resources are created inside the user's namespace.
