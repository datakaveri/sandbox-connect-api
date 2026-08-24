# API server — configuration field reference

## 0. Document header

| | |
|---|---|
| **Service** | `sandbox-api` (`cmd/api`) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, `stable/v2.3` |
| **Config source** | `infra/api/configmap.yaml` (`api-config`), `infra/api/secret.yaml` (`api-creds`), plus `database-creds` |
| **Config schema** | `cmd/api/types.go` — `ApiEnv`, `NotebookConfig`, `RegistrySecretConfig`, `RabbitMQConfig` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-07-29 |

Read [README.md](README.md) first — it documents the loading order and the baseline
startup-failure mode that every `,required` field shares.

## 1. Top-level structure

| Struct | Env prefix | What it configures | Where consumed |
|---|---|---|---|
| `ApiEnv` | `API_` | HTTP listener, auth, timeouts, rate limiting | `cmd/api/main.go`, `middleware.go`, `router.go` |
| `ApiEnv.NotebookConfig` | `API_DEFAULT_*`, `API_MAX_*`, `API_KUBEFLOW_URL`, `SLOT_CONFIG_PROFILE` | Default notebook resources, per-user quotas, notebook URL construction | `cmd/api/handlers.go` |
| `ApiEnv.RegistrySecretConfig` | `API_REGISTRY_` | Which image-pull secret the API provisions into user namespaces | `cmd/api/registry*.go` |
| `ApiEnv.RabbitMQConfig` | `RABBITMQ_` | Audit event publishing; entirely optional | `cmd/api/audit*.go` |

Env wiring in `infra/api/manifest.yaml`: `envFrom` pulls **all** of `api-config` and `api-creds`;
`API_POSTGRES_URL` is the single exception, projected individually from Secret `database-creds`
key `POSTGRES_URL`.

---

## 2. Field blocks

### Server and process

### `API_ADDRESS`

- **Type / format:** string, `host:port`. Bind address, not a URL — no scheme, no path.
- **Required:** yes
- **Purpose:** the listen address passed to `http.Server.Addr` in `main.go`. Determines which
  interface accepts traffic.
- **Expected value:** `0.0.0.0:<port>` in a container so the kubelet and Service can reach it.
  `127.0.0.1:<port>` binds loopback only.
- **Example value:** `0.0.0.0:3000`
- **Default if omitted:** none — startup fails.
- **How to obtain:** operator decision. The port must equal `containerPort` in
  `infra/api/manifest.yaml` and the Service `targetPort`.
- **Failure mode:** binding `127.0.0.1` inside a pod makes readiness probes and Service traffic
  fail with connection-refused while the process itself logs a healthy start — the misleading case
  worth checking first. A port already in use gives `listen tcp … address already in use`.
- **Change impact:** must be changed together with the Service and probe definitions.
- **Notes / gotchas:** unrelated to `API_PLATFORM_TOKEN_READY_PORT`, which is a notebook-side port.

### `API_VERSION`

- **Type / format:** string, URL path segment without slashes.
- **Required:** yes
- **Purpose:** mounts every route under `/<version>` in `router.go` and is echoed in responses.
- **Expected value:** `v1` unless a breaking API revision is being served in parallel.
- **Example value:** `v1`
- **Default if omitted:** none — startup fails.
- **How to obtain:** operator decision, coordinated with the frontend and any ingress rewrite.
- **Failure mode:** every client request 404s while the process looks healthy; the ingress path and
  this value have silently diverged.
- **Change impact:** breaking for all clients; requires frontend and ingress changes in lockstep.
- **Notes / gotchas:** do not include a leading or trailing `/`.

### `API_LOG_LEVEL`

- **Type / format:** string enum, effectively `debug` | anything else.
- **Required:** no
- **Purpose:** read directly with `os.Getenv` at the top of `main()` — before `env.Parse` — so it
  also governs logging of config-parse failures. Only the exact string `debug` lowers the level;
  every other value leaves it at INFO.
- **Expected value:** `info` in production, `debug` when diagnosing.
- **Example value:** `info`
- **Default if omitted:** `info`.
- **How to obtain:** operator decision.
- **Failure mode:** none at startup. `debug` on a busy cluster is a cost and disclosure concern —
  it logs request-level detail.
- **Change impact:** none; restart required.
- **Notes / gotchas:** it is **not** part of the config struct, so a typo such as `DEBUG` silently
  yields INFO instead of an error. Deployed value in `api-config` is currently `debug`.

### `API_KUBE_CONFIG_MODE`

- **Type / format:** string enum — `cluster` | `local`.
- **Required:** no
- **Purpose:** selects in-cluster service-account credentials versus an external kubeconfig file
  when building the Kubernetes client.
- **Expected value:** `cluster` for anything running as a pod; `local` only for a developer laptop.
- **Example value:** `cluster`
- **Default if omitted:** `cluster`.
- **How to obtain:** operator decision, determined by where the binary runs.
- **Privileges required:** in `cluster` mode the pod's ServiceAccount needs the notebook, PVC and
  secret permissions granted in `infra/rbac.yaml`.
- **Failure mode:** `cluster` outside a pod fails with
  `unable to load in-cluster configuration, KUBERNETES_SERVICE_HOST … must be defined`. Correct
  mode but insufficient RBAC produces `notebooks.kubeflow.org is forbidden` at first use, not at
  startup.
- **Change impact:** none to data.
- **Notes / gotchas:** `local` ignores the ServiceAccount entirely and inherits whatever the
  kubeconfig's current context points at — capable of writing to the wrong cluster.

### `API_KUBE_CONFIG_PATH`

- **Type / format:** string, absolute filesystem path.
- **Required:** conditional on `API_KUBE_CONFIG_MODE=local`
- **Purpose:** path to the kubeconfig used in `local` mode.
- **Expected value:** absolute path to a kubeconfig readable by the process user.
- **Example value:** `/home/dev-user/.kube/config`
- **Default if omitted:** `""` — correct and expected in `cluster` mode.
- **How to obtain:** developer's own kubeconfig.
- **Failure mode:** `local` with an empty or wrong path fails at client construction with
  `stat : no such file or directory`.
- **Change impact:** none.
- **Notes / gotchas:** leave empty in every deployed environment. A stale value here is harmless
  while the mode is `cluster`, which is exactly why it survives unnoticed.

### JupyterLite static assets

### `API_JUPYTERLITE_BASE_URL`

- **Type / format:** string, URL path prefix beginning with `/`.
- **Required:** no
- **Purpose:** the route prefix under which the bundled JupyterLite distribution is served.
- **Expected value:** a path, not a URL; no host, no trailing slash.
- **Example value:** `/jupyterlite`
- **Default if omitted:** `/jupyterlite`.
- **How to obtain:** operator decision; must match the ingress path and the frontend's link.
- **Failure mode:** mismatch with the ingress produces 404s on JupyterLite assets only — the rest
  of the API is unaffected, so it is easy to misattribute to the frontend.
- **Change impact:** coordinate with `infra/api/ingress.yaml` and the frontend.
- **Notes / gotchas:** was absent from `.env.all.example` before this revision.

### `API_JUPYTERLITE_STATIC_DIR`

- **Type / format:** string, filesystem path (absolute in the image, relative when run from source).
- **Required:** no
- **Purpose:** directory on disk served at `API_JUPYTERLITE_BASE_URL`.
- **Expected value:** the path the JupyterLite bundle is copied to by `infra/api/Dockerfile`.
- **Example value:** `/app/jupyterlite`
- **Default if omitted:** `jupyterlite` — a *relative* path, which resolves against the working
  directory and therefore differs between the image and a local `go run`.
- **How to obtain:** read from `infra/api/Dockerfile`; the deployed value is `/app/jupyterlite`.
- **Failure mode:** wrong path serves 404s for every asset under the base URL; JupyterLite loads a
  blank page rather than erroring visibly.
- **Change impact:** must track the Dockerfile.
- **Notes / gotchas:** the default being relative is the reason the ConfigMap sets it explicitly —
  keep it set.

### Database

### `API_POSTGRES_URL`

- **Type / format:** string, Postgres connection URI.
- **Required:** yes
- **Purpose:** DSN for the `pgx` pool backing every handler.
- **Expected value:** `postgresql://<user>:<password>@<host>:<port>/<database>[?sslmode=…]`.
  Percent-encode reserved characters in the password.
