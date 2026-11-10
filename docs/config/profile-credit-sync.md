# Profile credit sync — configuration field reference

## 0. Document header

| | |
|---|---|
| **Service** | `profile-credit-sync` (`cmd/cron/profile-credit-sync`) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, reviewed on `feature/evaluation-argo-service` at `5ed2930` |
| **Config source** | `infra/cron/profile-credit-sync/configmap.yaml` + `secret.yaml` + Secret `database-creds` |
| **Config schema** | `cmd/cron/profile-credit-sync/types.go` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-09-03 |

Read [README.md](README.md) first for the loading order and baseline startup-failure mode.

## 1. Top-level structure

A Kubernetes `CronJob` running every minute (`*/1 * * * *`) with `concurrencyPolicy: Forbid` and
`restartPolicy: Never`. Each run queries OpenCost for notebook resource consumption, converts it
to credit usage, writes it to the sandbox database, and pushes the updated balance to the platform
AAA service.

Because it runs every minute and forbids concurrency, **a run that hangs blocks all subsequent
runs**. A missing timeout is therefore more consequential here than in a daily job.

All eight `,required` fields must be present or the job exits 1 immediately — visible as a
`CronJob` whose every Job lands in `Error`.

## 2. Field blocks

### `PROFILE_CREDIT_SYNC_POSTGRES_URL`

- **Type / format:** string, Postgres connection URI.
- **Required:** yes
- **Purpose:** DSN for reading profiles and writing computed credit balances.
- **Expected value:** `postgresql://<user>:<password>@<host>:<port>/<database>[?sslmode=…]`.
- **Example value:** `postgresql://sandbox_credits:REDACTED@postgres.sandbox.svc:5432/sandbox_db?sslmode=require`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** Secret `database-creds`, key `POSTGRES_URL`, shared with the other services.
- **Privileges required:** see §3 — this is the only component that **writes credit balances**.
- **Failure mode:** connection failure exits before any write, which is safe. A wrong database is
  the hazardous case: the job succeeds against an empty schema and credits are never updated,
  while the CronJob reports success every minute.
- **Change impact:** shared schema; coordinated cutover.
- **Notes / gotchas:** must be the same database as `API_POSTGRES_URL`.

### `PROFILE_CREDIT_SYNC_OPENCOST_URL`

- **Type / format:** string, absolute URL with scheme. In-cluster Service URL.
- **Required:** yes
- **Purpose:** the OpenCost allocation API queried for per-notebook CPU/GPU/memory consumption —
  the input to every credit calculation.
- **Expected value:** `http://<service>.<namespace>:<port>`, no trailing slash, no path.
- **Example value:** `http://opencost.opencost:9003`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** the OpenCost release's Service name, namespace and port
  (`kubectl -n opencost get svc`). Port 9003 is the OpenCost API default.
- **Failure mode:** unreachable → the run fails with a connection error and credits stall; because
  the job retries every minute, this recovers on its own once OpenCost returns. The subtler failure
  is OpenCost being **up but lacking history** — after an OpenCost restart it returns empty
  allocations, and a run over empty data can record zero usage rather than erroring.
- **Change impact:** none to data, but a gap in OpenCost availability is a gap in billing that is
  not automatically backfilled.
- **Notes / gotchas:** `http`, not `https` — OpenCost's in-cluster Service is plaintext. Must be
  reachable from the `sandbox` namespace; a default-deny NetworkPolicy will block it.

### `PROFILE_CREDIT_SYNC_AAA_URL`

- **Type / format:** string, absolute URL with scheme.
- **Required:** yes
- **Purpose:** base URL of the platform AAA (authentication/authorisation/accounting) service that
  holds the authoritative user credit balance.
- **Expected value:** scheme + host, no trailing slash, no path.
- **Example value:** `https://dx.tgdex.telangana.gov.in`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** the platform team that operates the DX/AAA deployment.
- **Failure mode:** wrong host → the local database is updated but the platform balance is not, so
  the sandbox and the platform disagree about the user's credits. Users see one figure in the
  sandbox UI and another on the platform — reported as a billing bug rather than a config error.
