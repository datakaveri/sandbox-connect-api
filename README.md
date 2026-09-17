# Sandbox Connect API

Sandbox Connect manages CPU and GPU Jupyter notebooks on Kubernetes through a REST API,
scheduled bookings, and background controllers. This README describes the latest stable
branch, **`stable/v2.3`**.

## Components

| Component | Source | Responsibility |
|---|---|---|
| API | `cmd/api` | Authentication, profiles, bookings, notebook lifecycle, and JupyterLite sessions |
| Worker | `cmd/worker` | Claims pending notebook work and creates Kubeflow Notebooks and managed PVCs |
| Slot lifecycle | `cmd/cron/slot-lifecycle` | Long-running controller for booking transitions and timed cleanup |
| Profile credit sync | `cmd/cron/profile-credit-sync` | Scheduled usage and credit synchronization |
| Platform token sidecar | `cmd/platform-token-sidecar` | Refreshes delegated notebook credentials and reports readiness |

PostgreSQL coordinates state. Kubernetes and Kubeflow host notebook workloads. CPU and GPU
workload configuration lives in the worker's versioned Notebook templates.

## Operating modes

`API_BOOKINGS_ENABLED=true` is the default. Users select a category and available slots, then
create a booking through `POST /v1/bookings`. The slot-lifecycle controller must run, and its
`SLOT_CONFIG_PROFILE` must match the API's setting.

With `API_BOOKINGS_ENABLED=false`, users create, start, stop, and delete notebooks through the
direct `/v1/notebook` lifecycle routes. Booking and slot discovery are disabled. Notebook
listing/status, profile creation, and JupyterLite sessions are shared across modes.

Import the matching [Postman collection and example environment](postman/README.md).

## Requirements

- Go 1.24.2 or a compatible newer toolchain, as declared in `go.mod`.
- PostgreSQL; local Compose uses PostgreSQL 16.
- Access to Kubernetes with the Kubeflow Notebook and Profile CRDs, suitable storage, and
  permissions for the configured service accounts.
- Keycloak configured for API authentication and any enabled notebook token exchange.
- CPU/GPU notebook images and the registry credentials required to pull them.
- S3-compatible storage and worker credentials; other integrations depend on enabled features.
- Docker Compose for the local database example, and `kubectl` for cluster access.

Docker Compose starts **only PostgreSQL**. It does not install Kubernetes, Kubeflow, Keycloak,
or the external integrations. See the [infrastructure guide](infra/README.md) for dependencies.

## Local development

### 1. Check out the stable branch

```bash
git clone --branch stable/v2.3 https://github.com/datakaveri/sandbox-connect-api.git
cd sandbox-connect-api
cp .env.all.example .env
```

### 2. Configure your environment

Edit `.env` using the [configuration reference](docs/config/README.md). Replace every required
placeholder for the components you will run. The Go services load `.env` from the repository
root; it is a dotenv file, so do not execute it with `source`.

For a local cluster connection, set `API_KUBE_CONFIG_MODE=local`,
`WORKER_KUBE_CONFIG_MODE=local`, and `SLOT_LIFECYCLE_K8S_CONFIG_MODE=local`, with their matching
`*_PATH` variables pointing to your kubeconfig. Set profile credit sync's Kubernetes mode/path
similarly if you run it locally.

Set `POSTGRES_PASSWORD` to a local development password. Set all four component PostgreSQL
URLs to the same database, using user `postgres`, that password, host `localhost`, port `5432`,
and database `postgres` for this Compose example. URL-encode special characters in the password.
The local database has no TLS, so use `sslmode=disable` only for this local connection; retain
TLS for remote deployments.

Configure worker templates for your cluster's images, storage, scheduling, file service, and
Keycloak endpoints. For local runs, export the already-configured template ConfigMap:

```bash
mkdir -p .cache/notebook-templates
kubectl get configmap sandbox-worker-notebook-templates -n sandbox \
  -o jsonpath='{.data.cpu-notebook-template\.yaml}' \
  > .cache/notebook-templates/cpu-notebook-template.yaml
kubectl get configmap sandbox-worker-notebook-templates -n sandbox \
  -o jsonpath='{.data.gpu-notebook-template\.yaml}' \
  > .cache/notebook-templates/gpu-notebook-template.yaml
```

