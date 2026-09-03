# Worker — configuration field reference

## 0. Document header

| | |
|---|---|
| **Service** | `sandbox-worker` (`cmd/worker`) |
| **Code repo / branch** | `github.com/datakaveri/sandbox-connect-api`, reviewed on `feature/evaluation-argo-service` at `5ed2930` |
| **Config source** | `infra/worker/deployment.yaml` (inline env + Secrets `database-creds`, `s3-creds`), `infra/worker/configmap.yaml` (notebook templates) |
| **Config schema** | `cmd/worker/types.go` → `Env`; template schema in `cmd/worker/workload_template.go` |
| **Maintainer / point of contact** | Sandbox Connect backend team |
| **Last updated** | 2026-09-03 |

Read [README.md](README.md) first for the loading order and baseline startup-failure mode.

## 1. Top-level structure

The worker reconciles notebook rows in Postgres into Kubeflow `Notebook` objects and PVCs. Its
configuration has **two distinct halves**, and confusing them is the most common source of error:

| Half | Where | What it controls |
|---|---|---|
| Process env (11 vars) | `deployment.yaml` env + Secrets | how the worker connects to Postgres, S3 and Kubernetes, and where it reads its templates |
| `SandboxNotebookTemplate` YAML | ConfigMap `sandbox-worker-notebook-templates`, mounted at `/etc/sandbox-worker/templates` | **everything about the notebooks themselves** — images, volumes, node affinity, security context, the token sidecar |

> **Deprecated in this revision.** Fourteen `WORKER_*` variables previously in `.env.all.example`
> (`WORKER_CPU_NOTEBOOK_IMAGE`, `WORKER_STORAGE_CLASS_NAME`, `WORKER_GPU_NODE_INSTANCE_TYPES`,
> `WORKER_IMAGE_PULL_ENABLED`, the `WORKER_PLATFORM_*` group, …) are read by **no code**. They are
> pre-template leftovers; their settings now live in the template YAML. They have been removed from
> the example file. If you are looking for "where do I set the notebook image", the answer is
> `spec.notebook.spec.template.spec.containers[0].image` in the template, not an env var.

## 2. Field blocks — process environment

### `WORKER_CPU_NOTEBOOK_TEMPLATE_PATH`

- **Type / format:** string, absolute path to a YAML file readable by the process.
- **Required:** yes
- **Purpose:** the `SandboxNotebookTemplate` loaded at startup by
  `LoadSandboxNotebookTemplate(path, "cpu")` and used as the blueprint for every CPU notebook.
- **Expected value:** a path inside the mounted ConfigMap volume.
- **Example value:** `/etc/sandbox-worker/templates/cpu-notebook-template.yaml`
- **Default if omitted:** none — startup fails.
- **How to obtain:** the `mountPath` of the `notebook-templates` volume in `deployment.yaml`,
  joined with the ConfigMap key.
- **Failure mode:** a missing file fails startup with
  `open /etc/…: no such file or directory` — most often because the ConfigMap key was renamed
  without updating this path. Malformed YAML fails with a parse error at the same point. Both are
  clean startup failures, so this is one of the safer fields to get wrong.
- **Change impact:** template changes apply only to **newly created** notebooks; existing ones keep
  the spec they were created with.
- **Notes / gotchas:** this and its GPU counterpart were **missing from `.env.all.example`** before
  this revision, so a worker configured from the example file could never start. ConfigMap updates
  propagate to the mounted file within about a minute, but the worker reads templates **once at
  startup** — restart the deployment after editing.

### `WORKER_GPU_NOTEBOOK_TEMPLATE_PATH`

As above, loaded with kind `"gpu"` and used for GPU notebooks.

- **Example value:** `/etc/sandbox-worker/templates/gpu-notebook-template.yaml`
- **Required:** yes; **Default if omitted:** none — startup fails.
- **Notes / gotchas:** the GPU template additionally sets `spec.lifecycle.instanceTypeOverride`,
  which the CPU template omits. Do not point both variables at the same file.

### `WORKER_POSTGRES_URL`