- **Change impact:** cross-system; coordinate with the AAA service owner.
- **Notes / gotchas:** the API's ConfigMap carried a dead `API_AAA_URL` key with the same value.
  That key has been removed — **this** is the only place the AAA URL is actually read.

### Keycloak service account

Four fields authenticate this job to Keycloak via the direct-grant (password) flow so it can call
AAA on behalf of a service identity. See §3 for the client and account requirements.

### `PROFILE_CREDIT_SYNC_KEYCLOAK_URL`

- **Type / format:** string, absolute URL with scheme; include `/auth` only if the Keycloak
  deployment uses that context path.
- **Required:** yes
- **Purpose:** base URL for obtaining the service-account token.
- **Expected value:** no trailing slash, no realm segment.
- **Example value:** `https://idp.tgdex.telangana.gov.in/auth`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** Keycloak operator. Verify with
  `<value>/realms/<realm>/.well-known/openid-configuration`.
- **Failure mode:** wrong context path 404s on the token endpoint every minute; credits stall
  entirely.
- **Change impact:** **must equal `API_KEYCLOAK_URL`.**
- **Notes / gotchas:** the `/auth` prefix trap described in [api.md](api.md) applies identically.

### `PROFILE_CREDIT_SYNC_KEYCLOAK_REALM`

- **Type / format:** string, realm name; case-sensitive.
- **Required:** yes
- **Purpose:** realm containing the service account.
- **Example value:** `tgdex`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** Keycloak operator.
- **Failure mode:** wrong realm → `realm does not exist` (404) or `invalid_grant` on every run.
- **Change impact:** must equal `API_KEYCLOAK_REALM`.
- **Notes / gotchas:** none beyond case sensitivity.

### `PROFILE_CREDIT_SYNC_KEYCLOAK_CLIENT_ID`

- **Type / format:** string, Keycloak client ID.
- **Required:** yes
- **Purpose:** the client through which the service account authenticates.
- **Expected value:** a client with the **direct access grant** flow enabled — see §3.
- **Example value:** `frontend-client`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** Keycloak operator.
- **Failure mode:** a client without direct grants enabled returns
  `unauthorized_client: Client not allowed for direct access grants` on every run.
- **Change impact:** coordinate with the Keycloak operator.
- **Notes / gotchas:** the deployed value (`frontend-client`) is a **public browser client being
  reused for machine-to-machine authentication**. It works, but it means the direct-grant flow must
  stay enabled on a client that end users also authenticate through, which is a weaker posture than
  a dedicated confidential client. See the recommendation in §3.

### `PROFILE_CREDIT_SYNC_KEYCLOAK_USERNAME` + `PROFILE_CREDIT_SYNC_KEYCLOAK_PASSWORD`

Documented as a pair — see §3.

- **Type / format:** strings.
- **Required:** yes (both)
- **Purpose:** credentials of the Keycloak **user** account the job authenticates as via the
  password grant.
- **Expected value:** a dedicated service user, not a person's account. **Secret — Kubernetes
  Secret only.**
- **Example value:** `svc-credit-sync` / `<generated password>`
- **Default if omitted:** none — the job exits 1.
- **How to obtain:** created in Keycloak by the realm administrator; stored in Secret
  `profile-credit-sync-keycloak-creds`.
- **Privileges required:** see §3.
- **Failure mode:** wrong credentials → `invalid_grant: Invalid user credentials` every minute,
  and credits stall. Two subtler cases fail the same way with different causes: the account having
  a **required action** pending (Update Password, Verify Email) blocks the grant until an
  administrator clears it, and **temporary password** set at creation does the same. Both are easy
  to miss because the account looks fine in the admin console.
- **Change impact:** rotating requires updating the Secret; the next scheduled run picks it up
  without a restart, since each run is a fresh pod.
- **Notes / gotchas:** the account must be exempt from any password-expiry policy in the realm, or
  credit sync breaks on the expiry date with no prior warning.

### `PROFILE_CREDIT_SYNC_GPU_RESOURCE_KEYS`

