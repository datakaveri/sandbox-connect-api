# Direct notebook APIs

Use this directory when the API runs with `API_BOOKINGS_ENABLED=false`.

## Import into Postman

Import both files:

- `sandbox-connect-api.direct-notebook-apis.postman_collection.json`
- `sandbox-connect-api.direct-notebook-apis.example.postman_environment.json`

Select **Sandbox Connect API - Direct Notebook APIs - Example** as the active environment. Set `base_url` to the Sandbox Connect API origin without a trailing slash.

## Obtain an access token

The environment contains these authentication variables:

- `auth_token_url` defaults to `https://v2.dev.iudx.io/auth/realms/iudx-v2/protocol/openid-connect/token`.
- `auth_client_id` defaults to `angular-client`.
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

## Typical direct notebook workflow

1. Confirm the API is running with `API_BOOKINGS_ENABLED=false`.
2. Run **Public / Health Check**.
3. Run **Authentication / Get Access Token - Password Grant** to populate `access_token`.
4. Run **Profile / Create Kubeflow Profile** to initialize the user's profile and workspace explicitly.
5. Configure the notebook:
   - Set a unique `notebook_name`.
   - For CPU, set `notebook_type` to `cpu`; `instance_type` is not used.
   - For GPU, set `notebook_type` to `gpu` and set `instance_type` to a value returned by **Notebook Discovery / List Notebook Instance Types**.
   - Leave `image_name` blank to use the configured default image, or set an allowed custom image.
   - Optionally set `file_url` or `git_url`. For private Git access, populate either `git_access_token` or `git_token_secret_name`.
6. Run **Direct Notebook Lifecycle / Create Direct Notebook**. A successful request returns `201` while creation continues asynchronously.
7. Poll **Notebook Discovery / Get Notebook Status** until the notebook becomes `running`. **Check Notebook Exists** confirms only that the name is present; it does not indicate readiness.
8. Run **Direct Notebook Token Sessions / Create Direct Notebook Token Session** before using platform-authenticated operations inside the notebook. Retry after the indicated delay if the endpoint returns `503`.
9. Use the lifecycle operations as needed:
   - **Stop Direct Notebook** stops a running notebook without deleting its persistent data.
   - **Start Direct Notebook** restarts a stopped notebook.
   - **Delete Direct Notebook** deletes the notebook and its PVC, so use it only when the workspace is no longer needed.

The **Booking Token Session Compatibility** folder exists only for notebooks associated with older or existing booking records that must refresh delegated tokens while the API is in direct mode. It is not part of the normal direct-notebook workflow.

To filter notebooks by date, set `date_filter` to a JSON array such as `["2026-08-01","2026-08-31"]` and enable the disabled `filter` query parameter on **List Notebooks**.

## Token-session and JupyterLite notes

- Normal authenticated requests and token-session `POST` requests use `access_token`.
- Token-session `PUT` requests are intended for the notebook sidecar. They require `notebook_access_token` and `rotated_refresh_token` and are not normally sent manually.
- **JupyterLite / Create JupyterLite Session** sets an HttpOnly cookie and returns a launch URL. Postman can validate the response, but a real browser launch must call the endpoint from the browser context so the browser receives the cookie.

## Manual maintenance

When this mode's routes or request models change, update the collection JSON and its example environment together, import both files into Postman, and verify the affected requests. Apply shared route changes to the bookings-enabled collection as well.

Keep committed example values free of real credentials.