- **Type / format:** string, Postgres connection URI.
- **Required:** yes
- **Purpose:** DSN for the pool the worker polls for pending notebook work.
- **Expected value:** `postgresql://<user>:<password>@<host>:<port>/<database>[?sslmode=…]`.
- **Example value:** `postgresql://sandbox_worker:REDACTED@postgres.sandbox.svc:5432/sandbox_db?sslmode=require`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `database-creds`, key `POSTGRES_URL` — the **same** Secret and key the
  API uses. Managed outside this repository.
- **Privileges required:** see §3.
- **Failure mode:** as for the API. Pointing the worker at a *different* database than the API is
  the dangerous case: both start cleanly, but notebooks created via the API are never reconciled
  and simply stay pending forever.
- **Change impact:** shared schema with the API, slot lifecycle, and profile credit sync — any host
  change is a coordinated
  cutover.
- **Notes / gotchas:** must be the same database as `API_POSTGRES_URL`.

### `WORKER_MAX_CONCURRENT_WORKER`

- **Type / format:** int, count of goroutines.
- **Required:** yes
- **Purpose:** how many notebook reconciliations run in parallel.
- **Expected value:** 3–10. Size against Postgres connection headroom and the Kubernetes API
  server's client-side rate limit, not against CPU — the work is I/O-bound.
- **Example value:** `5`
- **Default if omitted:** none — startup fails.
- **How to obtain:** operator decision. With multiple worker replicas the effective concurrency is
  this value × replica count; size the total, not the per-pod figure.
- **Failure mode:** too low → notebook creation queues visibly, users wait minutes for a notebook
  that has not started provisioning. Too high → Kubernetes API client-side throttling
  (`Waited for … due to client-side throttling`) which *slows everything down* rather than
  erroring, and can exhaust the Postgres pool.
- **Change impact:** none to data.
- **Notes / gotchas:** `0` is accepted by `env.Parse` because the field is only `,required`, not
  validated. A worker with `0` starts, logs normally, and processes nothing — it looks healthy.

### `WORKSPACE_ENABLED`

- **Type / format:** bool.
- **Required:** no
- **Purpose:** controls whether the `shared-workspace` volume survives template selection. When
  false, `SelectWorkloadTemplate` in `cmd/worker/volume_policy.go` adds `shared-workspace` to the
  worker's omitted-volumes set, so the volume and its mount are stripped from the notebook spec.
  When true, the notebook mounts PVC `workspace` read-only at `/home/jovyan/workspace`.
- **Expected value:** the **same value as the API's `WORKSPACE_ENABLED`**.
- **Example value:** `false`
- **Default if omitted:** `false`.
- **How to obtain:** not an independent decision — copy the API's value.
- **Failure mode:** disagreeing with the API is the whole hazard, and the two directions fail
  differently:
  - worker `true`, API `false` → the notebook spec keeps a `shared-workspace` volume backed by PVC
    `workspace`, which the API never creates. The template's `existing` volume policy expects a
    bound `ReadWriteMany` claim, so notebooks stall instead of starting.
  - worker `false`, API `true` → PVCs are created per profile and never mounted; users see no
    workspace and 50 GiB per profile is wasted.
- **Change impact:** applies to newly created notebooks only, since templates are resolved per
  notebook at creation.
- **Notes / gotchas:** unprefixed and shared with the API — set both from one source of truth. The
  worker only *omits or keeps* the volume; it never creates the PVC. That is the API's job.

### `WORKER_KUBE_CONFIG_MODE` / `WORKER_KUBE_CONFIG_PATH`

Identical semantics to the API's `API_KUBE_CONFIG_MODE` / `API_KUBE_CONFIG_PATH` — see
[api.md](api.md).

- **Required:** no; **Defaults:** `cluster` and `""`.
- **Privileges required:** the worker's ServiceAccount needs **write** access to
  `notebooks.kubeflow.org`, PVCs, and Secrets in user namespaces — a broader grant than the API's.
  See `infra/rbac.yaml`.
- **Failure mode:** insufficient RBAC fails at first reconciliation, not at startup:
  `notebooks.kubeflow.org is forbidden: User "system:serviceaccount:sandbox:…" cannot create`.
  Notebooks stay pending with the error visible only in the worker log.

### S3 template storage