Set `WORKER_CPU_NOTEBOOK_TEMPLATE_PATH=.cache/notebook-templates/cpu-notebook-template.yaml`
and `WORKER_GPU_NOTEBOOK_TEMPLATE_PATH=.cache/notebook-templates/gpu-notebook-template.yaml`
in `.env`. Both templates are required and validated at worker startup. See the
[template contract](infra/worker/README.md).

### 3. Start PostgreSQL and initialize the schema

```bash
docker compose up -d --wait
docker compose exec -T postgres psql -U postgres -d postgres < db.sql
```

The schema command is for initial setup. Review database changes before applying them to an
existing deployment. PostgreSQL is exposed only on the local loopback interface.

### 4. Run the services

Run each long-running component in a separate terminal from the repository root:

```bash
go run ./cmd/api
go run ./cmd/worker
# Required for booking mode:
go run ./cmd/cron/slot-lifecycle
```

Profile credit sync runs once and exits; run it separately when its integrations are configured:

```bash
go run ./cmd/cron/profile-credit-sync
```

With the example API address, check health and open the API reference:

```bash
curl --fail http://localhost:3000/v1/health
# API reference: http://localhost:3000/v1/apis/
```

To serve JupyterLite locally, build its static assets with `./scripts/build-jupyterlite.sh` and
set `API_JUPYTERLITE_STATIC_DIR=jupyterlite` in `.env`. The script uses Python 3.9–3.12 or Docker.
The API Dockerfile builds and bundles those assets automatically.

## Deployment

Use the [operations guide](docs/operations.md) for the database, Secrets, ConfigMaps, RBAC,
workloads, ingress, build commands, and verification sequence. Files under `infra/` are
deployment templates: configure them for your environment before applying them. Keep filled
manifests and private overlays outside tracked files, and deploy matching worker images and
Notebook templates together.

`Jenkinsfile` defines multi-service CI/CD; automatic deployment currently targets `dev`.
The GitHub workflows publish API and worker images on manual dispatch. Review pipeline
configuration before enabling it for your environment.

## Documentation and validation

| Topic | Reference |
|---|---|
| Maintained documentation index | [docs/README.md](docs/README.md) |
| Architecture, lifecycle, storage, and token sessions | [docs/architecture.md](docs/architecture.md) |
| Component configuration and shared constraints | [docs/config/README.md](docs/config/README.md) |
| Deployment and troubleshooting | [docs/operations.md](docs/operations.md) |
| User tutorials and documentation site | [user-docs/README.md](user-docs/README.md) |
| Mode-specific API examples | [postman/README.md](postman/README.md) |
| OpenAPI specifications | [YAML](docs/swagger.yaml), [JSON](docs/swagger.json) |
| Public-release audit and outstanding findings | [PUBLIC_RELEASE_AUDIT.md](PUBLIC_RELEASE_AUDIT.md) |

The API serves ReDoc at `/v1/apis/`. After changing API annotations or response types, install
the Swag version declared in `go.mod`, then regenerate the OpenAPI artifacts:

```bash
go install github.com/swaggo/swag/cmd/swag@v1.16.4
./scripts/generate-openapi.sh
go test ./...
```

The user documentation site synchronizes the generated OpenAPI files before start/build.
Design drafts and dated cluster inventories under `docs/` provide historical context; verify
them against stable source and your environment before using them as deployment instructions.

## Credentials

Never commit real passwords, access/refresh tokens, private keys, kubeconfigs, or filled Secret
manifests. `.env.all.example` and committed Postman environments contain examples only.
Store credentials in local ignored files or your organization's secret-management system.

The current-tree scan does not clear the repository's history for publication. Historical
credential findings and the scope of the audit are recorded in
[PUBLIC_RELEASE_AUDIT.md](PUBLIC_RELEASE_AUDIT.md).

## License

[View License](./LICENSE)

Third-party dependency license information is listed in [dep-licenses](dep-licenses).