- **Example value:** `postgresql://sandbox_api:REDACTED@postgres.sandbox.svc:5432/sandbox_db?sslmode=require`
- **Default if omitted:** none — startup fails.
- **How to obtain:** **not** from `api-config`. Projected from Secret `database-creds`, key
  `POSTGRES_URL`, which is managed outside this repository — ask the DB owner or DevOps.
- **Privileges required:** see the credential subsection in §3.
- **Failure mode:** unreachable host fails the pool ping at startup:
  `failed to connect to host=… dial tcp: connect: connection refused`. Wrong password gives
  `FATAL: password authentication failed for user`. An `sslmode` mismatch against a TLS-enforcing
  server gives `pq: SSL is not enabled on the server` — commonly misread as a network fault.
- **Change impact:** schema is shared with the worker, slot lifecycle, and profile credit sync; a host change is a
  coordinated cutover across all four.
- **Notes / gotchas:** an `@` or `/` inside an un-encoded password truncates the URI and produces a
  confusing "database does not exist" rather than an auth error.

### Authentication (Keycloak)

Client-level requirements are in §3. The fields:

### `API_KEYCLOAK_URL`

- **Type / format:** string, absolute URL with scheme. **Include `/auth` if and only if the
  Keycloak instance is pre-17 or deployed under that context path.**
- **Required:** yes
- **Purpose:** base URL for realm metadata and token endpoints.
- **Expected value:** scheme + host + optional context path, no trailing slash, no realm segment.
- **Example value:** `https://idp.tgdex.telangana.gov.in/auth`
- **Default if omitted:** none — startup fails.
- **How to obtain:** the Keycloak operator. Confirm the context path by fetching
  `<value>/realms/<realm>/.well-known/openid-configuration` — a 200 means the value is right.
- **Failure mode:** wrong context path 404s on every token validation, surfacing as blanket
  401/403 for authenticated routes rather than a startup error.
- **Change impact:** must move together with `PROFILE_CREDIT_SYNC_KEYCLOAK_URL` and the sidecar's
  `KEYCLOAK_TOKEN_URL` in both notebook templates.
- **Notes / gotchas:** the `/auth` prefix is the single most common cause of "auth suddenly broke
  after the IdP upgrade".

### `API_KEYCLOAK_REALM`

- **Type / format:** string, realm name; case-sensitive.
- **Required:** yes
- **Purpose:** realm whose tokens this API accepts.
- **Expected value:** exact realm name as shown in the Keycloak admin console.
- **Example value:** `tgdex`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Keycloak operator.
- **Failure mode:** wrong realm rejects every token with an issuer mismatch → 401 on all
  authenticated routes.
- **Change impact:** `API_KEYCLOAK_PUBLIC_KEY` must be replaced in the same change — realms do not
  share signing keys.
- **Notes / gotchas:** must equal `PROFILE_CREDIT_SYNC_KEYCLOAK_REALM`.

### `API_KEYCLOAK_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** yes
- **Purpose:** the public client whose tokens the API expects; used for audience and authorized-party
  checks on incoming user tokens.
- **Expected value:** the client the **frontend** authenticates with — not a service account.
- **Example value:** `angular-tgdex-client`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Keycloak operator; must match the frontend build's configured client.
- **Failure mode:** mismatch with the frontend rejects otherwise-valid tokens → users log in
  successfully and then get 401s from the API, which looks like a session bug.
- **Change impact:** frontend and API must change together.
- **Notes / gotchas:** distinct from `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`, which is
  confidential and server-side only.

### `API_KEYCLOAK_PUBLIC_KEY`

- **Type / format:** string — base64 DER of an RSA public key, **no** PEM header/footer and no
  newlines. The code wraps it in `-----BEGIN PUBLIC KEY-----` itself.
- **Required:** yes
- **Purpose:** verifies the RS256 signature of every incoming JWT. This is why the API needs no
  network round-trip to the IdP per request.
- **Expected value:** the realm's active RS256 signing key.
- **Example value:** `MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A…QAB` (392 chars for a 2048-bit key)
- **Default if omitted:** none — startup fails.
- **How to obtain:** Keycloak admin console → Realm settings → Keys → RS256 row → **Public key**.
  Equivalently, the `x5c[0]` / `n` of `<API_KEYCLOAK_URL>/realms/<realm>/protocol/openid-connect/certs`.
- **Failure mode:** a wrong or rotated key rejects every token with
  `crypto/rsa: verification error` → uniform 401s. Pasting the PEM headers in produces a parse
  error at first request instead.
- **Change impact:** **rotating the realm signing key invalidates this value and takes the API
  down until it is updated.** Treat realm key rotation as a coordinated change.
- **Notes / gotchas:** the API pins one static key rather than fetching JWKS, so it cannot follow
  automatic key rotation. If the IdP team rotates on a schedule, that schedule is an outage
  schedule unless this is updated in the same window.

### `API_BLOCKED_EMAIL_DOMAINS`

- **Type / format:** string containing a comma-separated list of exact email domains.
- **Required:** no
- **Purpose:** denies otherwise-authenticated users access to all sandbox API and JupyterLite routes
  when the domain in their JWT `email` claim matches a configured domain.
- **Expected value:** domain names without `@`, separated by commas when more than one is required.
- **Example value:** `cbr.synthetic.org`
- **Default if omitted:** empty; domain blocking is disabled.
- **Failure mode:** a matching user receives HTTP 403. Invalid JWTs and unverified email addresses
  continue to receive their existing HTTP 401 responses.
- **Change impact:** takes effect on the next request; no restart-time migration or data change.
- **Notes / gotchas:** matching is exact and case-insensitive. Subdomains and suffix lookalikes are
  not blocked unless listed explicitly.

### Platform token exchange (notebook delegation)

The API mints a delegated token so a user's notebook can call platform file APIs as that user. It
hands the refresh token to the notebook's `platform-token-sidecar` — see
[platform-token-sidecar.md](platform-token-sidecar.md).

