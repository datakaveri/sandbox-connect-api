# API server — deployment modes and gateway integration

| | |
|---|---|
| **Service** | `sandbox-api` (`cmd/api`) |
| **Config schema** | `cmd/api/types.go` — `ApiEnv.Gateway` (`GatewayConfig`, `WorkloadVerifierConfig`) |
| **Code** | `cmd/api/gateway_auth.go`, `cmd/api/middleware.go` (`authenticateBearer`) |
| **Operation manifest** | [`openapi/openapi.yaml`](../../openapi/openapi.yaml) (generated) |
| **Last updated** | 2026-10-28 |

The API runs in one of three modes, chosen by **configuration only** — the same image serves
every mode, and nothing outside `authMiddleware` branches on it.

| `API_AUTH_MODE` | Reached | Who authenticates the user | Use |
|---|---|---|---|
| `standalone` *(default)* | directly (its own Ingress) | the API — bearer, local claim policy | today's deployments; unchanged |
| `gateway` | only through `dx-gateway-go` | the gateway (JWT, operation manifest), **then** the API's claim policy on the forwarded bearer | Go-fleet deployments |
| `hybrid` | both | gateway hop when a workload credential is present, otherwise standalone | migration window only |

---

## 1. Request flow in `gateway` mode

```
client ── Bearer ──▶ dx-gateway-go (/sandbox route)
                      1. JWT verify (Keycloak JWKS)          ── 401
                      2. operation manifest: x-dx-authz      ── 404 undeclared op
                      3. project X-Subject-*, keep Bearer, attach X-DX-Workload (aud dx:svc:sandbox-connect-api)
                      4. strip /sandbox, proxy
                                │
                                ▼
                     sandbox-api (API_AUTH_MODE=gateway)
                      a. verify X-DX-Workload (JWKS)         ── 401 missing / invalid
                      b. workload ∈ SUBJECT_ASSERTERS         ── 403
                      c. subject = X-Subject-Id                ── 401 absent
                      d. forwarded Bearer → local claim policy (azp, email_verified, KYC)
                         and bearer.sub == X-Subject-Id       ── 401 mismatch
                      e. handlers (ownership scoped to the subject, as today)
```

Step (d) is why the gateway route sets `keep_authorization: true`: the bearer carries
`email_verified`, `kyc_verified` and `azp`, which the gateway does not project, and it is the
`subject_token` for the notebook token exchange (RFC 8693). Without it the token-session and
JupyterLite-session endpoints cannot work.

### Self-authenticating surfaces

Two surfaces carry a credential the **service** verifies, in every mode:

| Surface | Credential | Through the gateway? |
|---|---|---|
| `PUT /v1/bookings/{id}/notebook-token-session`, `PUT /v1/notebook/{name}/notebook-token-session` | platform notebook token (`azp` = `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`, issuer = the realm) | yes — declared `authentication: none` in the manifest; the service authenticates |
| `/jupyterlite/**` (static assets) | the `sandbox_jupyterlite_auth` cookie | **no** — see §3 |

A workload credential that is **present** on these requests is still verified (invalid → 401);
an absent one is fine. Projected subject headers are stripped and ignored.

---

## 2. Field reference

### `API_AUTH_MODE`
- **Type:** `standalone` \| `gateway` \| `hybrid` (case-insensitive). **Default:** `standalone`.
- **Failure mode:** any other value stops startup. A misspelling never falls back to standalone.

### `API_GATEWAY_REQUIRE_BEARER`
- **Type:** bool. **Default:** `true`.
- **Purpose:** in gateway mode, reject a request whose bearer the gateway did not forward
  (`401 … must set keep_authorization`). Leave it `true`.
- **When `false`:** identity is taken from the projected headers alone and the local claim policy
  cannot run. A KYC-gated request is then **refused** (`403`) when `API_KYC_ENABLED=true`, never
  served without the gate; email verification is delegated to the IdP/gateway.

### `WORKLOAD_VERIFIER_*` (fleet names — identical across every Go service)

