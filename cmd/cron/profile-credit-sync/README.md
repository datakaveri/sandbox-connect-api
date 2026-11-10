# Profile credit sync

This component synchronizes Kubeflow profile usage and credits using OpenCost, the platform
credit service, Keycloak, and PostgreSQL. It runs once and exits.

From the repository root, configure `.env` and run:

```bash
go run ./cmd/cron/profile-credit-sync
```

See the [configuration reference](../../../docs/config/profile-credit-sync.md) for required
settings and the [operations guide](../../../docs/operations.md) for deployment.

The Kubernetes [CronJob](../../../infra/cron/profile-credit-sync/cronjob.yaml) runs every minute
with `concurrencyPolicy: Forbid`. Configure credentials and an immutable image tag for your
environment before deployment.
