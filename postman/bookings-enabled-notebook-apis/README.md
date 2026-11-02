# Bookings-enabled notebook APIs

Use this directory when the API runs with `API_BOOKINGS_ENABLED=true`, which is the default.

## Import into Postman

Import both files:

- `sandbox-connect-api.bookings-enabled-notebook-apis.postman_collection.json`
- `sandbox-connect-api.bookings-enabled-notebook-apis.example.postman_environment.json`

Select **Sandbox Connect API - Bookings Enabled - Example** as the active environment. Set
`base_url` to the API base URL, including any ingress prefix, without a trailing slash:

- Local API: `http://localhost:3000` (the example default).
- Live dev API: `https://v2.dev.sandbox.iudx.io/api`.

Requests already append `/v1/...`; do not include `/v1` in `base_url`.

## Obtain an access token

The environment contains these authentication variables:

- `auth_token_url` defaults to `https://idp.example.com/auth/realms/replace-with-realm/protocol/openid-connect/token`.
- `auth_client_id` must be set to your configured client ID (the example uses `<frontend-client-id>`).
- `auth_username` and `auth_password` must be populated locally.
- `access_token` is populated automatically after successful authentication.

Run **Authentication / Get Access Token - Password Grant**. It sends an `application/x-www-form-urlencoded` request equivalent to:

```bash
curl --location "${AUTH_TOKEN_URL}" \
  --header "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=password" \
  --data-urlencode "username=${AUTH_USERNAME}" \
  --data-urlencode "client_id=${AUTH_CLIENT_ID}" \
  --data-urlencode "password=${AUTH_PASSWORD}"
```

The Postman test script checks for a `200` response and an `access_token`, then saves it into the active environment. Run this request again when authenticated API calls start returning `401` because the token expired.

Do not save real credentials into the committed example environment. The password grant should only be used where the identity-provider configuration and security policy explicitly allow it.

## Typical booking workflow

1. Run **Public / Health Check**. A healthy API returns `200` with its status and version.
2. Run **Authentication / Get Access Token - Password Grant** to populate `access_token`.
3. Run **Profile / Create Kubeflow Profile**. It returns `201` when the user's profile and workspace are ready or already exist. Notebook creation can initialize a missing profile, but this explicit request makes setup failures easier to diagnose.
4. Run **Booking Discovery / List Booking Categories**. Choose an entry where `isBookable` is `true`, then set `category` to its `name`. The environment starts with `cpu_basic`.
5. Select a usable date and slot:
   - Leave `slot_date` and `month` empty to use tomorrow and its month automatically, or set them to `YYYY-MM-DD` and `YYYY-MM`.
   - Run **List Monthly Calendar Availability** to find a date with capacity.
   - Run **List Available Slots** for that date.
   - Copy an available slot's `key` into `slot_key`. The key must belong to the selected category.
6. Set a unique `notebook_name`, then run **Bookings / Create Booking**. Its standard body is:

   ```json
   {
     "notebookName": "{{notebook_name}}",
     "category": "{{category}}",
     "slotDate": "{{slot_date}}",
     "slotKeys": ["{{slot_key}}"],
     "fileUrl": "{{file_url}}",
     "gitUrl": "{{git_url}}",
     "gitAccessToken": "{{git_access_token}}",
     "gitTokenSecretName": "{{git_token_secret_name}}"
   }
   ```

   Runtime asset fields are optional. For a private Git repository, use either `git_access_token` or `git_token_secret_name`, never both.
7. A successful creation returns `201`. Copy `bookingId` from the response into `booking_id`.
8. Use **Bookings / List Bookings** and **Notebook Discovery / Get Notebook Status** to observe progress. A normal booking moves through `scheduled`, `ready`, `active`, `shutting_down`, and `completed`. The notebook may report `opening` before it becomes `running`.
9. Once the booked notebook exists, run **Booking Notebook Token Sessions / Create Notebook Token Session** before using platform credentials inside it. A `503` with `Retry-After` means token projection is still in progress; retry shortly.
10. Run only the lifecycle action appropriate for the current booking state:
    - **Cancel Scheduled Booking** cancels a future `scheduled` booking.
    - **Extend Active Booking** adds the next contiguous slot when capacity and category limits allow it.
    - **Reset Stuck Booking** cleans up a stuck `scheduled` or `ready` booking.
    - **Terminate Booking Session** ends a `ready`, `active`, or `shutting_down` booking early.

