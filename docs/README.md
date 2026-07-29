# Sandbox Connect documentation

This directory is the entry point for maintained system documentation.

## Start here

| Topic | Document |
|---|---|
| Components, data flow, lifecycle, storage, and security boundaries | [Architecture](architecture.md) |
| Build, deployment, verification, and troubleshooting | [Operations](operations.md) |
| Environment variables and cross-service configuration | [Configuration reference](config/README.md) |
| Worker Notebook template contract | [Worker template reference](../infra/worker/README.md) |
| Cluster dependencies and infrastructure bootstrap | [Infrastructure setup](../infra/README.md) |
| Istio policy for notebook token readiness | [Istio notes](../infra/istio/README.md) |

## API reference

The API serves generated ReDoc documentation at `/v1/apis/`.

The generated artifacts are:

- [OpenAPI YAML](swagger.yaml)
- [OpenAPI JSON](swagger.json)
- `docs.go`, consumed by the API binary

Regenerate all three after changing route annotations or API types:

```bash
./scripts/generate-openapi.sh
```

## Component-specific references

- [API configuration](config/api.md)
- [Worker configuration](config/worker.md)
- [Slot lifecycle configuration](config/slot-lifecycle.md)
- [Profile credit sync configuration](config/profile-credit-sync.md)
- [Platform token sidecar configuration](config/platform-token-sidecar.md)

Configuration values belong in the component references above. System behavior belongs in
`architecture.md`, and operational procedures belong in `operations.md`. Keeping those
responsibilities separate avoids copying environment-variable tables or deployment commands into
multiple files.