Five fields, all `,required`, all sourced from Secret `s3-creds`. They locate the object store
holding user-supplied notebook files and templates that the init container copies onto the PVC.

### `WORKER_S3_ENDPOINT`

- **Type / format:** string, absolute URL **with scheme**.
- **Required:** yes
- **Purpose:** S3 API endpoint.
- **Expected value:** `https://s3.<region>.amazonaws.com` for AWS, or the MinIO/Ceph RGW endpoint
  on-prem. No trailing slash, no bucket in the path.
- **Example value:** `https://s3.ap-south-1.amazonaws.com`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `s3-creds`, key `S3_ENDPOINT`; from the storage operator.
- **Failure mode:** an endpoint that disagrees with `WORKER_S3_REGION` fails signature validation
  with `SignatureDoesNotMatch` — which reads like a credential problem but is an endpoint problem.
- **Change impact:** none to data.
- **Notes / gotchas:** on-prem S3 usually needs path-style addressing; if a MinIO endpoint returns
  `NoSuchBucket` for a bucket that exists, virtual-host addressing is the cause.

### `WORKER_S3_REGION`

- **Type / format:** string, region ID.
- **Required:** yes
- **Purpose:** region used for request signing.
- **Expected value:** the bucket's region; `us-east-1` is the conventional placeholder for
  S3-compatible stores that have no regions.
- **Example value:** `ap-south-1`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `s3-creds`, key `S3_REGION`.
- **Failure mode:** wrong region → `AuthorizationHeaderMalformed … expecting 'ap-south-1'`, or a
  301 redirect the SDK does not follow.
- **Change impact:** none.
- **Notes / gotchas:** must match the endpoint.

### `WORKER_S3_ACCESS_KEY` + `WORKER_S3_SECRET_KEY`

Documented as a pair — see §3.

- **Type / format:** strings.
- **Required:** yes (both)
- **Purpose:** credentials for reading template objects.
- **Expected value:** a dedicated, read-only account. **Secret — Kubernetes Secret only.**
- **Example value:** `AKIAIOSFODNN7EXAMPLE` / `<40-char secret>`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `s3-creds`, keys `S3_ACCESS_KEY` / `S3_SECRET_KEY`; created by the
  storage operator.
- **Privileges required:** see §3.
- **Failure mode:** wrong credentials → `InvalidAccessKeyId` or `SignatureDoesNotMatch` on first
  fetch. Valid credentials without bucket permission → `AccessDenied`, and the notebook is created
  but its workspace is empty — a partial failure that users report as "my files are missing"
  rather than as an error.
- **Change impact:** rotate in the Secret and restart the worker.
- **Notes / gotchas:** unlike the API's registry credentials these are **not** copied into user
  namespaces, so they are only exposed to the worker pod.

### `WORKER_S3_TEMPLATE_BUCKET_NAME`

- **Type / format:** string, bucket name only — no scheme, no `s3://`, no path.
- **Required:** yes
- **Purpose:** bucket holding notebook templates and user-supplied files.
- **Expected value:** an existing bucket in `WORKER_S3_REGION`.
- **Example value:** `notebook-templates`
- **Default if omitted:** none — startup fails.
- **How to obtain:** Secret `s3-creds`, key `S3_TEMPLATE_BUCKET_NAME`.
- **Failure mode:** a non-existent bucket → `NoSuchBucket` at first fetch, not at startup, so the
  worker appears healthy until the first notebook is created.
- **Change impact:** the new bucket must already contain the template objects, or every subsequent
  notebook launches with an empty workspace.
- **Notes / gotchas:** writing `s3://notebook-templates` here produces a confusing
  `InvalidBucketName`.

---

## 2b. Field blocks — `SandboxNotebookTemplate`

Both templates live in ConfigMap `sandbox-worker-notebook-templates`. Each document is a
`SandboxNotebookTemplate` followed by a PVC document. `{pvcName}` and `{storageSize}` are the only
placeholders substituted by the worker.

### `spec.lifecycle.externalPVCWaitTimeout`