- **Type / format:** string, comma-separated Kubernetes extended-resource names.
- **Required:** no
- **Purpose:** which resource keys in the OpenCost response are counted as GPU usage for billing.
- **Expected value:** the extended-resource names the cluster's device plugins advertise.
- **Example value:** `nvidia.com/gpu`
- **Default if omitted:** `""` — **no resource key is recognised as a GPU, so GPU usage is billed
  as zero.**
- **How to obtain:** `kubectl get nodes -o json | jq '.items[].status.capacity'` and take the
  GPU-bearing keys.
- **Failure mode:** empty or wrong → the job succeeds, credits are written, and GPU consumption is
  simply absent from them. There is no error and no warning: the only symptom is users' GPU work
  costing nothing. This is the highest-impact silent failure in this service.
- **Change impact:** adding a second GPU vendor or an MIG/time-sliced resource name requires
  adding its key here, or that hardware bills as free.
- **Notes / gotchas:** must match `API_DEFAULT_GPU_TYPE` for consistency between what the API
  requests and what this job bills. AMD (`amd.com/gpu`), MIG profiles
  (`nvidia.com/mig-1g.5gb`) and time-sliced variants are all distinct keys.

### `PROFILE_CREDIT_SYNC_LOG_LEVEL`

- **Type / format:** string enum, `debug` | `info` | `warn` | `error`.
- **Required:** no
- **Purpose:** log verbosity; also read via `os.Getenv` before config parsing.
- **Expected value:** `info` in production.
- **Example value:** `info`
- **Default if omitted:** `info`.
- **How to obtain:** operator decision.
- **Failure mode:** none. An unrecognised value silently falls back.
- **Change impact:** none.
- **Notes / gotchas:** the deployed value is `debug`. At one run per minute this produces a
  continuous debug stream; consider `info` unless actively debugging.

### `PROFILE_CREDIT_SYNC_K8S_CONFIG_MODE` / `PROFILE_CREDIT_SYNC_K8S_CONFIG_PATH`

Same semantics as elsewhere — see [api.md](api.md).

- **Required:** no. **Note:** unlike the other services, `…_K8S_CONFIG_MODE` has **no
  `envDefault`**, so an unset value is `""` rather than `cluster`.
- **Expected value:** set it explicitly to `cluster`; the ConfigMap does.
- **Privileges required:** read-only access to pods and notebooks for attributing usage.
- **Failure mode:** leaving it unset relies on how the empty string is handled downstream rather
  than on the documented `cluster` default the other services provide — set it explicitly.

### Removed field

**`PROFILE_CREDIT_SYNC_MAX_PROFILE_CAN_SYNC_AT_ONCE` — deprecated, read by no code.** It appeared
in `.env.all.example` but no batching limit is implemented; every run processes all profiles. It
has been removed from the example file. If per-run batching becomes necessary as the profile count
grows, it must be implemented in code first.

---

## 3. Extra requirements by field category

### Credentials

#### Postgres — `PROFILE_CREDIT_SYNC_POSTGRES_URL`

Same database as the API and worker. This is the only component that writes credit balances, so
give it a dedicated role — it makes credit mutations attributable and lets you revoke this job's
write access without touching the API:

```sql
CREATE USER sandbox_credits WITH PASSWORD '<generated>';
GRANT CONNECT ON DATABASE sandbox_db TO sandbox_credits;
GRANT USAGE ON SCHEMA public TO sandbox_credits;
GRANT SELECT ON profiles, notebooks, bookings TO sandbox_credits;
GRANT UPDATE ON profiles TO sandbox_credits;
GRANT SELECT, INSERT ON credit_usage TO sandbox_credits;
```

Adjust table names to the current schema in `db.sql`. Created by the DBA/DevOps owner of
`database-creds`.

#### Keycloak service user — `…_KEYCLOAK_USERNAME` / `…_KEYCLOAK_PASSWORD`

The account lives in the realm named by `PROFILE_CREDIT_SYNC_KEYCLOAK_REALM`. Required setup, all
of which the realm administrator performs:

| Setting | Value | Why |
|---|---|---|
| Username | `svc-credit-sync` (dedicated, not a person) | a person's account breaks when they leave |
| Email verified | **on** | an unverified email blocks the direct grant |
| Required actions | **none** | any pending action (Update Password, Verify Email) fails the grant |
| Temporary password | **off** | a temporary password forces a reset on first use and the grant fails |
| Password policy | exempt from expiry | expiry silently breaks credit sync on a future date |
| Realm roles | only what AAA requires to post a balance | |
| Client roles | as required by the AAA service | ask the AAA owner for the exact role |
| Groups | none unless AAA authorises by group | |

**Do not grant `realm-management` roles** such as `manage-users` or `view-clients` — this job reads
cost data and posts balances; it has no reason to administer the realm.

Store both fields in Secret `profile-credit-sync-keycloak-creds`. Coordinate creation with DevOps.

### Keycloak client — `PROFILE_CREDIT_SYNC_KEYCLOAK_CLIENT_ID`

| | |
|---|---|
| **Naming convention** | currently the shared `frontend-client`; a dedicated `sandbox-credit-sync` is recommended |
| **Client type** | public today (no client secret field exists in this service) |
| **Flows enabled** | **Direct access grants must be on** — that is the flow this job uses. Standard flow as needed by whatever else uses the client |
| **Service accounts** | not used — this job authenticates as a *user*, not via client credentials |
| **Service-account role mappings** | n/a for the same reason |
| **Scopes** | whatever AAA requires in the access token |
| **Audience mappers** | add one for the AAA service if AAA validates audience |
| **Redirect URIs** | inherited from the client's browser use; irrelevant to this job |

Relation to other fields: this client is in the same realm as `API_KEYCLOAK_REALM` but is **not**
required to be the same client as `API_KEYCLOAK_CLIENT_ID`, and is unrelated to the
`sandbox-notebook` token-exchange client.

**Recommendation.** Reusing a public browser client for a machine account means direct access
grants must remain enabled on a client that end users log in through, which widens the surface for
password-guessing against real user accounts. A dedicated **confidential** client using the
client-credentials grant would remove both the shared client and the stored user password. That is
a code change — this service has no client-secret field — so it is a backlog item, not a config
fix.

### Domains / URLs

| Field | Scheme | Trailing slash | Reach | Must match |
|---|---|---|---|---|
| `PROFILE_CREDIT_SYNC_OPENCOST_URL` | `http://` | no | **internal only** | the OpenCost Service |
| `PROFILE_CREDIT_SYNC_AAA_URL` | `https://` | no | public | the platform AAA deployment |
| `PROFILE_CREDIT_SYNC_KEYCLOAK_URL` | `https://` | no | public | `API_KEYCLOAK_URL` **verbatim** |

### Tuning knobs

This service has no tuning knobs of its own — no batch size, no timeout, no concurrency setting.
Its pacing comes entirely from the CronJob:

| Knob | Where | Safe range | Notes |
|---|---|---|---|
| `schedule` | `cronjob.yaml` | `*/1` to `*/15` | more frequent means fresher balances and more OpenCost load |
| `concurrencyPolicy` | `cronjob.yaml` | `Forbid` | **keep `Forbid`** — concurrent runs would double-count usage |
| `restartPolicy` | `cronjob.yaml` | `Never` | a failed run is retried by the next schedule tick |

Because there is no run timeout in code and `concurrencyPolicy: Forbid`, a run that hangs on a
slow OpenCost or AAA call blocks every later run until it is killed. Add
`activeDeadlineSeconds` to the Job spec (a value below the schedule interval, e.g. `50`) if that
risk matters in your environment — it is not set today.

### Feature flags

None.

### External provider fields

`PROFILE_CREDIT_SYNC_AAA_URL` points at the platform DX/AAA service, which is operated by the
platform team rather than by this project. Onboarding requirements: the service account above must
be provisioned in the shared realm, and the AAA service must accept balance updates from it. Dev
and production use different AAA hosts — confirm the correct one with the platform team rather than
inferring it from the Keycloak hostname, as the two do not always share a domain.
