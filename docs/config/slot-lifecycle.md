# Slot lifecycle — configuration field reference

## 0. Document header

| | |
|---|---|
| **Service** | `slot-lifecycle` (`cmd/cron/slot-lifecycle`) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, reviewed on `feature/evaluation-argo-service` at `5ed2930` |
| **Config source** | `infra/cron/slot-lifecycle/configmap.yaml` (`slot-lifecycle-config`) + Secret `database-creds` |
| **Config schema** | `cmd/cron/slot-lifecycle/types.go` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-09-03 |

Read [README.md](README.md) first for the loading order and baseline startup-failure mode.

## 1. Top-level structure

Despite living under `cmd/cron/`, this is **not** a CronJob — it is deployed as a long-running
`Deployment` that ticks internally every `SLOT_LIFECYCLE_TICK_INTERVAL_SECS`. It advances booking
slots through their lifecycle (due → active → expired) and stops notebooks whose slot has ended.

All configuration comes from ConfigMap `slot-lifecycle-config` via `envFrom`, except
`SLOT_LIFECYCLE_POSTGRES_URL`, projected from Secret `database-creds` key `POSTGRES_URL`.

Without this component running, bookings are accepted by the API but never transition state.

## 2. Field blocks

### `SLOT_LIFECYCLE_POSTGRES_URL`

- **Type / format:** string, Postgres connection URI.
- **Required:** yes
- **Purpose:** DSN for the pool used to read due bookings and write slot state transitions.
- **Expected value:** `postgresql://<user>:<password>@<host>:<port>/<database>[?sslmode=…]`.
- **Example value:** `postgresql://sandbox_slots:REDACTED@postgres.sandbox.svc:5432/sandbox_db?sslmode=require`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `database-creds`, key `POSTGRES_URL` — the same Secret the API and
  worker use. Managed outside this repository.
- **Privileges required:** see §3.
- **Failure mode:** standard connection errors at startup. The dangerous case is pointing at a
  *different* database from the API: both start cleanly and bookings simply never advance.
- **Change impact:** shared schema; coordinated cutover with the API, worker, and profile credit sync.
- **Notes / gotchas:** the example file previously showed `?sslmode=disable` with a `local`
  kubeconfig — developer values, not deployment values. Use `require` or stronger against a
  production database.

### `SLOT_CONFIG_PROFILE`

- **Type / format:** string, profile name defined in `pkg/gpuconfig`.
- **Required:** no
- **Purpose:** selects the code-defined slot/category configuration — slot boundaries, durations
  and categories — that this component enforces.
- **Expected value:** `production`, the only profile defined in code.
- **Example value:** `production`
- **Default if omitted:** `production`. Note this differs from the API, where the same variable
  defaults to `""` and then falls back through the legacy name.
- **How to obtain:** read the profile names in `pkg/gpuconfig`.
- **Failure mode:** **must equal the API's `SLOT_CONFIG_PROFILE`.** If the two disagree, the API
  offers users one set of slot boundaries while this component expires them on another — bookings
  appear to end early or late, with no error logged anywhere. An unknown name means no slot
  configuration matches and nothing is ever transitioned.
- **Change impact:** cross-service; change both ConfigMaps together.
- **Notes / gotchas:** deliberately unprefixed because it is shared with the API. The differing
  defaults between the two services are a genuine trap — set it explicitly in both.

### `GPU_SLOT_CONFIG_PROFILE` — **deprecated**

- **Type / format:** string.
- **Required:** no
- **Purpose:** legacy name for `SLOT_CONFIG_PROFILE`, consulted only when the latter is empty.
- **Expected value:** unset. Migrate to `SLOT_CONFIG_PROFILE`.
- **Default if omitted:** `""`.
- **How to obtain:** n/a.
- **Failure mode:** setting both with different values silently prefers `SLOT_CONFIG_PROFILE`.
- **Change impact:** none.
- **Notes / gotchas:** the API's equivalent legacy variable has a *different* name
  (`API_GPU_SLOT_CONFIG_PROFILE`). Remove both once no environment sets them.

### `SLOT_LIFECYCLE_TICK_INTERVAL_SECS`

- **Type / format:** int, seconds.
- **Required:** no
- **Purpose:** how often the reconciliation loop runs.
- **Expected value:** 5–60 s. The practical floor is set by how promptly a slot must end — a tick
  interval of `N` means a booking can overrun by up to `N` seconds.
- **Example value:** `5`
- **Default if omitted:** `5`.
- **How to obtain:** operator decision, driven by the acceptable overrun on a paid GPU slot.
- **Failure mode:** too high delays both slot activation and notebook shutdown, so users are billed
  for capacity past the end of their slot. Too low adds constant database load for no benefit;
  with a value at or below `SLOT_LIFECYCLE_RUN_TIMEOUT_SECS`, ticks can overlap.
- **Change impact:** none to data.
- **Notes / gotchas:** keep `TICK_INTERVAL_SECS` well below `RUN_TIMEOUT_SECS` so a slow run cannot
  be started again before the previous one finishes.

### `SLOT_LIFECYCLE_RUN_TIMEOUT_SECS`

- **Type / format:** int, seconds.
- **Required:** no
- **Purpose:** upper bound on a single reconciliation run; the run's context is cancelled at this
  deadline.
- **Expected value:** long enough to process `SLOT_LIFECYCLE_BATCH_SIZE` bookings including the
  Kubernetes calls to stop notebooks. 45–120 s.