- **Type / format:** Go duration string (`120s`, `2m`).
- **Required:** no
- **Purpose:** how long to wait for an externally managed PVC to reach `Bound` before failing.
- **Expected value:** longer than the storage provisioner's worst-case bind time.
- **Example value:** `120s`
- **Default if omitted:** the code default; set it explicitly.
- **How to obtain:** measure PVC bind latency on the target StorageClass.
- **Failure mode:** too low on a slow provisioner marks healthy notebooks failed during a burst.
- **Notes / gotchas:** `WaitForFirstConsumer` StorageClasses do not bind until the pod is
  scheduled, so this must exceed scheduling time too.

### `spec.lifecycle.workspaceVolumeName`

- **Type / format:** string; must name a volume in `spec.notebook…volumes`.
- **Required:** yes in practice
- **Purpose:** identifies which volume is the user's persistent workspace.
- **Example value:** `user-data`
- **Failure mode:** naming a volume that does not exist means the workspace is never mounted and
  user files vanish between restarts — data loss with no error.
- **Notes / gotchas:** must match both the volume name and the PVC document's `metadata.name`.

### `spec.lifecycle.volumePolicies[]`

- **Type / format:** array of `{name, required?, managed?{retentionPolicy}, existing?{waitForBound, expected}}`.
- **Required:** no
- **Purpose:** declares, per volume, whether the worker creates it (`managed`) or waits for
  something else to (`existing`), and what happens on notebook deletion.
- **Expected value:** `retentionPolicy: DeleteWithNotebook` or `Retain`.
- **Example value:** `- name: user-data` / `managed: {retentionPolicy: DeleteWithNotebook}`
- **How to obtain:** data-retention policy decision — this is a **product/compliance** choice.
- **Failure mode:** `DeleteWithNotebook` **permanently deletes the user's workspace PVC** when the
  notebook is deleted. If users expect their files to survive, this silently destroys data. `Retain`
  is the safe default; it leaks PVCs that must be reclaimed manually.
- **Change impact:** affects newly created notebooks only.
- **Notes / gotchas:** the single highest-consequence field in either template. Confirm the
  intended retention behaviour with the data owner before changing it.

### `spec.lifecycle.instanceTypeOverride`

- **Type / format:** `{selectorKey: string, required: bool}`.
- **Required:** no — present in the GPU template only.
- **Purpose:** lets a notebook row's `instance_type` override node selection, keyed on
  `selectorKey`.
- **Example value:** `{selectorKey: node.kubernetes.io/instance-type, required: false}`
- **Failure mode:** `required: true` with a value matching no node leaves the notebook `Pending`
  indefinitely; `false` falls back to the template's affinity.
- **Notes / gotchas:** `selectorKey` must be the label key actually applied to nodes.

### `spec.lifecycle.runtimeInjection.image`

- **Type / format:** string, container image reference.
- **Required:** yes when runtime injection is used
- **Purpose:** the image used to clone a user-supplied Git repository into the workspace.
- **Expected value:** an image containing `git`, pinned to a tag or digest.
- **Example value:** `alpine/git:2.45.2`
- **Failure mode:** unpullable → the init container fails and the notebook never starts.
- **Notes / gotchas:** must be pullable with `imagePullSecrets` — in an air-gapped cluster, mirror
  it; `alpine/git` is a Docker Hub image and subject to anonymous pull rate limits.

### `spec.lifecycle.demoFiles.enabled` + `.initImage`

- **Type / format:** bool; container image reference.
- **Required:** no
- **Purpose:** seeds demonstration notebooks into a new workspace via an init container.
- **Expected value:** `enabled: true` with an `initImage` that writes the file named by
  `API_STARTUP_NOTEBOOK_FILENAME`.
- **Example value:** `true` / `registry.example.com/cbr/test-notebook-file:v1.2-b42d188`
- **How to obtain:** the team that builds the demo-content image.
- **Failure mode:** **must be the inverse of `API_DISABLE_INIT`.** With `enabled: false` and
  `API_DISABLE_INIT=false`, the API generates a URL to a file that was never created and users get
  a JupyterLab 404 inside an otherwise working notebook.
- **Change impact:** cross-service — update the API ConfigMap in the same change.
- **Notes / gotchas:** documented from the API side in [api.md](api.md) under `API_DISABLE_INIT`.

