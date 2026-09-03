# Sandbox Connect API — Configuration Reference

Field-level documentation for every configuration value consumed by the services in this
repository, following `CONFIG-DOC-TEMPLATE.md`.

## Template adaptation note

The source template targets Vert.x services configured by
`Charts/api-layer/v2/<service>/example-secrets/secrets/config.json`. **This repository has no
`config.json`.** Every service here is a Go binary configured by environment variables. The API,
worker, and two background services use
[`caarlos0/env/v11`](https://github.com/caarlos0/env) struct tags plus an optional `.env` overlay
via `godotenv`; the platform-token sidecar reads its environment directly and does not load
`.env`.

The template maps onto that model as follows:

| Template concept | Equivalent here |
|---|---|
| `config.json` | `.env.all.example` (contributor reference) + `infra/**/configmap.yaml` and `secret.yaml` (deployed values) |
| Top-level key (`commonOptions`, `postgresOptions`, …) | Env var prefix (`API_`, `WORKER_`, `PROFILE_CREDIT_SYNC_`, `SLOT_LIFECYCLE_`) |
| Full JSON path (`postgresOptions.host`) | The env var name (`API_POSTGRES_URL`) |
| `modules` / `verticles` `required` array | The Go config struct that declares the tag, and the `,required` marker on it |
| Config schema version | Not versioned; the authoritative schema is the set of `env:"…"` struct tags plus direct environment reads in `cmd/platform-token-sidecar/main.go` |

Because the template asks for one document per service, there is one file per deployable binary:

| Service | Doc | Config struct | Deployed config |
|---|---|---|---|
| API server | [api.md](api.md) | `cmd/api/types.go` → `ApiEnv` | `infra/api/configmap.yaml`, `infra/api/secret.yaml` |
| Worker | [worker.md](worker.md) | `cmd/worker/types.go` → `Env` | `infra/worker/deployment.yaml`, `infra/worker/configmap.yaml` |
| Slot lifecycle controller | [slot-lifecycle.md](slot-lifecycle.md) | `cmd/cron/slot-lifecycle/types.go` | `infra/cron/slot-lifecycle/configmap.yaml` |
| Profile credit sync CronJob | [profile-credit-sync.md](profile-credit-sync.md) | `cmd/cron/profile-credit-sync/types.go` | `infra/cron/profile-credit-sync/configmap.yaml`, `secret.yaml` |
| Platform token sidecar | [platform-token-sidecar.md](platform-token-sidecar.md) | `cmd/platform-token-sidecar/main.go` (direct `os.Getenv`) | inlined in the worker notebook templates |

## Document header

| | |
|---|---|
| **Service** | sandbox-connect-api (5 binaries, one repo) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, reviewed on `feature/evaluation-argo-service` at `5ed2930` |
| **Config path in chart** | n/a — see the mapping table above |
| **Config schema version** | unversioned; authoritative sources are `cmd/*/types.go` and direct reads in `cmd/platform-token-sidecar/main.go` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-09-03 |

## Review basis and live-deployment drift

This reference was re-audited on 2026-09-03 against all `env:"..."` struct tags, direct
`os.Getenv` calls, checked-in Kubernetes manifests, and the live `sandbox` namespace in the
current dev EKS context. Secret **key names** were checked, but secret values were neither copied
nor recorded here.

The live workloads do not all run this source revision: the API image maps to commit `a0ffae4`
(2026-07-28), the worker to `e4283629` (2026-07-22), and slot lifecycle to `b63058a`
(2026-06-22). Therefore the source schema in this branch remains authoritative for the next
rollout; live-only keys may still be required by those older images.

Known drift at review time:

- `API_BLOCKED_EMAIL_DOMAINS` exists in current source and `.env.all.example`, but is absent from
  both the checked-in and live `api-config`. It consequently defaults to empty and domain blocking
  is disabled until operators add the key and restart the API.
- The live API configuration still contains legacy `API_AAA_URL`,
  `API_KEYCLOAK_BILLING_CLIENT_ID`, `API_OPENCOST_URL`, and Keycloak admin credential keys. Current
  source does not read them. Do not copy them into new deployments; remove them from the live
  objects only after the old API image has been replaced and validated.
- The checked-in and live notebook templates now set platform-token sidecar requests to
  `cpu: 10m`, `memory: 32Mi` and limits to `cpu: 100m`, `memory: 128Mi`; these fields are covered
  in the worker and sidecar references.
- `WORKSPACE_ENABLED` is absent from both live API and worker environments (effective value
  `false`), while both checked-in manifests set it to `true`. Before the next rollout, verify the
  shared RWX storage class and treat that rollout as enabling the feature.
- Both live and checked-in values currently disagree on the registry pull Secret name:
  `api-config` selects `v2-registry-cred`, while both notebook templates reference
  `registry-cred`. Reconcile them before creating notebooks from these templates. The live
  `api-config` also contains ECR and RabbitMQ credentials as plain ConfigMap data; rotate and move
  them to the API Secret as described in [api.md](api.md).
- `SLOT_LIFECYCLE_BATCH_SIZE` is absent from the live ConfigMap, so the code default of `50`
  applies.

## How configuration is loaded

The following loading sequence applies to the API, worker, slot lifecycle, and profile credit
sync. The sidecar's direct loading, explicit validation, and fallback rules are documented in
[platform-token-sidecar.md](platform-token-sidecar.md).

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
| [Notebook delegation client](api.md#canonical-keycloak-setup) | `API_PLATFORM_TOKEN_EXCHANGE_CLIENT_ID`, `API_PLATFORM_TOKEN_NOTEBOOK_CLIENT_ID`, and `KEYCLOAK_CLIENT_ID` / `EXPECTED_CLIENT_ID` in the sidecar block of both notebook templates | One confidential client, default `sandbox-notebook`; all four IDs must match. |
| Sidecar readiness port | `API_PLATFORM_TOKEN_READY_PORT` and `READY_ADDRESS` + `ports.containerPort` in the notebook templates | Same port (default `8081`). |
| Demo-file init | `API_DISABLE_INIT` and `spec.lifecycle.demoFiles.enabled` in both notebook templates | Inverses of each other. `API_DISABLE_INIT=false` requires `demoFiles.enabled: true`. |
| Slot profile | `SLOT_CONFIG_PROFILE` on both the API and slot-lifecycle controller | Same profile name. |
| Shared workspace | `WORKSPACE_ENABLED` on both the API and the worker | Same boolean. The API creates the `workspace` PVC; the worker mounts it. Disagreeing either way breaks notebooks or wastes 50 GiB per profile. |
| Registry pull secret | `API_REGISTRY_SECRET_NAME` and `spec…imagePullSecrets[].name` in both notebook templates | Same secret name, same namespace. |
