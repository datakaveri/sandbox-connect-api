# Platform token sidecar — configuration field reference

## 0. Document header

| | |
|---|---|
| **Service** | `platform-token-sidecar` (`cmd/platform-token-sidecar`) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, `stable/v2.3` |
| **Config source** | **not** a ConfigMap — inlined as `containers[].env` in both `SandboxNotebookTemplate` documents in `infra/worker/configmap.yaml` |
| **Config schema** | `cmd/platform-token-sidecar/main.go` — direct `os.Getenv`, no config struct |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-07-29 |

## 1. Top-level structure

This binary runs as a **sidecar inside every user notebook pod**, not as a cluster service. It
keeps a valid platform access token on a shared in-memory volume so the notebook can call platform
file APIs as its owner:

1. Waits for the API to project a Secret containing a refresh token and the client secret.
2. Exchanges the refresh token at Keycloak for an access token.
3. Writes it to `ACCESS_TOKEN_FILE` on a `medium: Memory` `emptyDir` shared with the notebook.
4. Serves readiness on `READY_ADDRESS`, which the API polls to decide the notebook is usable.
5. Re-refreshes `REFRESH_SKEW_SECONDS` before expiry, forever.

Two properties make its configuration different from every other service here:

- **It has no `,required` fields and no config struct.** `env.Parse` is not used. Every value is
  read with `os.Getenv`, so a missing or misspelled variable yields an empty string and the sidecar
  starts anyway. **There is no startup validation at all** — the baseline "fails fast" behaviour
  described in [README.md](README.md) does not apply. Misconfiguration surfaces as a notebook that
  never becomes ready.
- **Its values are per-notebook**, templated by the worker. Editing them means editing both
  notebook templates and restarting the worker.

Integer fields go through `durationFromEnv(name, fallback)`, which returns the fallback when the
value is unset, unparseable, **or ≤ 0**. A negative or zero interval is therefore silently
replaced by the default rather than rejected.

## 2. Field blocks

### `KEYCLOAK_TOKEN_URL`

- **Type / format:** string, absolute URL to the realm token endpoint.
- **Required:** effectively yes — delegation cannot work without it, but nothing enforces it.
- **Purpose:** the endpoint where the refresh token is exchanged for an access token.
- **Expected value:** `<keycloak>/realms/<realm>/protocol/openid-connect/token`
- **Example value:** `https://idp.example.org/auth/realms/tgdex/protocol/openid-connect/token`
- **Default if omitted:** `""` — every refresh fails; the notebook never becomes ready.
- **How to obtain:** the realm's `.well-known/openid-configuration` → `token_endpoint`. Must be
  built from the same Keycloak and realm as `API_KEYCLOAK_URL` / `API_KEYCLOAK_REALM`.
- **Failure mode:** empty or wrong → the sidecar logs a refresh error, readiness never flips, and
  the API culls the notebook after `API_PLATFORM_TOKEN_READY_TIMEOUT_SECS`. The user sees a
  notebook that starts and then dies for no stated reason — check the **sidecar** container's log,
  not the notebook's.
- **Change impact:** must move with `API_KEYCLOAK_URL`; edit both templates.
- **Notes / gotchas:** resolved from **inside a notebook pod**. A hostname that resolves on the
  API pod but is blocked by NetworkPolicy or absent from notebook-namespace DNS fails here while
  everything else looks healthy.

### `KEYCLOAK_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** effectively yes
- **Purpose:** the confidential client used for the refresh grant.
- **Expected value:** the same client as `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`.
- **Example value:** `sandbox-notebook`
- **Default if omitted:** `""` → `invalid_client` on every refresh.
- **How to obtain:** Keycloak operator; client requirements are in [api.md](api.md) §3.
- **Failure mode:** a mismatch with the client that **minted** the refresh token fails with
  `invalid_grant`, because refresh tokens are bound to the issuing client. The confusing part is
  that the API-side exchange succeeds and only the in-notebook refresh fails.
- **Change impact:** change together with `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID` and
  `EXPECTED_CLIENT_ID` below.
- **Notes / gotchas:** three places must agree; see the cross-service table in
  [README.md](README.md).

### `KEYCLOAK_CLIENT_SECRET_FILE`

- **Type / format:** string, absolute path to a file containing the client secret.
- **Required:** effectively yes
- **Purpose:** path to the mounted client secret. The secret is read **from a file**, never from an
  environment variable — deliberately, so it does not appear in `kubectl describe pod` or in the
  notebook's own environment.
- **Expected value:** a path inside the read-only `platform-refresh-token` Secret mount.
- **Example value:** `/var/run/sandbox-connect/refresh/client_secret`
- **Default if omitted:** `""` → the client authenticates with no secret and Keycloak returns
  `unauthorized_client`.
