# Sandbox Connect API — Configuration Reference

Field-level documentation for every configuration value consumed by the services in this
repository, following `CONFIG-DOC-TEMPLATE.md`.

## Template adaptation note

The source template targets Vert.x services configured by
`Charts/api-layer/v2/<service>/example-secrets/secrets/config.json`. **This repository has no
`config.json`.** Every service here is a Go binary configured by environment variables, loaded by
[`caarlos0/env/v11`](https://github.com/caarlos0/env) from struct tags in each service's
`types.go`, with an optional `.env` file overlay via `godotenv`.

The template maps onto that model as follows:

| Template concept | Equivalent here |
|---|---|
| `config.json` | `.env.all.example` (contributor reference) + `infra/**/configmap.yaml` and `secret.yaml` (deployed values) |
| Top-level key (`commonOptions`, `postgresOptions`, …) | Env var prefix (`API_`, `WORKER_`, `PROFILE_CREDIT_SYNC_`, `SLOT_LIFECYCLE_`) |
| Full JSON path (`postgresOptions.host`) | The env var name (`API_POSTGRES_URL`) |
| `modules` / `verticles` `required` array | The Go config struct that declares the tag, and the `,required` marker on it |
| Config schema version | Not versioned; the authoritative schema is the set of `env:"…"` struct tags in `cmd/*/types.go` |

Because the template asks for one document per service, there is one file per deployable binary:

| Service | Doc | Config struct | Deployed config |
|---|---|---|---|
| API server | [api.md](api.md) | `cmd/api/types.go` → `ApiEnv` | `infra/api/configmap.yaml`, `infra/api/secret.yaml` |
| Worker | [worker.md](worker.md) | `cmd/worker/types.go` → `Env` | `infra/worker/deployment.yaml`, `infra/worker/configmap.yaml` |
| Slot lifecycle cron | [slot-lifecycle.md](slot-lifecycle.md) | `cmd/cron/slot-lifecycle/types.go` | `infra/cron/slot-lifecycle/configmap.yaml` |
| Profile credit sync cron | [profile-credit-sync.md](profile-credit-sync.md) | `cmd/cron/profile-credit-sync/types.go` | `infra/cron/profile-credit-sync/configmap.yaml`, `secret.yaml` |
| Platform token sidecar | [platform-token-sidecar.md](platform-token-sidecar.md) | `cmd/platform-token-sidecar/main.go` (direct `os.Getenv`) | inlined in the worker notebook templates |

## Document header

| | |
|---|---|
| **Service** | sandbox-connect-api (5 binaries, one repo) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, branch `stable/v2.3` |
| **Config path in chart** | n/a — see the mapping table above |
| **Config schema version** | unversioned; authoritative source is `cmd/*/types.go` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-07-29 |

## How configuration is loaded

1. `godotenv.Load()` reads `.env` from the working directory if present. A missing file is logged
   at INFO and is not an error — this is why local runs work without any Kubernetes objects.
2. `env.Parse(&config)` populates the struct from the process environment. **Values already set in
   the environment win over `.env`.**
3. Any field tagged `,required` that is empty or unset aborts startup.

### The single most common failure mode

A missing or unparseable `,required` variable produces, on stderr, at startup:

```
failed to parse environment variables  error="env: required environment variable \"API_KEYCLOAK_REALM\" is not set"
```

followed by exit code 1. In Kubernetes this surfaces as the pod restarting into
`CrashLoopBackOff` with no HTTP listener ever bound. Type errors look the same but say
`strconv.ParseInt: parsing "abc": invalid syntax`. Individual field blocks only call out failure
modes that differ from this baseline.

## Cross-service fields

These values **must match** across services and/or external systems. Changing one without the
others is the most common cause of a broken deployment.

| Value | Appears as | Must equal |
|---|---|---|
| Postgres DSN | `API_POSTGRES_URL`, `WORKER_POSTGRES_URL`, `PROFILE_CREDIT_SYNC_POSTGRES_URL`, `SLOT_LIFECYCLE_POSTGRES_URL` | The same database. All four services read and write the same `notebooks` / `bookings` / `profiles` tables. |
| Keycloak base URL | `API_KEYCLOAK_URL`, `PROFILE_CREDIT_SYNC_KEYCLOAK_URL` | Same Keycloak, including the `/auth` suffix or its absence. |
| Keycloak realm | `API_KEYCLOAK_REALM`, `PROFILE_CREDIT_SYNC_KEYCLOAK_REALM` | Same realm. |
| Realm signing key | `API_KEYCLOAK_PUBLIC_KEY` | The RS256 public key of the realm named by `API_KEYCLOAK_REALM`. |
| Notebook delegation client | `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`, `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`, and `KEYCLOAK_CLIENT_ID` / `EXPECTED_CLIENT_ID` in the sidecar block of both notebook templates | One confidential client, default `sandbox-notebook`. |
| Sidecar readiness port | `API_PLATFORM_TOKEN_READY_PORT` and `READY_ADDRESS` + `ports.containerPort` in the notebook templates | Same port (default `8081`). |
| Demo-file init | `API_DISABLE_INIT` and `spec.lifecycle.demoFiles.enabled` in both notebook templates | Inverses of each other. `API_DISABLE_INIT=false` requires `demoFiles.enabled: true`. |
| Slot profile | `SLOT_CONFIG_PROFILE` on both the API and the slot-lifecycle cron | Same profile name. |
| Shared workspace | `WORKSPACE_ENABLED` on both the API and the worker | Same boolean. The API creates the `workspace` PVC; the worker mounts it. Disagreeing either way breaks notebooks or wastes 50 GiB per profile. |
| Registry pull secret | `API_REGISTRY_SECRET_NAME` and `spec…imagePullSecrets[].name` in both notebook templates | Same secret name, same namespace. |
