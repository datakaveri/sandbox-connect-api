# Kubeflow blue-green upgrade dossier

Inventory date: **2026-08-10 UTC**
Source context: `arn:aws:eks:ap-south-1:135663523795:cluster/dev-eks-cluster`
Target release: **Kubeflow Community Distribution 26.03.1** at commit
`f09f3eeaa25cc852665f460497a42b7fc68639ac`.

This dossier is intended to be cloned onto the machine that can access the new
test cluster. It describes a blue-green migration from the existing Kubeflow
1.10 notebook platform. It does not authorize or require an in-place change to
the live cluster.

## Executive decision

Use the upstream `release-26.03.1` tag and install the supported **notebook
platform slice**, not the full `example` stack. The target slice consists of:

- Kubeflow namespaces and aggregate roles.
- Kubeflow's Istio 1.30.1, including Istio CNI.
- OAuth2 Proxy 7.15.2 and Dex 2.45.1 connected to the existing Keycloak realm.
- Dashboard 2.0.0 (Dashboard, Profiles/KFAM, and PodDefaults webhook).
- Notebooks v1 1.11.0 (Notebook Controller, Jupyter UI, PVC Viewer, Volumes UI,
  Tensorboard Controller, and Tensorboards UI).
- The Sandbox Connect API, worker, lifecycle controller, credit sync, custom
  RBAC, and the mesh-wide token-readiness policy.

Do not enable Pipelines, KServe, Knative, Katib, Trainer, Spark, or Hub merely
to call this an upgrade. None is present in the live platform. The complete
upstream example requests about 4.38 CPU, 12.3 GiB memory, and 65 GB platform
storage; it also introduces data stores and migration work that this service
does not currently need.

## Read this first

1. [Current-state inventory](01-current-state-inventory.md)
2. [Risk and gap register](02-risk-and-gap-register.md)
3. [Target design and version pin](03-target-design.md)
4. [Implementation runbook](04-implementation-runbook.md)
5. [Data migration and rollback](05-data-migration-and-rollback.md)
6. [Acceptance test plan](06-acceptance-test-plan.md)
7. [Security remediation](07-security-remediation.md)
8. [Operations command reference](08-command-reference.md)

The scripts are deliberately non-secret:

- `scripts/collect-live-inventory.sh` creates a fresh, private inventory bundle
  without Secret or ConfigMap values.
- `scripts/preflight-new-cluster.sh` checks the target before installation.
- `scripts/validate-install.sh` performs read-only post-install checks.
- `overlay-template/` is copied into a pinned upstream checkout and provides a
  minimal component graph plus the live culling/readiness customizations.

## Stop/go gates

Do not start target installation until all of the following are true:

- The target `kubectl` context is recorded and is not the live ARN above.
- The target Kubernetes version is 1.35 or 1.36. Release 26.03.1 CI covers
  Kubernetes 1.36, and its prerelease covered 1.35.
- Every target worker node is `linux/amd64`. Upstream warns that ARM64 image
  coverage is incomplete. The live workers are all AMD64.
- Cert-manager ownership is decided. On live EKS it is an AWS-managed add-on;
  do not also apply the upstream cert-manager base.
- The enforced `require-resource-limits` Kyverno policy is either absent during
  bootstrap or has a reviewed exception/overlay. Unmodified upstream Kubeflow
  deployments omit CPU and memory limits and will be rejected by that policy.
- A real backup and restore test exists for the four notebook PVCs and the
  Sandbox Connect PostgreSQL database.
- All credentials found in manifests have been rotated and recreated through a
  Secret manager. No credential value belongs in this folder or Git.
- The custom `v1.10-with-subpath-v2` Notebook Controller behavior has been
  characterized against upstream 1.11.0 with the tests in this dossier.

## Upstream sources

- [Kubeflow installation page](https://www.kubeflow.org/docs/started/installing-kubeflow/)
- [Community Distribution 26.03.1 release](https://github.com/kubeflow/community-distribution/releases/tag/26.03.1)
- [Pinned source tree](https://github.com/kubeflow/community-distribution/tree/f09f3eeaa25cc852665f460497a42b7fc68639ac)
- [Upgrading and extending guidance](https://github.com/kubeflow/community-distribution/blob/release-26.03.1/README.md#upgrading-and-extending)
- [Kubeflow Notebooks 1.11.0](https://github.com/kubeflow/notebooks/releases/tag/v1.11.0)
- [Kubeflow Dashboard 2.0.0](https://github.com/kubeflow/dashboard/releases/tag/v2.0.0)

Upstream community support is best-effort for roughly six months. Re-check the
installation page before production cutover; if a newer stable patch exists,
qualify it in the test cluster and update the immutable commit pin here.