### `spec.lifecycle.platformToken.sessionAPIBaseURL`

- **Type / format:** string, absolute URL with scheme.
- **Required:** conditional — required when the token sidecar is present.
- **Purpose:** base URL used to construct the per-notebook API endpoint where the sidecar persists
  rotated refresh tokens.
- **Expected value:** the Sandbox Connect API's public base URL, including any path prefix.
- **Example value:** `https://api-sandbox.example.org/api`
- **Failure mode:** empty → template validation fails. Unreachable → rotated refresh tokens cannot
  be persisted and long-running notebooks eventually lose platform access.
- **Change impact:** must track the API's ingress hostname.
- **Notes / gotchas:** resolved from **inside** a notebook pod — if network policy blocks egress
  to the public ingress, an internal Service DNS name is required instead.

### `spec.notebook.spec.template.spec.containers[name=platform-token-sidecar].resources.requests` / `.limits`

- **Type / format:** Kubernetes resource maps. `cpu` is a CPU quantity such as `10m`; `memory` is
  a binary memory quantity such as `32Mi`.
- **Required:** operationally yes in both CPU and GPU templates; Kubernetes itself permits them to
  be omitted unless a namespace policy requires them.
- **Purpose:** reserves enough CPU and memory for token refresh/readiness while bounding a broken
  sidecar's consumption. These values also determine pod scheduling and QoS classification.
- **Expected value:** current baseline requests `cpu: 10m`, `memory: 32Mi`; limits `cpu: 100m`,
  `memory: 128Mi`. Keep requests no greater than their corresponding limits.
- **Example value:** `requests: {cpu: 10m, memory: 32Mi}` and
  `limits: {cpu: 100m, memory: 128Mi}`.
- **Default if omitted:** none in this repository. A namespace `LimitRange` may inject a default;
  otherwise the sidecar has no reservation or container-level ceiling for the omitted resource.
- **How to obtain:** begin with the checked-in/live baseline, then size from sidecar CPU and
  working-set metrics during simultaneous notebook startup and Keycloak recovery bursts.
- **Failure mode:** an undersized CPU limit causes throttling and delayed readiness/token refresh;
  an undersized memory limit produces `OOMKilled`. Excessive requests make notebooks remain
  `Pending` with `Insufficient cpu` or `Insufficient memory` even though the notebook container
  itself would fit.
- **Change impact:** affects only notebooks created after the worker reloads the edited templates;
  existing pods retain their resource settings.
