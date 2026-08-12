# Data migration and rollback

The preferred pattern is blue-green. The source cluster remains untouched until
the target proves it can restore, serve, and delete workloads correctly.

## What must move

| Data | Migration method | Notes |
|---|---|---|
| 78 Profile CRs | sanitized YAML export/apply | Preserve name and `spec.owner`; remove server metadata/status. Sensitive identity data belongs in a private encrypted bundle. |
| Four stopped Notebook CRs | normally recreate from Sandbox metadata; export for evidence | Do not start the same notebook in both clusters. |
| Four 10 GiB EBS PVCs | CSI/EBS snapshot restore or quiesced file copy | Restore-test is mandatory. |
| Sandbox PostgreSQL | `pg_dump`/restore or storage-native clone | Required for notebook/booking/profile lifecycle coherence. |
| Kubernetes Secrets | recreate from source secret manager | Do not export them to Git or reuse leaked credentials. |
| Keycloak | reuse existing realm/client with added target redirect, or export through Keycloak tooling | Never copy the database by dumping Kubernetes Secret values. |
| Registry credentials | recreate/rotate | Verify per-Profile propagation. |
| TLS/DNS | issue new target certificate; later update DNS | Keep source hostname valid for rollback. |

## Build a private migration bundle

Run `scripts/collect-live-inventory.sh` against the source. For migration-only
exports, use a directory outside the Git checkout with restrictive permissions.
The following examples require `yq` v4 and do not include Secrets:

```bash
umask 077
export PRIVATE_BUNDLE="$PWD/private-kubeflow-migration-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$PRIVATE_BUNDLE"

kubectl --context "$SOURCE_CONTEXT" get profiles.kubeflow.org -o yaml | \
  yq 'del(.items[].metadata.creationTimestamp,
          .items[].metadata.generation,
          .items[].metadata.managedFields,
          .items[].metadata.resourceVersion,
          .items[].metadata.uid,
          .items[].status)' \
  >"$PRIVATE_BUNDLE/profiles.yaml"

kubectl --context "$SOURCE_CONTEXT" get notebooks.kubeflow.org -A -o yaml | \
  yq 'del(.items[].metadata.creationTimestamp,
          .items[].metadata.generation,
          .items[].metadata.managedFields,
          .items[].metadata.resourceVersion,
          .items[].metadata.uid,
          .items[].status)' \
  >"$PRIVATE_BUNDLE/notebooks-reference.yaml"
```

Encrypt the bundle with the organization's approved tool and recipient before
moving it. Add its unencrypted name to a global ignore. Do not commit either
plaintext identities or database dumps.

## PVC migration choices

### Option A: CSI/EBS snapshots (recommended)

1. Install an EBS `VolumeSnapshotClass` with deletion policy `Retain` through
   IaC on source and target.
2. Keep all four Notebooks stopped and verify no migration/helper pod mounts the
   claims for write.
3. Create one `VolumeSnapshot` per claim and wait for `readyToUse=true`.
4. Record the snapshot handles in the private bundle.
5. Restore new target PVCs from snapshots; keep source claims intact.
6. Mount restored claims read-only first, compare file counts/checksums, then
   perform an application-level read/write test on a disposable clone.

The source currently has no VolumeSnapshotClass, so this path is not ready until
the snapshot class and cross-cluster restore have been proven.

### Option B: quiesced streaming copy

For only four small claims, create restricted migration Pods that mount a source
claim read-only and a target claim writable. Stream `tar` between explicit
contexts through the operator host or approved private transport. Preserve
numeric ownership and modes; do not follow external symlinks. Compare a
manifest of relative path, size, mode, and checksum after copy.

Do not use `kubectl cp` blindly for multi-gigabyte state without retry and
checksum handling. Remove migration Pods and their RBAC after verification.

### Velero caveat

Current Velero schedules do not include Profile PVCs and the deployment has no
node-agent. Creating a Velero Backup object that lists PVC/PV metadata is not
proof that volume bytes were copied. If Velero is chosen, install/configure the
CSI snapshot or filesystem backup mechanism and demonstrate a restore into a
different namespace/cluster first.

## PostgreSQL consistency

Determine whether PostgreSQL is external or cluster-local from the approved
Secret manager without printing its URL. Use a database role with only the
permissions needed for dump/restore.

For a rehearsal:

1. Take a logical dump from a consistent snapshot.
2. Restore to an isolated target database.
3. Change every target Sandbox component to the cloned URL.
4. Confirm the target has no ability to reach the live database.
5. Reconcile DB Profile/Notebook records against restored Kubernetes objects.

For final cutover:

1. Announce a write freeze and stop new bookings/profile creation.
2. Scale source worker, slot-lifecycle, and profile-credit-sync to zero; keep the
   API read-only or stop it.
3. Confirm there are no active/ready/shutting-down bookings and all Notebooks
   are stopped.
4. Take the final DB dump and final PVC snapshot/delta.
5. Restore target data and perform reconciliation before enabling target
   controllers.
6. Enable target API/controllers exactly once, then switch DNS/traffic.

## Reconciliation invariants

- Every DB Profile has exactly one Profile CR and namespace.
- Every live DB Notebook row maps to the expected Notebook/PVC, or has an
  explicitly terminal state.
- No Notebook name exists active in both clusters.
- PVC sizes, storage class, ownership, and checksums match migration records.
- Profile namespaces have required Istio, Pod Security, RBAC, pull Secret, and
  owner AuthorizationPolicy state.
- No token Secret or refresh token from the old cluster is reused. Users obtain
  new delegated sessions.

## Cutover and rollback

Use low-TTL test DNS before final cutover. Keep the source cluster and database
snapshot read-only for an agreed observation window.

Rollback trigger examples:

- Authentication loop or identity/header mismatch.
- Profile reconciliation deletes or changes unexpected namespaces.
- CPU/GPU Notebook cannot become ready within SLO.
- PVC integrity mismatch.
- Token-session readiness returns persistent 403/503.
- Lifecycle worker creates duplicates or corrupts booking transitions.

Rollback steps:

1. Stop target API writes and scale all target Sandbox controllers to zero.
2. Capture target logs/state for diagnosis without copying tokens.
3. Restore DNS/ingress to source.
4. Re-enable source controllers against the source database only.
5. Reconcile any writes accepted by target. If target accepted user changes,
   decide explicitly whether to reverse-migrate them; never let both databases
   diverge silently.

Do not delete target or source PVCs, CRDs, Profiles, namespaces, or snapshots
during the rollback window. Deleting the Profiles CRD can cascade into all
Profile namespaces.
