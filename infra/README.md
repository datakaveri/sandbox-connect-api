# Sandbox Connect infrastructure

This directory contains deployment templates for `stable/v2.3`. Configure hostnames, images,
storage, identity-provider settings, and credentials for your environment before applying them.
The [operations guide](../docs/operations.md) is the deployment procedure for Sandbox Connect.

## Cluster dependencies

Provision these dependencies before deploying the services:

- Kubernetes with Kubeflow Notebook (`kubeflow.org/v1beta1`) and Profile CRDs/controllers.
- Service accounts and reviewed RBAC for API, worker, and background-controller access.
- A writable StorageClass for managed notebook PVCs. The example templates use `ceph-block`;
  shared workspaces require an appropriate RWX class when enabled.
- Any existing PVCs and read-only NFS exports referenced by your Notebook templates.
- Keycloak, a browser client for API authentication, and the confidential notebook client when
  delegated downloads are enabled.
- PostgreSQL reachable by all database-backed services.
- S3-compatible template storage and scoped worker credentials.
- Registries and pull credentials for service, notebook, helper, and sidecar images.
- Ingress, DNS, and TLS for your public endpoints; Istio when required by your Kubeflow setup.
- OpenCost and the platform credit service when profile-credit synchronization is enabled.
  Configure OpenCost against your cluster's Prometheus service and review resource sizing.

Select and pin upstream releases appropriate to your cluster. This repository does not provide
a complete cluster installer. The preserved [Kubeflow upgrade dossier](../docs/called-kubeflow-upgrade-docs/README.md)
includes bootstrap, identity integration, and migration material for a dated environment; review
its assumptions and version pins before adapting it. Cluster removal must follow the runbook
for the release and resources actually installed.

## Deployment files

| Component | Files |
|---|---|
| API | `api/Dockerfile`, `api/configmap.yaml`, `api/secret.yaml`, `api/manifest.yaml` |
| Worker | `worker/Dockerfile`, `worker/configmap.yaml`, `worker/deployment.yaml` |
| Slot lifecycle Deployment | `cron/slot-lifecycle/` |
| Profile credit sync CronJob | `cron/profile-credit-sync/` |
| Notebook token sidecar | `platform-token-sidecar/Dockerfile`, `platform-token-sidecar/sandbox-notebook-client.json` |
| Permissions | `rbac.yaml` |
| Notebook token readiness policy | [istio/README.md](istio/README.md) |
| User documentation site | `userdocs/`; build instructions in [user-docs/README.md](../user-docs/README.md) |

`api/ingress.yaml` routes to the API; `api/ingress-kubeflow.yaml` routes to the Istio gateway.
For the user documentation site, choose `userdocs/ingress.yaml` when the proxy strips the
`/user-docs` prefix, or `userdocs/ingress-new.yaml` when the origin serves that prefix. The two
user-docs ingress files have the same resource name and are alternatives.

## Configuration and Secrets

Use the [configuration reference](../docs/config/README.md) and
[worker template contract](worker/README.md). Replace all `example.com` endpoints and explicit
placeholders, including Notebook images, NFS servers/export paths, and the realm public key.
Public keys are not secrets, but must match your own realm.

Create environment-managed `database-creds`, `s3-creds`, and registry pull Secrets before the
workloads. The tracked Secret files are templates only. Create filled copies in a private,
ignored overlay or through your secret-management system, rather than editing credentials into
tracked files. Keep registry pull Secret names consistent between API settings and both worker
templates. Service image pull credentials may use a separate Secret.

The Keycloak client import contains `CHANGE_ME_AFTER_IMPORT`; generate a new client secret
after import and supply it through `api-creds`. Follow the
[canonical Keycloak configuration](../docs/config/api.md#canonical-keycloak-setup).

Deploy matching worker images and Notebook templates together. Review the resource names,
permissions, storage policies, and integration settings rather than applying the directory
recursively.