- **Example value:** `45`
- **Default if omitted:** `45`.
- **How to obtain:** measure a full batch run against the target cluster.
- **Failure mode:** too low → runs are cancelled mid-batch with `context deadline exceeded`,
  leaving some bookings unprocessed each tick. If the backlog grows faster than the surviving
  portion of each run clears it, slots stop expiring altogether — a silent, compounding failure.
- **Change impact:** none to data; partial runs are re-attempted on the next tick.
- **Notes / gotchas:** raise this and `BATCH_SIZE` together when clearing a backlog.

### `SLOT_LIFECYCLE_BATCH_SIZE`

- **Type / format:** int, rows per run.
- **Required:** no
- **Purpose:** maximum bookings processed in one reconciliation run.
- **Expected value:** 50–500, sized against peak concurrent bookings per tick.
- **Example value:** `50`
- **Default if omitted:** `50`.
- **How to obtain:** operator decision, informed by peak booking volume.
- **Failure mode:** too small and the component cannot keep up at peak — a backlog forms and slot
  expiry drifts progressively later, which reads as a clock problem rather than a throughput one.
  Too large and a run exceeds `RUN_TIMEOUT_SECS` and is cancelled mid-batch.
- **Change impact:** none to data.
- **Notes / gotchas:** was missing from `.env.all.example` before this revision, along with the two
  interval fields above.

### `SLOT_LIFECYCLE_LOG_LEVEL`

- **Type / format:** string enum, `debug` | `info` | `warn` | `error`.
- **Required:** no
- **Purpose:** log verbosity. Read via `os.Getenv`, not through the config struct.
- **Expected value:** `info` in production.
- **Example value:** `info`
- **Default if omitted:** `info`.
- **How to obtain:** operator decision.
- **Failure mode:** none. An unrecognised value falls back to the default silently rather than
  erroring — a typo here is invisible.
- **Change impact:** none; restart required.
- **Notes / gotchas:** the deployed value is `info`, unlike the API's ConfigMap which ships
  `debug`.

### `SLOT_LIFECYCLE_LOG_FORMAT`

- **Type / format:** string enum, `json` | `text`.
- **Required:** no
- **Purpose:** selects the `slog` handler — structured JSON or human-readable text.
- **Expected value:** `json` in any environment with log aggregation.
- **Example value:** `json`
- **Default if omitted:** `json`.
- **How to obtain:** determined by the log pipeline.
- **Failure mode:** `text` in a cluster whose collector expects JSON produces log lines that are
  ingested as unparsed strings, so field-based queries and alerts silently return nothing.
- **Change impact:** none.
- **Notes / gotchas:** the example file previously showed `text` — a developer convenience that
  should not be copied into a deployment. Not currently set in `slot-lifecycle-config`, so it
  correctly defaults to `json`.

### `SLOT_LIFECYCLE_K8S_CONFIG_MODE` / `SLOT_LIFECYCLE_K8S_CONFIG_PATH`

Identical semantics to the API's equivalents — see [api.md](api.md).

- **Required:** no; **Defaults:** `cluster` and `""`.
- **Purpose:** the Kubernetes client used to stop notebooks whose slot has expired.
- **Privileges required:** permission to get, list and delete/patch `notebooks.kubeflow.org` in
  user namespaces. See `infra/rbac.yaml`.
- **Failure mode:** insufficient RBAC fails at the first expiry, not at startup: bookings are
  marked expired in the database while the notebook keeps running, so the user retains GPU capacity
  they are no longer booked for. Check the log for `notebooks.kubeflow.org is forbidden` whenever
  billing and actual usage disagree.
- **Notes / gotchas:** the example file previously shipped `local` with a hard-coded developer
  kubeconfig path; both must be `cluster` and empty in a deployment.

---

## 3. Extra requirements by field category

### Credentials

#### Postgres — `SLOT_LIFECYCLE_POSTGRES_URL`

Same database as the API and worker. Needs `SELECT` and `UPDATE` on the bookings and notebooks
tables; it does not insert or delete. The shared grants in [api.md](api.md) §3 cover it. A
dedicated role is preferable so that slot transitions are attributable in the audit trail:

```sql
CREATE USER sandbox_slots WITH PASSWORD '<generated>';
GRANT CONNECT ON DATABASE sandbox_db TO sandbox_slots;
GRANT USAGE ON SCHEMA public TO sandbox_slots;
GRANT SELECT, UPDATE ON bookings, notebooks TO sandbox_slots;
```

Created by the DBA/DevOps owner of `database-creds`.

### Keycloak client IDs / secrets

None — this component does not talk to Keycloak.

### Domains / URLs

None.

### Tuning knobs

| Field | Safe range | Size against | Too low | Too high |
|---|---|---|---|---|
| `SLOT_LIFECYCLE_TICK_INTERVAL_SECS` | 5–60 | acceptable slot overrun | needless DB load; overlapping ticks | users billed past slot end |
| `SLOT_LIFECYCLE_RUN_TIMEOUT_SECS` | 45–120 | time to process one full batch | runs cancelled mid-batch, growing backlog | slow runs detected late |
| `SLOT_LIFECYCLE_BATCH_SIZE` | 50–500 | peak bookings per tick | backlog, drifting expiry | run exceeds the timeout |

The three interact: `BATCH_SIZE` must be processable within `RUN_TIMEOUT_SECS`, which should
comfortably exceed `TICK_INTERVAL_SECS`. Change them as a set.

### Feature flags

None. `SLOT_CONFIG_PROFILE` selects a profile rather than toggling behaviour, but note that this
component is only meaningful when the API runs with `API_BOOKINGS_ENABLED=true`.

### External provider fields

None.