- **How to obtain:** the mount path of the `platform-refresh-token` volume plus the Secret key
  name. The Secret's content originates from `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_SECRET`.
- **Privileges required:** see [api.md](api.md) §3 for the client.
- **Failure mode:** a path that does not exist → refresh fails on every attempt. Because the volume
  is `optional: true`, a missing Secret does **not** fail the pod — the notebook starts, the file
  is simply absent, and only readiness fails.
- **Change impact:** must track the volume `mountPath` and the Secret's key names.
- **Notes / gotchas:** keep the file-based approach. Moving this secret into a plain env var would
  expose it to the user's own notebook process, which runs arbitrary user code in the same pod.

### `REFRESH_TOKEN_FILE`

- **Type / format:** string, absolute path.
- **Required:** effectively yes
- **Purpose:** the user's refresh token, projected by the API into the per-notebook Secret. This is
  the credential that makes the token *user-scoped* rather than service-scoped.
- **Expected value:** a path inside the `platform-refresh-token` mount.
- **Example value:** `/var/run/sandbox-connect/refresh/refresh_token`
- **Default if omitted:** `""`; `BOOTSTRAP_TOKEN_FILE` also loses its derived default (below).
- **How to obtain:** the volume `mountPath` plus the Secret key the API writes.
- **Failure mode:** absent → nothing to refresh, so readiness never flips. The sidecar **waits**
  rather than exiting, polling every `SECRET_WAIT_INTERVAL_SECONDS`, which is correct behaviour
  (the Secret is created asynchronously) but means a permanently missing Secret looks identical to
  a slow one.
- **Change impact:** must track the API's projection logic; not an independently choosable value.
- **Notes / gotchas:** mounted `readOnly` with `defaultMode: 256` (`0400`). Keep both — this file
  grants the bearer the user's platform identity.

### `BOOTSTRAP_TOKEN_FILE`

- **Type / format:** string, absolute path to a JSON file.
- **Required:** no
- **Purpose:** initial token material used before the first successful refresh.
- **Expected value:** normally `bootstrap.json` alongside the refresh token.
- **Example value:** `/var/run/sandbox-connect/refresh/bootstrap.json`
- **Default if omitted:** derived — `filepath.Join(filepath.Dir(REFRESH_TOKEN_FILE), "bootstrap.json")`.
  If `REFRESH_TOKEN_FILE` is also empty, the derivation yields an empty path.
- **How to obtain:** the same Secret mount.
- **Failure mode:** a wrong path delays first readiness until a full refresh completes rather than
  failing outright.
- **Change impact:** none if left unset and the derived default is acceptable.
- **Notes / gotchas:** the template sets it explicitly, which is harmless but redundant — the
  derived default produces the same path.

### `ACCESS_TOKEN_FILE`

- **Type / format:** string, absolute path.
- **Required:** no
- **Purpose:** where the refreshed access token is written for the notebook to read. **This is the
  sidecar's only output.**
- **Expected value:** a path on the shared `platform-token-cache` `emptyDir`, and the same value
  the notebook container's `MAHAAGX_TOKEN_FILE` points at.
- **Example value:** `/var/run/sandbox-connect/platform/token`
- **Default if omitted:** `/var/run/sandbox-connect/platform/token`.
- **How to obtain:** must equal `MAHAAGX_TOKEN_FILE` in the notebook container's env.
- **Failure mode:** if the two disagree, the sidecar becomes ready and the API marks the notebook
  usable, but the notebook reads a file that never appears — every platform file operation fails
  from inside an apparently healthy notebook. The most misleading failure in this component.
- **Change impact:** update `MAHAAGX_TOKEN_FILE` in the same template edit.
- **Notes / gotchas:** the volume is `medium: Memory`, so the token never touches disk. Keep it
  that way. The notebook mounts it `readOnly`; the sidecar does not.

### `STATUS_FILE`

- **Type / format:** string, absolute path to a JSON file.
- **Required:** no
- **Purpose:** the sidecar's own status/diagnostics output.
- **Expected value:** a path on the shared memory volume.
- **Example value:** `/var/run/sandbox-connect/platform/status.json`
- **Default if omitted:** derived — `status.json` in the same directory as `ACCESS_TOKEN_FILE`.
- **How to obtain:** n/a; the default is correct.
- **Failure mode:** an unwritable path loses diagnostics but does not stop token refresh.
- **Change impact:** none.
- **Notes / gotchas:** not set in the current templates, which is fine. Read this file first when
  debugging a notebook that will not become ready.

### `READY_ADDRESS`

