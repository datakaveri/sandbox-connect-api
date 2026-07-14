# Worker runtime policy

The worker reads scheduling and PVC mount behavior from the versioned
`runtime-config.yaml` key in `sandbox-worker-runtime-config`. The Deployment
mounts that key at `/etc/sandbox-worker/runtime/runtime-config.yaml` and sets
`WORKER_RUNTIME_CONFIG_PATH` to the same path.

CPU and GPU policies are independent. Each policy can define:

- `nodeSelector`, `affinity`, and `tolerations`.
- An optional per-notebook instance-type override.
- Zero or more PVC mounts, in the order they should appear in the notebook.

The worker does not require project-specific names such as uploads, artifacts,
or CBR. A deployment can remove those entries, add different entries, or use no
PVCs. Only entries present in the selected CPU/GPU policy are mounted.

## PVC sources

`source.type: managed` means the worker creates the claim. Managed claims must
have a `retentionPolicy`:

- `DeleteWithNotebook`: label the claim for automatic notebook cleanup.
- `Retain`: preserve the claim when the notebook is deleted.

`source.type: existing` means another platform component creates the claim.
The worker only waits for and mounts it; it never deletes it. Set
`required: false` when a missing external claim should be omitted instead of
blocking notebook creation.

Existing claims must be created in every user namespace where they are used.
For example, a claim called `sandbox-artifacts` in namespace `user-a` is a
different claim from `sandbox-artifacts` in namespace `user-b`. The underlying
PV/CSI configuration may point those claims to different bucket prefixes such
as `no-code-artifacts/user-a` and `no-code-artifacts/user-b`.

Supported template tokens are:

- `{namespace}`
- `{notebookName}`
- `{pvcName}`
- `{storageSize}`

They may be used in `claimNameTemplate`, `subPathTemplate`, and managed PVC
`spec` string values.

The checked-in TGDex profile uses `rook-ceph` for its managed user-data PVC.
Deployments using a hostpath provisioner or another backend should replace that
value with the StorageClass installed in their cluster.

## Workspace mount

At most one mount can set `workspace: true`. It is the persistent workspace
used by runtime file or Git injection. A workload can be stateless and define no
workspace, but notebook requests containing runtime assets will then fail with a
clear configuration error.

## Deployment order

1. Provision any external PVs/PVCs in the user namespaces.
2. Edit and apply `infra/worker/configmap.yaml`.
3. Apply `infra/worker/deployment.yaml`.
4. Restart the worker after any runtime ConfigMap change because the policy is
   loaded only during process startup:

   ```bash
   kubectl rollout restart deployment/sandbox-worker -n sandbox
   kubectl rollout status deployment/sandbox-worker -n sandbox
   ```

5. Confirm the worker logs `worker runtime configuration loaded`.
6. Create one CPU and one GPU sandbox and inspect their pod scheduling fields,
   volumes, and mounts.

If `WORKER_RUNTIME_CONFIG_PATH` is unset, the worker generates a legacy policy
from the old storage-class and CPU/GPU instance-type environment variables.
This fallback supports a staged rollout but should not be used for new
deployments.
