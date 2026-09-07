# evaluation-argo-service deployment

These manifests are development artifacts. They have placeholders and must not
be applied as-is. The service has no Kubernetes Service or ingress because it
exposes no user-facing HTTP API.

## Safe rollout order

1. Confirm the target checkout and cluster explicitly. Do not copy these files
   into the separate live-manifest checkout as part of feature development.
2. Install and validate Argo Workflows CRDs/controller in a non-production
   environment.
3. Provision and validate an EFS/RWX StorageClass. Then configure the API to
   provision the dedicated profile-scoped `evaluation-workspace` PVC and
   confirm every sandbox for the same user sees it read-only at
   `/home/jovyan/evaluation-workspace`. The approval copy Job mounts it
   read-write.
4. Build the runner, uploader, copier, and controller images; replace all image
   placeholders with immutable digest references.
5. Obtain the exact file-server API contract and create run-scoped credentials.
   The current repository does not contain that contract, so uploader/copier
   image implementation is intentionally not guessed here.
6. Reconcile `runner-rbac.example.yaml`, the replacement ConfigMap, and Secrets
   into each participating user namespace. Fill and review a NetworkPolicy from
   `networkpolicy.example.yaml` using real service CIDRs.
7. Have the DBA create the restricted worker role, review, back up, and apply
   `migrations/001_evaluations.sql` in a test database.
   `worker-db-grants.example.sql` shows the controller's required table-only
   grants; replace its role placeholder and review it separately.
8. Deploy the controller and exercise PVC-detach, workflow failure, restart,
   upload, approval-copy, and checksum/idempotency cases.
9. Set `API_EVALUATIONS_ENABLED=true` on Sandbox Connect. For booking mode also
   set `SLOT_LIFECYCLE_EVALUATIONS_ENABLED=true`; direct mode does not require
   the slot-lifecycle flag.

All evaluation feature flags default to `false`. Apply the database migration
before enabling them.

The Approve endpoint also requires `API_EVALUATION_WORKSPACE_ENABLED=true`;
Submit and List Outputs remain available when the dedicated workspace is
disabled. Set the matching worker flag and claim name so new notebook specs
receive the same PVC. The older `WORKSPACE_ENABLED` feature is independent.

## Production storage finding (2026-09-07)

The inspected `PMJAY-IISC-Hackathon` context has only `ebs-csi-storage-class`
and `gp2`. Both are EBS/`ReadWriteOnce`; no EFS StorageClass, RWX PV, or RWX PVC
was present. An `efs.csi.aws.com` CSIDriver registration exists, but no matching
EFS CSI controller or node workload was returned by the read-only inspection.
Consequently the evaluation workspace defaults to disabled and its storage
class has no default. `ebs-csi-storage-class` must not be used for this common
workspace. The API verifies the configured StorageClass provisioner before it
creates any claim; this requires read-only `get` access to StorageClasses.
`efs-storageclass.example.yaml` is a review-only starting point once the EFS
CSI driver and filesystem prerequisites are ready; it has not been applied.

## Unresolved environment inputs

- the allowed PS4 supporting-file set and final notebook relative path;
- exact sandbox placeholders and production environment names/values;
- file-server upload/download routes, authentication, limits, and `/user` path;
- approver roles (empty API policy means the owning user may approve);
- target RWX storage class and normal-notebook mount policy;
- target cluster and booking behavior after evaluation submission.

The controller creates Workflows and copy Jobs in each user's namespace because
PVCs and Secrets are namespace-scoped. The controller ClusterRole deliberately
does not grant Secret read access. Workflow/copy pods receive only their own
namespace-local Secret references.
