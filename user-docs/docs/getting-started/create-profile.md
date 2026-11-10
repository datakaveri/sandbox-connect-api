---
title: Create Your Profile
---

# Create Your Profile

A profile maps a user to a Kubeflow namespace. Some deployments create profiles automatically; others call the profile endpoint during onboarding.

```bash
curl -X POST "$SANDBOX_API_URL/v1/profile/create" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "userId": "00000000-0000-0000-0000-000000000000",
    "email": "user@example.com"
  }'
```

The user ID should match the authenticated subject expected by the deployment.
