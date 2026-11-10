# Postman collections

The Postman assets are split by the API's `API_BOOKINGS_ENABLED` operating mode. Each directory contains its collection, isolated example environment, and workflow documentation:

- [Bookings-enabled notebook APIs](bookings-enabled-notebook-apis/README.md) target `API_BOOKINGS_ENABLED=true`, which is the API default.
- [Direct notebook APIs](direct-notebook-apis/README.md) target `API_BOOKINGS_ENABLED=false`.
- [Auditing subset](forAuditing/README.md) contains only authentication setup, category/slot discovery, booking creation/listing, and extend/cancel/terminate requests.

Import the collection and environment from the same directory. Do not combine a collection with the environment from the other operating mode.

Both collections include **Authentication / Get Access Token - Password Grant**. Configure `auth_username` and `auth_password` locally; a successful request stores the returned bearer token in `access_token` automatically.

The collections are maintained manually. Update the affected collection and its paired environment when an API route, payload, or variable changes. Shared API changes must be applied to both modes.

Never commit usernames, passwords, access tokens, refresh tokens, or other real credentials. Use local Postman values or an approved secret store. If a credential is exposed in chat, logs, or source control, rotate it.