Do not run the entire **Bookings** folder sequentially. Cancel, extend, reset, and terminate are alternative state-dependent operations and cannot all succeed against the same `booking_id`.

To filter notebooks by date, set `date_filter` to a JSON array such as `["2026-08-01","2026-08-31"]` and enable the disabled `filter` query parameter on **List Notebooks**.

## Token-session and JupyterLite notes

- Normal authenticated requests and token-session `POST` requests use `access_token`.
- Token-session `PUT` requests are intended for the notebook sidecar. They require `notebook_access_token` and `rotated_refresh_token` and are not normally sent manually.
- **JupyterLite / Create JupyterLite Session** sets an HttpOnly cookie and returns a launch URL. Postman can validate the response, but a real browser launch must call the endpoint from the browser context so the browser receives the cookie.

## COS admin CPU/GPU dashboard

The **COS Admin Dashboard** folder contains four read-only requests. They use the same
`access_token` variable as the rest of the collection. Authenticate as a user with realm role
`cos_admin`, or paste a fresh token into that variable locally. The token's `azp` must match the
configured frontend client (currently `angular-client` in dev). The API checks its signature,
issuer and expiry; it does not require the frontend client in `aud`.

| Request | Endpoint | View |
|---|---|---|
| Get Dashboard Summary | `GET /v1/platform/sandbox-dashboard/summary` | Current sandbox counts/resource requests and recorded booking session hours |
| Get Dashboard Trends | `GET /v1/platform/sandbox-dashboard/trends` | Daily CPU/GPU recorded session hours in IST |
| List Dashboard Users | `GET /v1/platform/sandbox-dashboard/users` | Paginated user usage with search and sorting |
| List Dashboard Sandboxes | `GET /v1/platform/sandbox-dashboard/sandboxes` | Current sandbox inventory, optionally filtered by owner and state |

All four requests include an enabled `resourceType` parameter. Set `dashboard_resource_type`
to `all` (default), `cpu` or `gpu`.

| Environment variable | Default | Use |
|---|---|---|
| `dashboard_from`, `dashboard_to` | empty | Optional history window for summary, trends and users. Enable both query parameters together and supply RFC3339 timestamps with an offset, such as `2026-10-07T00:00:00+05:30`. The window must be positive, no longer than 30 days, and end no later than now. |
| `dashboard_page`, `dashboard_size` | `1`, `20` | Users/sandboxes pagination; page starts at 1 and size is 1–100. |
| `dashboard_user_query` | empty | Enable `q` on users to search email or user ID; maximum 100 characters. |
| `dashboard_user_sort` | `bookedSessionHours:desc` | Users sort: `bookedSessionHours:desc`, `runningSandboxes:desc` or `email:asc`. |
| `dashboard_user_id` | empty | Enable `userId` on sandboxes and set an owner UUID from the users response. |
| `dashboard_state` | empty | Enable `state` on sandboxes: `opening`, `running`, `stopped`, `failed`, `orphaned` or `unknown`. |

Optional blank filters are disabled in the collection because sending an empty query value
returns `400`. With `from`/`to` disabled, history defaults to the last 7 IST calendar days.
Sandboxes always describe the current inventory and do not accept date parameters.

Current resource figures are **requests from running Notebook containers**. Historical hours
are **recorded booking session hours**; direct notebooks and browser sessions are excluded.
Neither value measures actual CPU/GPU utilization. Inspect `meta.partial`, `meta.sources`,
`meta.warnings` and nullable fields when a source is unavailable. An `orphaned` sandbox has a
retained database record but a confirmed missing Kubernetes Notebook object.

The requests include checks for `200`, metadata, private/no-store response headers and endpoint
response shapes. Saved `200` examples contain synthetic empty data. Authentication failures
return `401`; a valid token without `cos_admin` returns `403`.

The collection also includes the other API routes active in booking mode: health/docs,
category/slot discovery, booking lifecycle, notebook token sessions, notebook discovery,
JupyterLite sessions and profile creation. Direct-mode notebook mutation routes belong to the
separate [direct-mode collection](../direct-notebook-apis/README.md).

## Manual maintenance

When this mode's routes or request models change, update the collection JSON and its example environment together, import both files into Postman, and verify the affected requests. Apply shared route changes to the direct-mode collection as well.

Keep committed example values free of real credentials.