- **Type / format:** string, `host:port` bind address.
- **Required:** no
- **Purpose:** address of the readiness endpoint the **API** polls to decide the notebook is usable.
- **Expected value:** `0.0.0.0:<port>` so it is reachable from outside the pod. The port must equal
  `API_PLATFORM_TOKEN_READY_PORT` and the `token-ready` `containerPort`.
- **Example value:** `0.0.0.0:8081`
- **Default if omitted:** `0.0.0.0:8081`.
- **How to obtain:** operator decision; changing it means editing three places plus any Istio
  policy.
- **Failure mode:** binding `127.0.0.1` makes the endpoint unreachable from the API, so **every**
  notebook is culled at the readiness timeout while the sidecar log shows successful refreshes —
  the single most confusing failure mode in the delegation path. A port mismatch with
  `API_PLATFORM_TOKEN_READY_PORT` behaves identically.
- **Change impact:** update `API_PLATFORM_TOKEN_READY_PORT`, both templates' `containerPort`, and
  `infra/istio/notebook-token-ready-authorizationpolicy.yaml` where Istio is enforced.
- **Notes / gotchas:** in a cluster with strict mTLS, the API's plain-HTTP probe to this port must
  be explicitly permitted — that is what `infra/istio/` is for. Without it Istio returns `403` on
  `/readyz` and every notebook times out at the readiness deadline. See `infra/istio/README.md`
  for why this must be mesh policy rather than an annotation on the notebook template.

### `TOKEN_SESSION_URL`

- **Type / format:** string, absolute URL, or empty.
- **Required:** no
- **Purpose:** endpoint used to validate the notebook session before refreshing, so a token stops
  being minted once the session ends.
- **Expected value:** derived from `spec.lifecycle.platformToken.sessionAPIBaseURL`, or empty to
  disable session validation.
- **Example value:** `""` (as deployed) or `https://api-sandbox.example.org/api/session`
- **Default if omitted:** `""` → **session validation is skipped**.
- **How to obtain:** the Sandbox Connect API's session endpoint.
- **Failure mode:** left empty, the sidecar keeps refreshing a valid user token for as long as the
  pod lives, regardless of whether the platform session was revoked. That is a security-relevant
  default, not a neutral one: revoking a user's session does not revoke their notebook's access.
  Set it wherever session revocation must propagate.
- **Change impact:** enabling it makes notebooks depend on the API's availability for continued
  token refresh.
- **Notes / gotchas:** empty in both templates today. Worth revisiting alongside
  `sessionAPIBaseURL`, which *is* set — the base URL is configured while the endpoint that uses it
  is not.

### `EXPECTED_USER_ID`

- **Type / format:** string, user identifier (Keycloak `sub`), or empty.
- **Required:** no
- **Purpose:** asserts the refreshed token belongs to the expected user — a guard against a Secret
  from one user's notebook being mounted into another's.
- **Expected value:** the notebook owner's user ID, templated per notebook; empty disables the
  check.
- **Example value:** `""` (as deployed)
- **Default if omitted:** `""` → **no user binding is verified**.
- **How to obtain:** set by the worker per notebook from the owning profile.
- **Failure mode:** empty means a misprojected Secret would be used without complaint, and the
  notebook would act as the wrong user. Populated, a mismatch fails the refresh and the notebook
  never becomes ready — a loud, safe failure.
- **Change impact:** enabling it requires the worker to template the value per notebook.
- **Notes / gotchas:** this is a defence-in-depth check that is currently switched off. Populating
  it is the cheapest hardening available in this component.

### `EXPECTED_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** no
- **Purpose:** asserts the refreshed token was issued to the expected client.
- **Expected value:** the same value as `KEYCLOAK_CLIENT_ID`.
- **Example value:** `sandbox-notebook`
- **Default if omitted:** `""` → check skipped.
- **How to obtain:** Keycloak operator.
- **Failure mode:** a mismatch fails refresh and the notebook never becomes ready — the intended
  behaviour.
- **Change impact:** change with `KEYCLOAK_CLIENT_ID` and
  `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`.
- **Notes / gotchas:** set in both templates; unlike `EXPECTED_USER_ID`, this check is active.

### Timing knobs

Five integers, all seconds, all via `durationFromEnv` — an unset, unparseable, zero or negative
value falls back to the default rather than erroring.