- **Notes / gotchas:** this block was added to both templates in August 2026. Keep the CPU and GPU
  copies identical unless measurements demonstrate different sidecar workloads. See
  [platform-token-sidecar.md](platform-token-sidecar.md#container-resource-requests-and-limits).

### `spec.notebook`

The embedded Kubeflow `Notebook` spec, passed to Kubernetes largely verbatim. The fields that most
often need environment-specific attention:

| Path | Purpose | Failure mode if wrong |
|---|---|---|
| `…containers[0].image` | the notebook image | `ImagePullBackOff`; this is where the notebook image is set — **not** `WORKER_CPU_NOTEBOOK_IMAGE`, which is dead |
| `…imagePullSecrets[].name` | pull secret name | must equal `API_REGISTRY_SECRET_NAME`, or pulls fail unauthenticated |
| `…affinity.nodeAffinity…values` | which nodes may host notebooks | no match → `Pending` with `didn't match Pod's node affinity/selector` |
| `…securityContext.runAsUser` / `fsGroup` | file ownership on the workspace | a mismatch with the image's user makes the workspace read-only to the user, reported as "I can't save my notebook" |
| `…securityContext.supplementalGroups` | access to shared NFS exports | wrong GID → permission denied on the mounted dataset only |
| `volumes[].nfs.server` / `.path` | shared dataset mounts | wrong path hangs the mount and the pod stalls in `ContainerCreating` |
| `volumes[] platform-refresh-token.secretName` | set per-notebook by the worker at creation | must remain `""` with `optional: true` in the template; hard-coding a name breaks delegation |
| `containers[].env MAHAAGX_FILE_API_BASE_URL` | file API the notebook talks to | wrong host → file operations fail inside the notebook only |
| `…containers[1] platform-token-sidecar` | the sidecar block | see [platform-token-sidecar.md](platform-token-sidecar.md) |
| `…containers[1].resources.requests/limits` | sidecar CPU/memory reservation and ceiling | too low → throttling or `OOMKilled`; too high → unschedulable notebook pod |
| PVC doc `spec.storageClassName` | workspace storage class | non-existent class → PVC stays `Pending`, notebook never starts. **This is where storage class is set — `WORKER_STORAGE_CLASS_NAME` is dead** |
| PVC doc `spec.accessModes` | `ReadWriteOnce` vs `ReadWriteMany` | `RWO` restricts the notebook to one node; a class that cannot satisfy the mode leaves the PVC `Pending` |

---

## 3. Extra requirements by field category

### Credentials

#### Postgres — `WORKER_POSTGRES_URL`

Same database as the API. The worker needs the same DML grants — see the SQL in
[api.md](api.md) §3. It may share the API's role or use a separate role with identical grants;
separate roles are preferable so that per-role connection limits and audit trails distinguish the
two.

#### S3 — `WORKER_S3_ACCESS_KEY` / `WORKER_S3_SECRET_KEY`

Read-only on the template bucket. For AWS:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::notebook-templates" },
    { "Effect": "Allow", "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::notebook-templates/*" }
  ]
}
```

For MinIO, the equivalent `readonly` policy scoped to the bucket. No `s3:PutObject` or
`s3:DeleteObject` — the worker only reads. Created by the storage operator, stored in Secret
`s3-creds`.

### Keycloak client IDs / secrets

The worker holds none directly. The notebook token sidecar it injects does — its client
requirements are documented in the API reference's
[canonical Keycloak setup](api.md#canonical-keycloak-setup), and its runtime fields are documented
in [platform-token-sidecar.md](platform-token-sidecar.md).

The worker's responsibility is wiring, not client administration. In both CPU and GPU templates
it must:

- set sidecar `KEYCLOAK_CLIENT_ID` and `EXPECTED_CLIENT_ID` to the same value as the API's two
  notebook-client ID fields;
- set `KEYCLOAK_TOKEN_URL` to the token endpoint for the API authentication realm;
- retain empty `TOKEN_SESSION_URL` and `EXPECTED_USER_ID` placeholders for the worker to replace
  per notebook; and
- mount the API-projected client secret and refresh token only into the sidecar.

### Domains / URLs

| Field | Scheme | Trailing slash | Reach | Must match |
|---|---|---|---|---|
| `WORKER_S3_ENDPOINT` | required | no | internal or public | `WORKER_S3_REGION` |
| `spec.lifecycle.platformToken.sessionAPIBaseURL` | required | no | reachable **from a notebook pod** | the API ingress host |
| `MAHAAGX_FILE_API_BASE_URL` (template env) | required | no | reachable from a notebook pod | the platform file API |
| sidecar `KEYCLOAK_TOKEN_URL` (template env) | required | no | reachable from a notebook pod | `API_KEYCLOAK_URL` + realm |

### Tuning knobs

| Field | Safe range | Size against | Too low | Too high |
|---|---|---|---|---|
| `WORKER_MAX_CONCURRENT_WORKER` | 3–10 per replica | Kubernetes API rate limit; Postgres connections | visible provisioning queue | client-side throttling, pool exhaustion |
| `externalPVCWaitTimeout` | 60s–5m | provisioner bind latency | healthy notebooks marked failed | failures detected slowly |

### Feature flags

| Flag | Turns on | Becomes required as a result |
|---|---|---|
| `demoFiles.enabled: true` | demo-file seeding init container | `demoFiles.initImage`; `API_DISABLE_INIT=false` on the API |
| `instanceTypeOverride.required: true` | strict node-type pinning from the DB | node labels matching every `instance_type` value in use |
| `WORKSPACE_ENABLED=true` | keeps the `shared-workspace` volume in the notebook spec | the same value on the API, which creates the PVC; a `ceph-filesystem` RWX class |
| `volumePolicies[].managed.retentionPolicy` | workspace PVC lifecycle | a deliberate data-retention decision — see the block above |

### External provider fields

None.
