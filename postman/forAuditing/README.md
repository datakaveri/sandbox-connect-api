# Auditing Postman subset

This is a minimal bookings-enabled collection for auditing only these requests:

0. Get Access Token — setup only; it has no test assertions
1. List Category
2. List Slots
3. Create Booking
4. List Bookings
5. Extend Booking
6. Cancel Booking
7. Terminate Booking

The target API must run with `API_BOOKINGS_ENABLED=true`.

## Import into Postman

Import both files from this directory:

- `sandbox-connect-api.for-auditing.postman_collection.json`
- `sandbox-connect-api.for-auditing.example.postman_environment.json`

Select **Sandbox Connect API - Auditing Subset - Example** as the active environment.

## Environment setup

1. Set `base_url` to the Sandbox Connect API origin without a trailing slash.
2. Set `auth_username` and `auth_password` locally. The environment already contains the development token URL and `angular-client` client ID.
3. Leave `slot_date` empty to use tomorrow automatically, or enter a date in `YYYY-MM-DD` format.
4. Do not save real credentials or tokens into the committed example environment.

## Typical audit workflow

### Authentication setup

Run **0) Get Access Token (Not to be tested)**. It intentionally has no `pm.test` assertions, so it is setup rather than an audited API. When the identity provider returns `200`, its post-response script stores the returned token in `access_token` for the remaining requests.

### Category, slot, and booking checks

1. Run **1) List Category** and confirm it returns `200`. Select a category whose `isBookable` value is `true`, then copy its `name` into `category`.
2. Run **2) List Slots** and confirm it returns `200`. Choose a slot whose `availableSlots` is greater than zero, then copy its `key` into `slot_key`.
3. Set a unique `notebook_name` and run **3) Create Booking**. A successful request returns `201`; its test script automatically stores the response's `bookingId` in `booking_id`.
4. Run **4) List Bookings** and confirm it returns `200`. Verify that the created booking appears with the expected notebook name, category, date, slot, and status.

### Lifecycle checks are separate branches

Requests 5–7 require different booking states and should not be run sequentially against one booking:

- **5) Extend Booking** requires an `active` booking with a valid next contiguous slot. Set `booking_id` to a suitable active booking before running it.
- **6) Cancel Booking** requires a future `scheduled` booking. A newly created future booking can normally be used for this check.
- **7) Terminate Booking** requires a `ready`, `active`, or `shutting_down` booking. Use a separate suitable booking because a cancelled booking cannot be terminated.

For a complete lifecycle audit, prepare separate booking IDs for extend, cancel, and terminate, updating `booking_id` before each request. Each lifecycle request asserts a `200` response when its preconditions are satisfied.

Do not run the entire collection as a single linear Collection Runner workflow: the lifecycle operations are mutually state-dependent.

## Manual maintenance

This collection is intentionally limited to the eight listed requests. When any corresponding route, request body, response, or required variable changes, update this collection and its environment manually without adding unrelated APIs.