| Field | Purpose | Default | Example | Safe range | Too low | Too high |
|---|---|---|---|---|---|---|
| `REFRESH_SKEW_SECONDS` | refresh this long before expiry | `60` | `60` | 30–300 | tokens expire mid-request during clock skew or a slow IdP → intermittent 401s inside notebooks, hard to reproduce | needless refresh traffic; must stay below the token lifetime |
| `CHECK_INTERVAL_SECONDS` | how often expiry is re-evaluated | `30` | `30` | 15–60 | negligible cost | must be well below `REFRESH_SKEW_SECONDS`, or expiry is noticed too late to act on |
| `SECRET_WAIT_INTERVAL_SECONDS` | poll interval while waiting for the projected Secret | `1` | `1` | 1–5 | negligible | slows first readiness, risking the API's readiness timeout |
| `REFRESH_RETRY_INTERVAL_SECONDS` | backoff after a failed refresh | `5` | `5` | 5–30 | hammers Keycloak from every notebook at once during an IdP outage — a thundering herd that can keep the IdP down | slow recovery after a transient failure |
| `REQUEST_TIMEOUT_SECONDS` | HTTP timeout for Keycloak calls | `10` | not set | 10–30 | refreshes abort against a slow IdP | a hung call delays detection |

**Required:** none. **How to obtain:** operator decision, sized against the realm's access-token
lifetime — `REFRESH_SKEW_SECONDS` must be comfortably less than it, or every token is already
expired when minted. **Change impact:** none to data; applies to newly created notebooks only.
**Notes:** `REQUEST_TIMEOUT_SECONDS` is read by the code but set in neither template; it is the one
knob here with no explicit value.

---

## 3. Extra requirements by field category

### Credentials

The sidecar holds two, both **file-mounted, never environment variables**:

| Credential | Path | Origin | Sensitivity |
|---|---|---|---|
| Client secret | `KEYCLOAK_CLIENT_SECRET_FILE` | `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_SECRET`, projected by the API | authenticates the confidential client for *all* notebooks — the higher-value of the two |
| User refresh token | `REFRESH_TOKEN_FILE` | minted by the API's token exchange for this specific user | grants the bearer that user's platform identity until revoked |

Both arrive in the per-notebook Secret mounted at `platform-refresh-token`, `readOnly` with
`defaultMode: 256` (`0400`). The container additionally runs with
`readOnlyRootFilesystem: true` and `capabilities: drop: [ALL]`.

**Keep this shape.** The notebook container in the same pod runs arbitrary user code. The reason
these are files on a restricted mount rather than env vars is that env vars would be visible to
that user code and in `kubectl describe pod`. The user is *meant* to have their own access token
(via the shared memory volume); they are not meant to have the client secret or the raw refresh
token.

### Keycloak client IDs / secrets

The sidecar uses the `sandbox-notebook` confidential client documented in [api.md](api.md) §3.
Sidecar-specific requirements on that client:

- **Refresh token grant must be enabled** — the sidecar's entire loop depends on it.
- **Offline/long-lived refresh tokens must be permitted**, since a notebook may run far longer than
  the default refresh-token idle timeout. If the realm's *SSO Session Idle* or *Client Session
  Idle* is shorter than a typical notebook session, long-running notebooks lose platform access
  partway through — a failure that only appears under real usage patterns and never in a short
  test.
- The client must accept refresh requests from the notebook's network position.

### Domains / URLs

Every URL here is resolved **from inside a notebook pod**, which is often a different namespace and
network policy context than the API. Verify reachability from there, not from the API pod.

| Field | Scheme | Trailing slash | Must match |
|---|---|---|---|
| `KEYCLOAK_TOKEN_URL` | required | no | `API_KEYCLOAK_URL` + `API_KEYCLOAK_REALM` |
| `TOKEN_SESSION_URL` | required if set | no | `spec.lifecycle.platformToken.sessionAPIBaseURL` |
| `READY_ADDRESS` | none — `host:port` | n/a | `API_PLATFORM_TOKEN_READY_PORT`; `containerPort` |

### Tuning knobs

See the timing-knobs table above. The constraint that matters:

```
CHECK_INTERVAL_SECONDS  <  REFRESH_SKEW_SECONDS  <  access token lifetime
```

Violating either inequality produces intermittent, hard-to-reproduce 401s inside notebooks rather
than a clean failure.

### Feature flags

| Flag | Turns on | Becomes required as a result |
|---|---|---|
| `TOKEN_SESSION_URL` non-empty | session validation before each refresh | the API session endpoint must be reachable from notebook pods |
| `EXPECTED_USER_ID` non-empty | per-notebook user binding | the worker must template the owner's user ID |
| `EXPECTED_CLIENT_ID` non-empty | client binding check | must equal `KEYCLOAK_CLIENT_ID` |

The first two are empty today, so both checks are disabled. Neither is required for correct
operation, but both narrow the blast radius of a projection bug — worth enabling.

### External provider fields

None. The platform file API the notebook ultimately calls is configured on the **notebook**
container as `MAHAAGX_FILE_API_BASE_URL`, not on the sidecar — see [worker.md](worker.md).