| Env | Required in gateway/hybrid | Default | Meaning |
|---|---|---|---|
| `WORKLOAD_VERIFIER_ENFORCEMENT` | yes, must be `required` | — | anything else stops startup in gateway/hybrid; ignored in standalone |
| `WORKLOAD_VERIFIER_JWKS_URL` | yes | — | realm JWKS (in-cluster URL is fine) |
| `WORKLOAD_VERIFIER_ISSUER` | yes | — | issuer the **gateway** obtains its credential from (compose: `http://keycloak:8080/realms/iudx`) |
| `WORKLOAD_VERIFIER_SERVICE` | — | `sandbox-connect-api` | required audience is `dx:svc:<service>`; must equal the gateway route's `workload_destination` |
| `WORKLOAD_VERIFIER_SUBJECT_ASSERTERS` | yes | `dx-gateway-go` | comma list of workloads allowed to speak for an end user — each entry is impersonation authority |
| `WORKLOAD_VERIFIER_ALLOWED_CALLERS` | — | empty (= any verified workload) | optional allow-list of calling workloads |
| `WORKLOAD_VERIFIER_LEEWAY_SECONDS` | — | `30` | clock skew |

The verifier connects to the JWKS at startup, so a wrong URL fails the boot rather than the first
request.

---

## 3. Deploying behind the gateway

### 3.1 Gateway route (`dx-gateway-go` config)

```yaml
- path_prefix: /sandbox
  upstream: "http://sandbox-connect-api.<namespace>.svc:80"   # first host label = manifest name
  strip_prefix: true
  auth_mode: required            # the manifest overrides per operation
  keep_authorization: true       # §1 step (d)
  workload_destination: sandbox-connect-api
```

### 3.2 Operation manifest

Mount `openapi/openapi.yaml` into the gateway's `operation_manifest_dir` **as
`sandbox-connect-api.yaml`** — the filename must equal the upstream's first host label. The
gateway then enforces each operation's `x-dx-authz` and **denies every undeclared operation**.
Paths in the manifest are the service's own (after `/sandbox` is stripped).

### 3.3 Keycloak

The gateway's client must be able to mint a token with audience `dx:svc:sandbox-connect-api`
(client scope `dx-aud:sandbox-connect-api`). In dev, `scripts/keycloak-workload-identity.sh` in
the orchestrator repo provisions it.

### 3.4 JupyterLite

The asset tree is nested, and manifest templates match segment-for-segment, so it cannot be
declared and must not go through the gateway. Serve it **on the same host** as the gateway, so
the cookie set by `POST /sandbox/v1/jupyterlite/session` is sent:

* expose only the JupyterLite path on the sandbox's own Ingress (e.g. `/sandbox/jupyterlite`,
  no prefix strip), and
* set `API_JUPYTERLITE_BASE_URL` to that path, so the launch URL and the served path agree.
  If that Ingress strips a prefix, also set `API_JUPYTERLITE_SERVE_PATH` to the path the pod
  actually receives (e.g. public `/sandbox/jupyterlite`, served `/jupyterlite`).

### 3.5 Notebook sidecar

Point the worker template's `lifecycle.platformToken.sessionAPIBaseURL` at the gateway
(`https://<host>/<gateway-prefix>/sandbox`) in gateway mode. Rotation requests are
service-authenticated (§1), so they pass the gateway's `authentication: none` declaration and
reach the same check as before.

### 3.6 Checklist

1. `API_AUTH_MODE=gateway`, `WORKLOAD_VERIFIER_ENFORCEMENT=required`, JWKS/issuer set.
2. Gateway route as in §3.1, manifest mounted as in §3.2, Keycloak audience provisioned.
3. NetworkPolicy: admit the gateway's namespace only (plus the ingress controller for §3.4).
4. Smoke: `GET /sandbox/v1/health` → 200; `GET /sandbox/v1/notebook/list` with a user token → 200;
   the same request straight to the service without `X-DX-Workload` → 401.

---

## 4. Changing the API surface

Every operation is declared by swag annotations on its handler:

```go
// @ID           listNotebooks
// @x-dx-authz   {"authentication":"required","authorization":"identity"}
// @Router       /v1/notebook/list [get]
```

Then run `./scripts/generate-openapi.sh` (swag → `docs/swagger.*`, then `tools/openapi3` →
`openapi/openapi.yaml`). `cmd/api/openapi_manifest_test.go` compiles the manifest with the
gateway's own compiler and fails when a routed operation is undeclared, a declared one is not
routed in either booking mode, or a user operation is anything other than `required`/`identity`.