### `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** no
- **Purpose:** the **confidential** client used to perform the OAuth token-exchange grant.
- **Expected value:** the confidential client described in the
  [canonical Keycloak setup](#canonical-keycloak-setup).
- **Example value:** `sandbox-notebook`
- **Default if omitted:** `sandbox-notebook`.
- **How to obtain:** import and configure the client as described in the
  [canonical Keycloak setup](#canonical-keycloak-setup).
- **Failure mode:** unknown client → `invalid_client` from the token endpoint; notebooks start but
  never become token-ready and are culled after `API_PLATFORM_TOKEN_READY_TIMEOUT_SECS`.
- **Change impact:** must change together with the sidecar's `KEYCLOAK_CLIENT_ID` and
  `EXPECTED_CLIENT_ID` in both notebook templates.
- **Notes / gotchas:** despite the shared default, this and `…_NOTEBOOK_CLIENT_ID` are separate
  knobs; see the next block.

### `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** no
- **Purpose:** the expected `azp` (authorized party) in delegated notebook access tokens.
- **Expected value:** identical to `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`; the API rejects
  configuration where the two values differ.
- **Example value:** `sandbox-notebook`
- **Default if omitted:** `sandbox-notebook`.
- **How to obtain:** Keycloak operator.
- **Failure mode:** a mismatch prevents token exchange before Keycloak is called; an exchanged
  token whose `azp` differs is rejected before it is written to the notebook.
- **Change impact:** change together with `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`,
  `KEYCLOAK_CLIENT_ID`, and `EXPECTED_CLIENT_ID` in both notebook templates.
- **Notes / gotchas:** this is a validation identity, not the optional downstream audience.

### `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_SECRET`

- **Type / format:** string, opaque secret.
- **Required:** conditional — required whenever notebook delegation is used.
- **Purpose:** authenticates the confidential exchange client.
- **Expected value:** the client secret from Keycloak. **Secret — Kubernetes Secret only, never a
  ConfigMap.**
- **Example value:** `<32-char opaque string>`
- **Default if omitted:** `""`, which disables delegation rather than failing startup.
- **How to obtain:** Keycloak admin console → Clients → `sandbox-notebook` → Credentials →
  Client secret. Stored in Secret `api-creds`.
- **Privileges required:** see the [canonical Keycloak setup](#canonical-keycloak-setup).
- **Failure mode:** empty or wrong → `unauthorized_client` / `invalid_client_credentials` from the
  token endpoint. Because the empty case does not fail startup, the symptom is notebooks that
  launch and then never reach ready — check the API log for the exchange error, not the notebook.
- **Change impact:** rotating in Keycloak requires updating the Secret and restarting the API.
- **Notes / gotchas:** commit `f548ed9` replaced a bundled copy of this secret with a placeholder;
  do not reintroduce a real value into `infra/`.

### `API_PLATFORM_TOKEN_EXCHANGE_TOKEN_URL`

- **Type / format:** string, absolute URL to the realm token endpoint.
- **Required:** no
- **Purpose:** explicit token endpoint for the exchange, when it should not be derived from
  `API_KEYCLOAK_URL` + realm.
- **Expected value:** `<keycloak>/realms/<realm>/protocol/openid-connect/token`
- **Example value:** `https://idp.tgdex.telangana.gov.in/auth/realms/tgdex/protocol/openid-connect/token`
- **Default if omitted:** `""` → derived from `API_KEYCLOAK_URL` and `API_KEYCLOAK_REALM`.
- **How to obtain:** the realm's `.well-known/openid-configuration` → `token_endpoint`.
- **Failure mode:** set to a different realm than `API_KEYCLOAK_REALM` and exchange fails with
  issuer/audience errors that read like client misconfiguration.
- **Change impact:** none if left empty.
- **Notes / gotchas:** prefer leaving it empty so it cannot drift from the realm settings. It is
  set explicitly in the live cluster.

### `API_PLATFORM_TOKEN_EXCHANGE_SCOPE`

- **Type / format:** string, space-separated OAuth scopes.
- **Required:** no
- **Purpose:** scopes requested for the delegated token.
- **Expected value:** the scopes the downstream file API requires; `openid` must be present for an
  OIDC token.
- **Example value:** `openid profile email`
- **Default if omitted:** `openid profile email`.
- **How to obtain:** downstream API owner + Keycloak client scope configuration.
- **Failure mode:** requesting a scope not assigned to the client yields `invalid_scope`; a missing
  required scope produces a token that downstream APIs reject as insufficiently privileged.
- **Change impact:** coordinate with the downstream API.
- **Notes / gotchas:** space-separated, not comma-separated.

### `API_PLATFORM_TOKEN_EXCHANGE_AUDIENCE`

- **Type / format:** string, audience identifier.
- **Required:** no
- **Purpose:** optional `audience` filter on the exchange request.
- **Expected value:** empty unless the downstream API requires one audience already available to
  the notebook client through an allowed client scope.
- **Example value:** `""`
- **Default if omitted:** `""` — deployed as empty.
- **How to obtain:** downstream API owner.
- **Failure mode:** an audience not already available to the client returns `invalid_target`.
- **Change impact:** coordinate with the downstream API's audience validation.
- **Notes / gotchas:** the parameter only filters audiences already present; it does not grant a
  new audience. Add downstream audiences through an explicitly allowed client scope first.

### `API_PLATFORM_TOKEN_READY_PORT`

- **Type / format:** int, TCP port.
- **Required:** no
- **Purpose:** the port the API probes **inside the notebook pod** to decide whether the sidecar
  has obtained a token and the notebook may be marked running.
- **Expected value:** 1024–65535; must equal the sidecar's `READY_ADDRESS` port and the
  `token-ready` `containerPort` in both notebook templates.
- **Example value:** `8081`
- **Default if omitted:** `8081`.
- **How to obtain:** operator decision; changing it means editing three places.
- **Failure mode:** mismatch means the probe never succeeds, so every notebook is reported
  not-ready and culled at timeout even though the sidecar is healthy.
- **Change impact:** update both templates and, where Istio is enforced, the
  `notebook-token-ready-authorizationpolicy` that permits this port.
- **Notes / gotchas:** unrelated to `API_ADDRESS`.

### `API_PLATFORM_TOKEN_READY_TIMEOUT_SECS`

- **Type / format:** int, seconds.
- **Required:** no
- **Purpose:** how long the API waits for sidecar readiness before giving up on a notebook.
- **Expected value:** longer than a cold image pull plus the first Keycloak round-trip. 40–120 s is
  reasonable; size it against observed notebook start time.
- **Example value:** `40`
- **Default if omitted:** `40`.
- **How to obtain:** measure notebook start latency in the target cluster.
- **Failure mode:** too low and notebooks are marked failed during a slow image pull — the classic
  "works when warm, fails on a cold node". Too high and genuinely broken notebooks occupy a slot
  for the full duration.
- **Change impact:** none to data.
- **Notes / gotchas:** unrelated to the HTTP timeouts below.

### CORS and rate limiting

### `API_CORS_ORIGINS`

- **Type / format:** string, comma-separated origins, or `*`.
- **Required:** no
- **Purpose:** allowed browser origins; parsed by `parseAllowedOrigins` in `utils.go`.
- **Expected value:** explicit scheme + host [+ port] entries, no trailing slash and no path.
- **Example value:** `https://sandbox.tgdex.telangana.gov.in,https://tgdex.telangana.gov.in`
- **Default if omitted:** `""` → the allow-list is empty, so cross-origin browser calls are refused.
- **How to obtain:** the frontend's deployed origins.
- **Failure mode:** a missing origin produces browser-only failures — `curl` succeeds while the
  frontend reports a CORS error, so it is easy to misdiagnose as an outage.
- **Change impact:** none to data.
- **Notes / gotchas:** the deployed value is `*`. That is acceptable only because auth is by
  bearer token rather than cookies; if cookie auth is ever introduced, `*` becomes a real
  vulnerability and must be replaced with an explicit list.

### `API_RATE_LIMIT`

- **Type / format:** int, requests per IP per window.
- **Required:** no — but see the failure mode.
- **Purpose:** maximum requests one client IP may make within `API_RATE_WINDOW_SECS`.
- **Expected value:** size against expected per-user request bursts; the frontend issues several
  calls per page load. 80/30 s is the deployed setting.
- **Example value:** `80`
- **Default if omitted:** **`0`, not "unlimited"** — the field has no `envDefault`, so it takes
  Go's zero value.
- **How to obtain:** operator decision, informed by frontend traffic.
- **Failure mode:** with `API_RATE_LIMIT=0` and a non-zero window, `isAllowed` admits only the
  first request per IP per window and returns 429 for the rest — a near-total outage that looks
  like a load-balancer fault. Set it explicitly.
- **Change impact:** none to data.
- **Notes / gotchas:** the limiter is per-pod and in-memory, so the effective cluster-wide limit is
  this value multiplied by the replica count, and it resets on every restart. Behind a proxy that
  does not set the client IP, all traffic shares one bucket.

### `API_RATE_WINDOW_SECS`

- **Type / format:** int, seconds.
- **Required:** no
- **Purpose:** the fixed window over which `API_RATE_LIMIT` is counted.
- **Expected value:** 10–60 s.
- **Example value:** `30`
- **Default if omitted:** `0`.
- **How to obtain:** operator decision.
- **Failure mode:** `0` makes the elapsed-time check `now.Sub(firstReq) >= 0` always true, so the
  counter resets on every request and **rate limiting is silently disabled entirely** — the
  opposite of the `API_RATE_LIMIT=0` failure, and equally invisible.
- **Change impact:** none.
- **Notes / gotchas:** fixed window, not sliding: up to `2 × API_RATE_LIMIT` requests can land
  across a window boundary.

### Timeouts and request limits

All five are plain `int` seconds with **no default**, so an unset value is `0`. `0` does not mean
the same thing for each — the distinction is the point of this subsection.

### `API_TIMEOUT_SECS`

- **Type / format:** int, seconds.
- **Required:** no
- **Purpose:** per-request `context.WithTimeout` applied in `middleware.go`; bounds handler and
  database work.
- **Expected value:** longer than the slowest legitimate handler (notebook creation is the worst
  case), shorter than the ingress timeout.
- **Example value:** `60`
- **Default if omitted:** `0` — **`context.WithTimeout(ctx, 0)` is already expired**, so every
  request fails immediately. Must be set.
- **How to obtain:** measure p99 handler latency.
- **Failure mode:** unset → uniform instant `context deadline exceeded` on all routes. Too low →
  notebook creation aborts midway, leaving Kubernetes objects behind that the worker must reconcile.
- **Change impact:** none to data, but a too-low value can leave orphaned PVCs.
- **Notes / gotchas:** keep below the ingress/load-balancer timeout so clients see the API's error
  rather than a gateway 504.

### `API_READ_TIMEOUT_SECS` / `API_WRITE_TIMEOUT_SECS` / `API_IDLE_TIMEOUT_SECS`

- **Type / format:** int, seconds. Passed to `http.Server.ReadTimeout`, `WriteTimeout`,
  `IdleTimeout` in `main.go`.
- **Required:** no
- **Purpose:** transport-level limits — time to read a request, to write a response, and to hold an
  idle keep-alive connection.
- **Expected value:** read ≥ largest upload duration; write ≥ `API_TIMEOUT_SECS`; idle > client
  keep-alive interval.
- **Example values:** `30` / `60` / `120`
- **Default if omitted:** `0`, which for `http.Server` means **no timeout at all** — the opposite
  of the `API_TIMEOUT_SECS` behaviour above.
- **How to obtain:** operator decision.
- **Failure mode:** all zero → slow-client connections accumulate until the pod exhausts file
  descriptors, appearing as a gradual degradation rather than a clean failure. `WriteTimeout`
  below `API_TIMEOUT_SECS` truncates slow responses mid-body, which clients report as malformed
  JSON.
- **Change impact:** none to data.
- **Notes / gotchas:** keep `WriteTimeout ≥ API_TIMEOUT_SECS`, or the handler's own deadline can
  never fire cleanly.

### `API_MAX_BODY_SIZE_IN_MB`

- **Type / format:** int, mebibytes. Applied as `http.MaxBytesHandler(handler, n<<20)`.
- **Required:** no
- **Purpose:** caps request body size across all routes.
- **Expected value:** the largest legitimate upload, plus headroom.
- **Example value:** `5`
- **Default if omitted:** `0` → **every request with a body is rejected**, including small JSON
  POSTs.
- **How to obtain:** operator decision.
- **Failure mode:** unset → all writes fail with `http: request body too large` (HTTP 413) while
  GETs keep working — which reads as a routing bug. Too low → uploads fail at a fixed size.
- **Change impact:** none.
- **Notes / gotchas:** the limit is bytes-on-the-wire, so base64-encoded payloads need roughly a
  third more headroom than the source file.

### Feature flags

### `API_KYC_ENABLED`

- **Type / format:** bool — `true` | `false` (Go `strconv.ParseBool`, so `1`/`0`/`t`/`f` also parse).
- **Required:** yes — this flag has no default and startup fails without it.
- **Purpose:** gates notebook creation on the caller's KYC status; when true, un-verified users are
  refused.
- **Expected value:** `true` in any environment holding real user data.
- **Example value:** `true`
- **Default if omitted:** none — startup fails.
- **How to obtain:** product/compliance decision, not an operator preference.
- **Fields that become required when true:** none additional in this service — verification status
  is read from the database, not from config.
- **Failure mode:** an unparseable value (e.g. `yes`) fails startup with
  `strconv.ParseBool: parsing "yes": invalid syntax`. Set to `false` in production, users bypass
  KYC silently — there is no warning log.
- **Change impact:** none to data; changes who may create notebooks from the next request onward.
- **Notes / gotchas:** the live cluster runs `false` (a dev environment); the manifests in this
  repo say `true`. Confirm the intended value per environment before applying.

### `API_BOOKINGS_ENABLED`

- **Type / format:** bool.
- **Required:** no
- **Purpose:** enables the slot-booking/scheduling routes. When false, booking endpoints are not
  served and notebooks are created on demand.
- **Expected value:** `true` where GPU capacity is scheduled by slot.
- **Example value:** `true`
- **Default if omitted:** `true`.
- **Fields that become required when true:** `SLOT_CONFIG_PROFILE` should be set to a profile
  defined in `pkg/gpuconfig`, and the slot-lifecycle Deployment must be running — without it, bookings
  are accepted but never transition state.
- **How to obtain:** product decision.
- **Failure mode:** enabled without the slot-lifecycle Deployment running, bookings sit in their initial
  state forever and users see reservations that never start. No error is logged by the API.
- **Change impact:** disabling with live bookings in the table strands them; drain first.
- **Notes / gotchas:** also read via `os.Getenv` in a test helper; the struct tag is authoritative.

### `API_DISABLE_INIT`

- **Type / format:** bool.
- **Required:** no
- **Purpose:** tells the API whether demo files are seeded into new notebooks. It does **not**
  control seeding — it only changes the URL the API generates, pointing at the JupyterLab auto
  workspace instead of a notebook file.
- **Expected value:** the inverse of `spec.lifecycle.demoFiles.enabled` in the worker's notebook
  templates.
- **Example value:** `false`
- **Default if omitted:** `false`.
- **How to obtain:** read `demoFiles.enabled` from `infra/worker/configmap.yaml`.
- **Failure mode:** the mismatch case is the dangerous one — `false` while `demoFiles.enabled` is
  also false generates a URL to a notebook file that was never created, so users land on a
  JupyterLab **404 page** inside a working notebook. Nothing fails server-side.
- **Change impact:** must be changed in lockstep with the worker templates.
- **Notes / gotchas:** the tightest API↔worker coupling in the system and the least obvious.

### `WORKSPACE_ENABLED`

- **Type / format:** bool.
- **Required:** no
- **Purpose:** enables the profile-scoped shared CephFS workspace used by the no-code sharing flow.
  When true, `ensureProfileWorkspacePVC` in `cmd/api/k8s.go` creates a PVC named `workspace` in
  each user's profile namespace — storage class `ceph-filesystem`, `ReadWriteMany`, `Filesystem`,
  `50Gi` — labelled `sandbox-connect.tgdex.io/profile-workspace: true`. The API waits for Kubeflow
  to create the namespace first.
- **Expected value:** `true` only on clusters that provide a `ReadWriteMany` CephFS storage class.
- **Example value:** `false`
- **Default if omitted:** `false`.
- **How to obtain:** determined by the cluster's storage capability, not by preference — check for
  the storage class with `kubectl get storageclass ceph-filesystem`.
- **Fields that become required as a result:** none in config, but the cluster **must** provide a
  `ceph-filesystem` storage class supporting `ReadWriteMany`. The size, class, access mode and
  volume mode are compile-time constants in `cmd/api/k8s.go`, not configurable.
- **Failure mode:** **this flag is read by both the API and the worker and the two must agree.**
  The split cases fail differently and neither is obvious:
  - API `true`, worker `false` → a 50 GiB RWX PVC is created per profile and never mounted. Silent
    storage waste that scales with user count.
  - API `false`, worker `true` → the worker keeps the `shared-workspace` volume in the notebook
    spec and waits for a PVC the API never creates, so notebooks stall against the template's
    `existing` volume policy rather than starting.

  On a cluster with no `ceph-filesystem` class, the PVC is created but stays `Pending` forever, and
  every notebook then blocks waiting for it to bind.
- **Change impact:** enabling it on an existing deployment provisions a PVC per profile namespace
  on next reconcile. Disabling it leaves those PVCs behind — they are not garbage-collected, so
  reclaim them manually.
- **Notes / gotchas:** deliberately unprefixed, like `SLOT_CONFIG_PROFILE`, because it is shared
  across services. Notebooks mount the claim **read-only** at `/home/jovyan/workspace`; the claim
  stays writable so an external copy pod can populate it. If an existing `workspace` PVC does not
  match the expected class, access mode, volume mode and size, the API logs a mismatch rather than
  silently adopting it.

### Notebook defaults and quotas

`SLOT_CONFIG_PROFILE`, `API_KUBEFLOW_URL` and the notebook resource defaults live in
`NotebookConfig`.

### `API_KUBEFLOW_URL`

- **Type / format:** string, absolute URL with scheme; may include a path prefix.
- **Required:** yes
- **Purpose:** base URL used to build the user-facing notebook link returned by the API.
- **Expected value:** the externally reachable Kubeflow/notebook host, no trailing slash.
- **Example value:** `https://sandbox.tgdex.telangana.gov.in`
- **Default if omitted:** none — startup fails.
- **How to obtain:** the ingress hostname for Kubeflow in the target environment.
- **Failure mode:** an internal or wrong host yields notebook links that are unreachable from a
  browser. The API returns 200 with a broken URL — no server-side error at all.
- **Change impact:** coordinate with the Kubeflow ingress.
- **Notes / gotchas:** the live cluster includes a path suffix (`/kubeflow`); whether one is needed
  depends on how Kubeflow is exposed.

### `SLOT_CONFIG_PROFILE`

- **Type / format:** string, profile name defined in `pkg/gpuconfig`.
- **Required:** no
- **Purpose:** selects the code-defined slot/category configuration used for booking.
- **Expected value:** `production` — the only profile defined in code. Empty resolves to it as well.
- **Example value:** `production`
- **Default if omitted:** `""`, then falls back to `API_GPU_SLOT_CONFIG_PROFILE`, then to the
  code default.
- **How to obtain:** read the profile names in `pkg/gpuconfig`.
- **Failure mode:** an unknown profile name means slot lookups find no matching configuration and
  bookings fail to schedule.
- **Change impact:** must match the slot-lifecycle controller's `SLOT_CONFIG_PROFILE`; a mismatch has the
  two components disagreeing about slot boundaries.
- **Notes / gotchas:** unprefixed on purpose — the same variable name is shared with slot lifecycle.

### `API_GPU_SLOT_CONFIG_PROFILE` — **deprecated**

- **Type / format:** string.
- **Required:** no
- **Purpose:** legacy name for `SLOT_CONFIG_PROFILE`, consulted in `main.go` only when the latter
  is empty.
- **Expected value:** unset. Migrate to `SLOT_CONFIG_PROFILE`.
- **Default if omitted:** `""`.
- **Failure mode:** setting both with different values silently prefers `SLOT_CONFIG_PROFILE`.
- **Change impact:** none.
- **Notes / gotchas:** retained for backward compatibility with older deployments; remove once no
  environment sets it.

### `API_NOTEBOOK_LIST_LIMIT`

- **Type / format:** int, rows.
- **Required:** no
- **Purpose:** default page size for the notebook list endpoint (`handlers.go`).
- **Expected value:** 10–50.
- **Example value:** `10`
- **Default if omitted:** `0` → the list endpoint returns an empty page by default.
- **How to obtain:** operator/frontend decision.
- **Failure mode:** unset → users see "no notebooks" despite having them; reads as data loss.
- **Change impact:** none.
- **Notes / gotchas:** callers can override per request; only the default is configured here.

### `API_STARTUP_NOTEBOOK_FILENAME`

- **Type / format:** string, file name relative to the PVC root; empty allowed.
- **Required:** no
- **Purpose:** the notebook file a generated notebook URL opens. Empty opens the JupyterLab file
  browser instead.
- **Expected value:** a file the init container actually creates on the PVC.
- **Example value:** `demo.ipynb`
- **Default if omitted:** `""` → file browser.
- **How to obtain:** must match what `demoFiles.initImage` writes into the workspace.
- **Failure mode:** naming a file the init image does not create gives users a JupyterLab 404 on
  first open — the same symptom as an `API_DISABLE_INIT` mismatch, from a different cause.
- **Change impact:** coordinate with the init image contents.
- **Notes / gotchas:** `generateNotebookURL` overrides this for images with a known skeletal
  notebook, so the effective value is not always what is configured here.

### Notebook resource defaults

Thirteen fields with identical semantics: they populate the `resources` block of a created
notebook when the request does not specify one. All are **`,required`** — startup fails if any is
absent — and all are strings passed to Kubernetes verbatim, so they must be valid
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/)
values. There is **no validation in this service**; a malformed value is accepted at startup and
rejected later by the API server when a notebook is created.

**Shared attributes for all thirteen** — *Required:* yes · *Default if omitted:* none, startup
fails · *How to obtain:* capacity decision, sized against the smallest node in the relevant pool ·
*Change impact:* affects newly created notebooks only; existing notebooks keep their resources ·
*Failure mode:* an invalid quantity (e.g. `4GB` instead of `4Gi`) passes startup and fails at
notebook creation with `quantities must match the regular expression …`. A request exceeding node
capacity leaves the notebook `Pending` with
`0/N nodes are available: Insufficient cpu` — the API reports success and the user sees a notebook
that never starts.

| Field | Type / format | Purpose | Example | Notes / gotchas |
|---|---|---|---|---|
| `API_DEFAULT_CPU_STORAGE_SIZE` | quantity | PVC size for CPU notebooks | `10Gi` | Cannot be shrunk later; PVC expansion needs an `allowVolumeExpansion` StorageClass. |
| `API_DEFAULT_CPU_REQUEST` | decimal cores | CPU request, CPU notebooks | `1.25` | Sum across `API_MAX_TOTAL_CPU` notebooks must fit the pool. |
| `API_DEFAULT_CPU_LIMIT` | decimal cores | CPU limit, CPU notebooks | `1.5` | Must be ≥ the request or the pod is rejected. |
| `API_DEFAULT_MEMORY_REQUEST` | quantity | Memory request, CPU notebooks | `3.5Gi` | |
| `API_DEFAULT_MEMORY_LIMIT` | quantity | Memory limit, CPU notebooks | `4Gi` | Exceeding it OOM-kills the kernel; users report "notebook restarted". |
| `API_DEFAULT_GPU_STORAGE_SIZE` | quantity | PVC size for GPU notebooks | `50Gi` | Larger than CPU by convention — datasets. |
| `API_DEFAULT_GPU_TYPE` | resource name | GPU extended-resource key | `nvidia.com/gpu` | Must match what the device plugin advertises; a typo leaves pods `Pending` forever. |
| `API_DEFAULT_GPU_REQUEST` | integer | GPU count requested | `1` | GPUs are integral and non-overcommittable; request must equal limit. |
| `API_DEFAULT_GPU_LIMIT` | integer | GPU count limit | `1` | Must equal the request. |
| `API_DEFAULT_GPU_CPU_REQUEST` | decimal cores | CPU request on GPU notebooks | `3` | Sized against the GPU node type, not the CPU pool. |
| `API_DEFAULT_GPU_CPU_LIMIT` | decimal cores | CPU limit on GPU notebooks | `3.9` | Keep below node allocatable, which is less than the advertised vCPU count. |
| `API_DEFAULT_GPU_MEMORY_REQUEST` | quantity | Memory request, GPU notebooks | `12Gi` | |
| `API_DEFAULT_GPU_MEMORY_LIMIT` | quantity | Memory limit, GPU notebooks | `14Gi` | Host memory, not VRAM — VRAM is fixed by the GPU model. |

### `API_GPU_NODE_INSTANCE_TYPES`

- **Type / format:** string, comma-separated instance types, no spaces.
- **Required:** yes
- **Purpose:** the values matched against the `node.kubernetes.io/instance-type` label for GPU
  notebook scheduling.
- **Expected value:** exactly the labels present on GPU nodes in this cluster.
- **Example value:** `g4dn.xlarge,p4d.24xlarge,p5.48xlarge`
- **Default if omitted:** none — startup fails.
- **How to obtain:** `kubectl get nodes -L node.kubernetes.io/instance-type`.
- **Failure mode:** a value matching no node leaves GPU notebooks `Pending` with
  `didn't match Pod's node affinity/selector`. Nothing fails at startup, and CPU notebooks are
  unaffected — so it presents as "GPU is broken".
- **Change impact:** must track the node pool; adding a GPU node type requires updating this.
- **Notes / gotchas:** on-prem clusters often label nodes with a role word (`gpu`, `aic`) rather
  than a cloud instance type — the CBR worker templates do exactly this. The value must match the
  cluster's actual labelling scheme, not AWS naming.

### Per-user quotas

Four `int` fields, no defaults, enforced in `handlers.go` before notebook creation.

**Shared attributes** — *Required:* no · *Default if omitted:* `0` · *How to obtain:* capacity
policy decision · *Change impact:* none to existing notebooks; applies from the next creation
request · *Failure mode:* **`0` blocks all creation.** The checks are `>=`, so
`runningCPU >= 0` is true on the very first attempt and users get
`Cannot create CPU notebook: running CPU notebook limit exceeded (0/0 running)`. Leaving these
unset is the most common cause of "nobody can create notebooks" after a fresh deployment.

| Field | Purpose | Example | Notes / gotchas |
|---|---|---|---|
| `API_MAX_RUNNING_CPU` | Max simultaneously **running** CPU notebooks per user | `2` | Stopped notebooks do not count. |
| `API_MAX_RUNNING_GPU` | Max simultaneously running GPU notebooks per user | `1` | Keep low; GPUs cannot be overcommitted. |
| `API_MAX_TOTAL_CPU` | Max CPU notebooks per user, running or stopped | `6` | Each still holds a PVC — this is the real storage driver. |
| `API_MAX_TOTAL_GPU` | Max GPU notebooks per user, running or stopped | `4` | A combined check on `MaxTotalCPU + MaxTotalGPU` also applies, so the two interact. |

### Registry secret provisioning

The API provisions an image-pull secret into user namespaces so notebook images can be pulled.
`API_REGISTRY_SECRET_TYPE` selects the flow and determines which other fields are required.

### `API_REGISTRY_SECRET_TYPE`

- **Type / format:** string enum — `ecr` | `private-registry` | `none`.
- **Required:** no
- **Purpose:** discriminator selecting the credential flow; `none` provisions no secret.
- **Expected value:** `ecr` on AWS with ECR; `private-registry` for a static-credential registry
  (e.g. the CBR on-prem Harbor); `none` for a public registry.
- **Example value:** `ecr`
- **Default if omitted:** `none`.
- **How to obtain:** determined by where notebook images are hosted.
- **Fields that become required as a result:**
  - `ecr` → `API_REGISTRY_ECR_REGION`, `API_REGISTRY_AWS_ACCESS_KEY_ID`, `API_REGISTRY_AWS_SECRET_KEY`, `API_REGISTRY_URL`
  - `private-registry` → `API_REGISTRY_USERNAME`, `API_REGISTRY_PASSWORD`, `API_REGISTRY_URL`
  - `none` → nothing
- **Failure mode:** an unrecognised value is treated as `none`, so no secret is created and every
  notebook fails with `ImagePullBackOff` — a notebook-level symptom for an API-level
  misconfiguration.
- **Change impact:** switching type requires deleting the previously provisioned secret in each
  user namespace, or stale credentials persist.
- **Notes / gotchas:** the required-field sets are enforced by the flow at runtime, not by
  `env.Parse`, so a half-configured `ecr` setup starts cleanly and fails on first notebook.

### `API_REGISTRY_SECRET_NAME`

- **Type / format:** string, Kubernetes object name (RFC 1123 label).
- **Required:** no
- **Purpose:** name of the `kubernetes.io/dockerconfigjson` Secret created in each user namespace.
- **Expected value:** must equal the `imagePullSecrets[].name` in both notebook templates.
- **Example value:** `v2-registry-cred`
- **Default if omitted:** `registry-cred`.
- **How to obtain:** operator decision; read the current value from `infra/worker/configmap.yaml`.
- **Failure mode:** mismatch with the templates → notebooks reference a secret that does not exist
  and fail with `ImagePullBackOff`, while the API reports the secret created successfully.
- **Change impact:** update both notebook templates in the same change.
- **Notes / gotchas:** the repo's API config uses `v2-registry-cred` while the worker templates
  reference `registry-cred`. Applied together as-is, every notebook would fail with
  `ImagePullBackOff` — reconcile the two before deploying the API and worker from this branch
  together.

### `API_REGISTRY_URL`

- **Type / format:** string, registry host. **No scheme, no trailing slash, no path.**
- **Required:** conditional on `API_REGISTRY_SECRET_TYPE` ≠ `none`
- **Purpose:** the registry the generated docker config authenticates against.
- **Expected value:** hostname only; for ECR, `<account-id>.dkr.ecr.<region>.amazonaws.com`.
- **Example value:** `108779579607.dkr.ecr.ap-south-1.amazonaws.com`
- **Default if omitted:** `""`.
- **How to obtain:** AWS console (ECR registry URI) or the registry operator.
- **Failure mode:** including `https://` produces a docker config keyed on a host that never
  matches the image reference, so the credential is silently ignored and pulls fail as
  unauthenticated.
- **Change impact:** must match the registry prefix of the images in the notebook templates.
- **Notes / gotchas:** the account ID in this value must be the account that owns the images —
  note that `.env.all.example` and the deployed ConfigMap currently reference **different** AWS
  accounts.

### `API_REGISTRY_ECR_REGION`

- **Type / format:** string, AWS region ID.
- **Required:** conditional on `API_REGISTRY_SECRET_TYPE=ecr`
- **Purpose:** region for the `ecr:GetAuthorizationToken` call.
- **Expected value:** the region embedded in `API_REGISTRY_URL`.
- **Example value:** `ap-south-1`
- **Default if omitted:** `""` → the AWS SDK fails with `could not find region configuration`.
- **How to obtain:** AWS console.
- **Failure mode:** a region that disagrees with `API_REGISTRY_URL` returns a token valid for the
  wrong registry; pulls fail with a 401 from ECR.
- **Change impact:** none.
- **Notes / gotchas:** must match the region substring of the registry URL exactly.

### `API_REGISTRY_AWS_ACCESS_KEY_ID` + `API_REGISTRY_AWS_SECRET_KEY`

Documented as a pair — see §3.

- **Type / format:** strings. Access key IDs begin `AKIA` (long-lived) or `ASIA` (temporary).
- **Required:** conditional on `API_REGISTRY_SECRET_TYPE=ecr`
- **Purpose:** IAM credentials used solely to call `ecr:GetAuthorizationToken` and mint the
  12-hour registry token the API then writes into user namespaces.
- **Expected value:** an IAM user dedicated to this service. **Secret — Kubernetes Secret only.**
- **Example value:** `AKIAIOSFODNN7EXAMPLE` / `<40-char secret>`
- **Default if omitted:** `""` → the SDK falls back to the node instance role, which usually lacks
  ECR permissions.
- **How to obtain:** AWS IAM → Users → create an access key. Store in Secret `api-creds`.
- **Privileges required:** see §3.
- **Failure mode:** missing or wrong → `AccessDeniedException … not authorized to perform:
  ecr:GetAuthorizationToken` in the API log; every notebook then hits `ImagePullBackOff`. Because
  the token lasts 12 hours, revoked credentials keep working until the next refresh — failures
  appear up to half a day after the change, which badly obscures the cause.
- **Change impact:** rotate by updating the Secret and restarting the API.
- **Notes / gotchas:** prefer IRSA (a ServiceAccount-bound IAM role) over static keys where the
  cluster supports it, which removes these two fields entirely. Keep both values in the `api-creds`
  Secret — never in `api-config`, where they would be readable by anyone with `get configmap` in
  the namespace and printed verbatim by `kubectl describe`.

### `API_REGISTRY_USERNAME` + `API_REGISTRY_PASSWORD`

- **Type / format:** strings.
- **Required:** conditional on `API_REGISTRY_SECRET_TYPE=private-registry`
- **Purpose:** static credentials for a non-ECR registry.
- **Expected value:** a **read-only** registry account. **Secret — Kubernetes Secret only.**
- **Example value:** `sandbox-puller` / `<token>`
- **Default if omitted:** `""`.
- **How to obtain:** the registry operator (Harbor/Nexus/GHCR). For GHCR use a PAT with
  `read:packages` only.
- **Privileges required:** pull on the notebook image repositories and nothing else. These
  credentials are copied into every user namespace, where users can read them — so any push
  privilege here is effectively granted to all sandbox users.
- **Failure mode:** wrong credentials → `unauthorized: authentication required` on pull;
  notebooks sit in `ImagePullBackOff`.
- **Change impact:** rotating requires deleting the provisioned secret in user namespaces so it is
  regenerated.
- **Notes / gotchas:** both were missing from `.env.all.example` before this revision, which made
  the `private-registry` flow undiscoverable.

### Audit publishing (RabbitMQ)

All eight fields are optional. If `RABBITMQ_HOST` is empty, `application.auditService` is `nil`
and auditing is **silently disabled** — there is no warning. Treat "no audit events" as a config
problem before suspecting the broker.

### `RABBITMQ_HOST`

- **Type / format:** string, hostname. No scheme, no port.
- **Required:** no — but it is the switch that enables auditing.
- **Purpose:** broker host for audit events.
- **Expected value:** hostname or in-cluster Service DNS name.
- **Example value:** `rabbitmq.messaging.svc.cluster.local`
- **Default if omitted:** `""` → auditing disabled.
- **How to obtain:** the RabbitMQ operator.
- **Failure mode:** unresolvable host → connection errors logged per publish attempt; **requests
  still succeed**, so the only signal is missing audit records.
- **Change impact:** none to data.
- **Notes / gotchas:** the placeholder `<your-rabbitmq-host>` shipped in the ConfigMap is a
  non-empty string, so it *enables* auditing and then fails DNS on every publish. Set it to a real
  host or remove the key entirely.

### `RABBITMQ_PORT`

- **Type / format:** string (not int), TCP port.
- **Required:** no
- **Purpose:** broker port.
- **Expected value:** `5672` for AMQP, `5671` for AMQPS.
- **Example value:** `5672`
- **Default if omitted:** `""`.
- **How to obtain:** RabbitMQ operator.
- **Failure mode:** plaintext port against a TLS-only listener hangs until the connection times out
  rather than failing fast.
- **Change impact:** none.
- **Notes / gotchas:** must agree with `RABBITMQ_ONLY_FOR_MAHA_AGX` — see below.

### `RABBITMQ_VHOST`

- **Type / format:** string, virtual host name.
- **Required:** no
- **Purpose:** the vhost the connection opens.
- **Expected value:** `/` unless a dedicated vhost exists.
- **Example value:** `/`
- **Default if omitted:** `""`, which is **not** the same as `/`.
- **How to obtain:** RabbitMQ operator.
- **Failure mode:** a vhost the user cannot access → `ACCESS_REFUSED - access to vhost '…' refused
  for user`.
- **Change impact:** none.
- **Notes / gotchas:** `/` must be written literally; some tooling URL-encodes it as `%2F`, which
  is wrong here.

### `RABBITMQ_USERNAME` + `RABBITMQ_PASSWORD`

- **Type / format:** strings.
- **Required:** conditional — required when `RABBITMQ_HOST` is set.
- **Purpose:** AMQP credentials for publishing audit events.
- **Expected value:** a publish-only account. **Secret — Kubernetes Secret only.**
- **Example value:** `sandbox-audit` / `<password>`
- **Default if omitted:** `""` → `ACCESS_REFUSED` on connect.
- **How to obtain:** RabbitMQ operator; see §3 for the exact `rabbitmqctl` grants.
- **Privileges required:** see §3.
- **Failure mode:** `ACCESS_REFUSED - Login was refused using authentication mechanism PLAIN`,
  logged per attempt; API requests continue to succeed.
- **Change impact:** rotate in the Secret and restart.
- **Notes / gotchas:** **the live cluster holds these in a ConfigMap; rotate and move them to
  `api-creds`.** Passwords containing `%`, `#`, `^` or `:` are fine here because they are passed
  as discrete fields rather than inside a URI.

### `RABBITMQ_EXCHANGE`

- **Type / format:** string, exchange name.
- **Required:** conditional — required when `RABBITMQ_HOST` is set.
- **Purpose:** target exchange for audit messages.
- **Expected value:** an existing exchange; the service does not declare it.
- **Example value:** `auditing`
- **Default if omitted:** `""` → publishes to the default exchange, where the routing key is
  treated as a queue name.
- **How to obtain:** the audit platform owner.
- **Failure mode:** a non-existent exchange closes the channel with
  `NOT_FOUND - no exchange '…' in vhost '/'`.
- **Change impact:** coordinate with the audit consumer.
- **Notes / gotchas:** must pre-exist, with a type compatible with the routing key in use.

### `RABBITMQ_ROUTING_KEY`

- **Type / format:** string, routing key.
- **Required:** conditional — required when `RABBITMQ_HOST` is set.
- **Purpose:** routing key attached to each audit message.
- **Expected value:** whatever the audit consumer binds on.
- **Example value:** `sandbox.audit`
- **Default if omitted:** `""` → messages route nowhere on a topic/direct exchange and are
  **silently dropped**.
- **How to obtain:** the audit platform owner.
- **Failure mode:** no error at all. Publishing succeeds; nothing arrives. Confirm with the
  consumer, not the API log.
- **Change impact:** coordinate with the consumer's binding.
- **Notes / gotchas:** the shipped placeholder `<your-routing-key>` publishes to a key nothing is
  bound to.

### `RABBITMQ_ONLY_FOR_MAHA_AGX`

- **Type / format:** string (not bool) — `"true"` enables AMQPS.
- **Required:** no
- **Purpose:** despite the name, this selects the **TLS transport** (`amqps://` instead of
  `amqp://`) for the MahaAGX deployment.
- **Expected value:** `"true"` only in MahaAGX; unset elsewhere.
- **Example value:** `true`
- **Default if omitted:** `""` → plaintext AMQP.
- **How to obtain:** determined by the target environment.
- **Failure mode:** `true` against a non-TLS listener fails the handshake; `false` against a
  TLS-only listener hangs until timeout. Neither affects API request handling.
- **Change impact:** must be set together with the matching `RABBITMQ_PORT`.
- **Notes / gotchas:** typed as `string`, so it is compared literally — `True` or `1` do **not**
  enable TLS. The name describes a deployment rather than the behaviour; rename when the MahaAGX
  branch merges.

---

## 3. Extra requirements by field category

### Credentials

#### Postgres — `API_POSTGRES_URL`

The account lives in the Postgres instance backing the sandbox. It needs DML on the application
tables, no DDL (migrations are applied separately):

```sql
CREATE USER sandbox_api WITH PASSWORD '<generated>';
GRANT CONNECT ON DATABASE sandbox_db TO sandbox_api;
GRANT USAGE ON SCHEMA public TO sandbox_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO sandbox_api;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO sandbox_api;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sandbox_api;
```

Created by the DBA/DevOps owner of `database-creds`. The worker and both background components may share this role
or use separate roles with the same grants.

#### RabbitMQ — `RABBITMQ_USERNAME` / `RABBITMQ_PASSWORD`

Publish-only on the audit exchange; no configure or read rights:

```bash
rabbitmqctl add_user sandbox-audit '<generated>'
rabbitmqctl set_permissions -p / sandbox-audit "^$" "^auditing$" "^$"
```

The three regexes are configure / write / read. `"^$"` matches nothing, so the account can neither
declare topology nor consume — appropriate for a publisher. Created by the RabbitMQ operator.

#### AWS IAM — `API_REGISTRY_AWS_ACCESS_KEY_ID` / `API_REGISTRY_AWS_SECRET_KEY`

One IAM user for this service. `ecr:GetAuthorizationToken` is account-wide by API design and
cannot be resource-scoped; scope the pull actions instead:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*" },
    { "Effect": "Allow",
      "Action": ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"],
      "Resource": "arn:aws:ecr:ap-south-1:<account-id>:repository/tgdex/*" }
  ]
}
```

No `ecr:PutImage` or any `ecr:*` write action — the resulting token is distributed to every user
namespace, so anything it can do, sandbox users can do. Created by the AWS account owner.

#### Registry — `API_REGISTRY_USERNAME` / `API_REGISTRY_PASSWORD`

Read-only pull on the notebook image repositories, for the same reason. Created by the registry
operator.

### Keycloak clients

Two distinct clients. Documenting the clients, not just the fields:

#### 1. Frontend client — `API_KEYCLOAK_CLIENT_ID`

| | |
|---|---|
| **Naming convention** | `angular-<tenant>-client` (e.g. `angular-tgdex-client`) |
| **Client type** | **Public** — it runs in a browser and holds no secret |
| **Flows enabled** | Standard flow (authorization code + PKCE). Service accounts **off**, direct grant **off** |
| **Realm roles** | Whatever the product defines for sandbox users |
| **Service-account roles** | None — public clients have no service account |
| **Scopes** | `openid`, `profile`, `email` |
| **Redirect URIs** | The frontend origins, exactly; avoid `*` |
| **Web origins** | Must include the origins listed in `API_CORS_ORIGINS` |

The API only *validates* tokens from this client — it never authenticates as it. Relation to other
fields: tokens are verified against `API_KEYCLOAK_PUBLIC_KEY` for realm `API_KEYCLOAK_REALM`.

#### 2. Notebook delegation client — `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`

| | |
|---|---|
| **Naming convention** | `sandbox-notebook` |
| **Client type** | **Confidential** — holds `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_SECRET` |
| **Flows enabled** | Standard flow off, implicit flow off, direct grant off, service accounts off |
| **Token exchange** | **Standard Token Exchange** on, including **Allow refresh token in Standard Token Exchange** |
| **Full scope allowed** | Off — explicitly grant only required scopes, roles, and claims |
| **Role mappings** | Only what the downstream file API requires, such as `consumer`, `provider`, and organisation claims; no `realm-management` roles unless a specific downstream check requires one |
| **Scopes** | Must permit the values requested by `API_PLATFORM_TOKEN_EXCHANGE_SCOPE` and any downstream file-service audience |
| **Redirect URIs** | None — no browser flow |
| **Session lifetime** | Realm and client idle/maximum lifetimes must cover the longest booking; refresh-token rotation cannot exceed Keycloak's absolute limits |

Relation to other fields: the same client ID appears as `KEYCLOAK_CLIENT_ID` and
`EXPECTED_CLIENT_ID` in the sidecar block of both notebook templates, and its secret is mounted
into the notebook as `KEYCLOAK_CLIENT_SECRET_FILE`.

#### Canonical Keycloak setup

1. Import
   [`infra/platform-token-sidecar/sandbox-notebook-client.json`](../../infra/platform-token-sidecar/sandbox-notebook-client.json)
   into the target realm. The import creates the `sandbox-notebook` confidential client with
   Standard Token Exchange and refresh-token exchange enabled.
2. Regenerate the imported `CHANGE_ME_AFTER_IMPORT` client secret immediately. Store the new value
   as `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_SECRET` in Kubernetes Secret `api-creds`; never put it
   in a ConfigMap or commit it to the repository.
3. Keep `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`, `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`,
   sidecar `KEYCLOAK_CLIENT_ID`, and sidecar `EXPECTED_CLIENT_ID` set to the same client ID in both
   notebook templates.
4. Keep the delegated subject unchanged. The API validates that the exchanged token's `sub`
   matches the notebook owner and its `azp` matches `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`.
5. Keep full-scope inheritance disabled. Explicitly assign only the file-service roles, claims,
   scopes, and audiences required inside the notebook.
6. Configure sidecar `KEYCLOAK_TOKEN_URL` for the same realm used by API authentication. The API
   realm, token endpoint, and downstream file API must form one compatible trust chain.
7. Size the realm and client idle/maximum session lifetimes for the longest booking. A rotated
   refresh token cannot extend the session beyond Keycloak's absolute lifetime.

The optional `API_PLATFORM_TOKEN_EXCHANGE_AUDIENCE` only filters audiences already available to
the notebook client. Add a distinct file-service audience through an allowed client scope before
requesting it.

##### Add the notebook audience to the browser client

The browser access token used as the exchange subject must contain `sandbox-notebook` in its
`aud` claim. Keycloak rejects exchange when the requesting notebook client is outside the subject
token's audience.

For the default browser client `angular-client`:

1. In the Keycloak Admin Console, open the target realm and select
   **Clients → angular-client → Client scopes**.
2. Open the browser client's dedicated scope, normally `angular-client-dedicated`.
3. Select **Mappers → Configure a new mapper → Audience**.
4. Configure and save:

   | Field | Value |
   |---|---|
   | `Name` | `sandbox-notebook-audience` |
   | `Included Client Audience` | `sandbox-notebook` |
   | `Add to access token` | On |
   | `Add to ID token` | Off |

5. Log in again so the frontend receives a new access token, then confirm its claims include:

   ```json
   {
     "azp": "angular-client",
     "aud": ["sandbox-notebook"]
   }
   ```

Other audiences may appear alongside `sandbox-notebook`. Prefer the browser client's dedicated
scope when every login should support notebook token exchange. If an optional scope supplies the
mapper, the frontend must explicitly request that scope during login.

##### Runtime session flow

The frontend creates a session with a bodyless authenticated
`POST /v1/bookings/{id}/notebook-token-session`, or
`POST /v1/notebook/{notebook_name}/notebook-token-session` when bookings are disabled. The API
exchanges the browser access token, stores the delegated bootstrap bundle in the per-notebook
Secret, and waits for the matching sidecar session to publish a usable access token. It returns
`200` only after readiness succeeds. A `503` with `Retry-After` means the frontend must retry
before opening the notebook URL.

The sidecar refreshes the delegated session and persists rotated refresh tokens with `PUT` to the
same endpoint. Only a delegated access token whose `sub` and `azp` match the notebook session may
perform that update; rotation does not repeat the synchronous readiness wait. Browser refresh
tokens are never sent to or stored by Sandbox Connect.

### Domains / URLs

| Field | Scheme | Trailing slash | Reach | Must match |
|---|---|---|---|---|
| `API_KEYCLOAK_URL` | required | no | public | `PROFILE_CREDIT_SYNC_KEYCLOAK_URL`; sidecar `KEYCLOAK_TOKEN_URL` prefix |
| `API_PLATFORM_TOKEN_EXCHANGE_TOKEN_URL` | required | no | public | derived from the two fields above when empty |
| `API_KUBEFLOW_URL` | required | no | **public** — it is handed to browsers | the Kubeflow ingress host |
| `API_JUPYTERLITE_BASE_URL` | none — path only | no | n/a | `infra/api/ingress.yaml` |
| `API_REGISTRY_URL` | **none** — host only | no | n/a | the image prefix in the notebook templates |
| `API_ADDRESS` | none — `host:port` | n/a | in-pod | `containerPort` in `manifest.yaml` |
| `RABBITMQ_HOST` | none — host only | n/a | internal | the broker Service |

### Tuning knobs

| Field | Safe range | Size against | Too low | Too high |
|---|---|---|---|---|
| `API_RATE_LIMIT` | 40–200 | frontend calls per page load × concurrent users per IP | 429 storms; `0` blocks everything | no protection from abusive clients |
| `API_RATE_WINDOW_SECS` | 10–60 | burst shape | limiter resets too often; `0` disables it | genuine users stay blocked longer |
| `API_TIMEOUT_SECS` | 30–120 | p99 handler latency | truncated notebook creation, orphaned PVCs; `0` fails every request | slow requests pin worker goroutines |
| `API_READ_TIMEOUT_SECS` | 15–60 | largest upload | uploads truncated | slow-loris exposure |
| `API_WRITE_TIMEOUT_SECS` | ≥ `API_TIMEOUT_SECS` | slowest response | responses truncated mid-body | connections held open |
| `API_IDLE_TIMEOUT_SECS` | 60–300 | client keep-alive | needless reconnects | FD exhaustion |
| `API_MAX_BODY_SIZE_IN_MB` | 1–50 | largest upload | 413 on normal requests; `0` blocks all bodies | memory pressure |
| `API_NOTEBOOK_LIST_LIMIT` | 10–50 | frontend page size | empty lists at `0` | slow list queries |
| `API_PLATFORM_TOKEN_READY_TIMEOUT_SECS` | 40–120 | cold-start image pull | notebooks culled during a slow pull | broken notebooks hold slots |
| `API_MAX_RUNNING_*` / `API_MAX_TOTAL_*` | ≥ 1 | cluster capacity ÷ expected users | **`0` blocks all creation** | capacity exhaustion, unbounded PVC growth |

There is no connection-pool size knob — the `pgx` pool uses its defaults. If Postgres
`max_connections` becomes a constraint, that is a code change, not a config change.

### Feature flags

| Flag | Turns on | Becomes required as a result |
|---|---|---|
| `API_KYC_ENABLED=true` | KYC gating on notebook creation | nothing in this service |
| `API_BOOKINGS_ENABLED=true` | slot booking routes | `SLOT_CONFIG_PROFILE`; the slot-lifecycle Deployment must be running |
| `API_DISABLE_INIT=true` | notebook URLs point at the JupyterLab auto workspace | `demoFiles.enabled: false` in both notebook templates |
| `WORKSPACE_ENABLED=true` | per-profile CephFS `workspace` PVC creation | a `ceph-filesystem` RWX storage class in the cluster; the **same** value on the worker |
| `API_REGISTRY_SECRET_TYPE=ecr` | ECR token minting | `API_REGISTRY_ECR_REGION`, `API_REGISTRY_AWS_ACCESS_KEY_ID`, `API_REGISTRY_AWS_SECRET_KEY`, `API_REGISTRY_URL` |
| `API_REGISTRY_SECRET_TYPE=private-registry` | static registry credentials | `API_REGISTRY_USERNAME`, `API_REGISTRY_PASSWORD`, `API_REGISTRY_URL` |
| `RABBITMQ_HOST` non-empty | audit publishing | `RABBITMQ_USERNAME`, `RABBITMQ_PASSWORD`, `RABBITMQ_EXCHANGE`, `RABBITMQ_ROUTING_KEY` |
| `RABBITMQ_ONLY_FOR_MAHA_AGX="true"` | AMQPS transport | a TLS-capable `RABBITMQ_PORT` (5671) |

### External provider fields

None in this service. The DigiLocker/KYC integration referenced by `API_KYC_ENABLED` reads
verification status from the database; the external relationship is owned by the platform team and
configured elsewhere.
