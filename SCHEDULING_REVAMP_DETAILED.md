# Generic Runtime Scheduling and PVC-Mount Framework

## 1. Design Principle

The worker must provide reusable Kubernetes configuration capabilities without encoding project-specific assumptions such as:

- Every project has uploads, artifacts, or CBR storage.
- Every notebook has a fixed number of PVCs.
- External storage must use RWX.
- Artifacts must mount at `/mnt/artifacts`.
- Both CPU and GPU workloads must always be enabled.
- Every existing PVC must be backed by RGW or NFS.

The worker will instead follow this rule:

> For a selected workload, apply only the scheduling rules and PVC mounts defined in its configuration.

The current TGDex deployment can define four mounts, while another deployment can define one, two, or none.

```text
Project A
└── user-data

Project B
├── user-data
└── shared-datasets

TGDex
├── user-data
├── uploads
├── artifacts
└── cbr-nfs

Stateless project
└── no PVC mounts
```

Project-specific requirements belong in deployment configuration and platform automation, not worker validation code.

## 2. Generic Configuration Interface

### 2.1 Configuration loading

Add:

```text
WORKER_RUNTIME_CONFIG_PATH
```

Behavior:

- If set, load the referenced YAML or JSON file.
- Reject unreadable, malformed, or invalid configured files.
- Do not silently use legacy behavior when an explicitly configured file is broken.
- If unset, synthesize the current behavior from legacy environment variables.
- Load once at startup; ConfigMap changes require a worker rollout.

### 2.2 Top-level structure

```yaml
apiVersion: sandbox-connect/v1alpha1

defaults:
  externalPVCWaitTimeout: 120s

workloads:
  cpu:
    scheduling: {}
    pvcMounts: []

  gpu:
    scheduling: {}
    pvcMounts: []
```

Only configured workload sections are required.

CPU-only projects are valid:

```yaml
workloads:
  cpu:
    scheduling: {}
    pvcMounts: []
```

If a GPU notebook is requested but no GPU policy exists, fail that notebook with a clear configuration error. Do not reject a CPU-only configuration at worker startup.

## 3. Generic Scheduling Configuration

### 3.1 Schema

```yaml
scheduling:
  nodeSelector: {}
  affinity: {}
  tolerations: []

  instanceTypeOverride:
    enabled: false
    selectorKey: node.kubernetes.io/instance-type
    required: false
```

`nodeSelector`, `affinity`, and `tolerations` use normal Kubernetes structures.

Example CPU policy:

```yaml
scheduling:
  nodeSelector:
    sandbox.tgdex.io/workload: cpu

  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
          - matchExpressions:
              - key: node.kubernetes.io/instance-type
                operator: In
                values:
                  - t3a.large
                  - t3a.2xlarge
                  - c5a.4xlarge

  tolerations:
    - key: sandbox.tgdex.io/cpu
      operator: Equal
      value: "true"
      effect: NoSchedule
```

Example GPU policy:

```yaml
scheduling:
  nodeSelector:
    sandbox.tgdex.io/workload: gpu

  affinity: {}

  tolerations:
    - key: nvidia.com/gpu
      operator: Exists
      effect: NoSchedule

  instanceTypeOverride:
    enabled: true
    selectorKey: node.kubernetes.io/instance-type
    required: true
```

### 3.2 Instance-type behavior

The current project stores a GPU instance type on the notebook/category. Other projects may not want that value to affect scheduling, so make the behavior configurable.

When enabled:

1. Copy configured `nodeSelector`.
2. Read `notebook.instance_type`.
3. Set the configured selector key to that value.
4. Preserve every other selector.

For example, a stored `g4dn.xlarge` value produces:

```yaml
nodeSelector:
  sandbox.tgdex.io/workload: gpu
  node.kubernetes.io/instance-type: g4dn.xlarge
```

When disabled:

- Ignore the stored instance type for scheduling.
- Use only configured selectors and affinity.

When `required: true` and the notebook has no instance type:

- Fail provisioning with a clear error.
- Do not pick an arbitrary GPU node type.

### 3.3 Helper pods

Apply the selected scheduling policy to:

- The Kubeflow Notebook pod.
- Runtime file/Git injection pods.
- Other helper pods that mount a managed workspace PVC.

This avoids storage-topology conflicts between a helper pod and the final notebook. Helper pods inherit placement constraints but do not automatically request GPU resources.

## 4. Generic PVC-Mount Configuration

### 4.1 Mount list

Each workload can define zero or more PVC mounts:

```yaml
pvcMounts:
  - name: example-volume
    mountPath: /mnt/example
    readOnly: false
    workspace: false
    required: true

    source:
      type: existing
      claimNameTemplate: example-pvc
```

There are no predefined names such as `uploads`, `artifacts`, or `cbr`. The `name` is an arbitrary Kubernetes volume name used in the generated pod and in logs.

### 4.2 Existing PVC source

Use when another system owns the PVC:

```yaml
- name: shared-data
  mountPath: /mnt/shared
  readOnly: true
  required: true

  source:
    type: existing
    claimNameTemplate: shared-data
    waitForBound: true
```

Optional compatibility checks may be configured:

```yaml
source:
  type: existing
  claimNameTemplate: shared-data
  waitForBound: true

  expected:
    accessModes:
      - ReadWriteMany
    volumeMode: Filesystem
```

If `expected` is omitted, the worker only verifies claim availability. It does not impose RWX or filesystem assumptions.

The claim is always resolved in the notebook namespace. Cross-namespace PVC references are not supported because Kubernetes pods cannot mount them.

### 4.3 Optional existing PVC

Some projects may want a mount only when infrastructure provides it:

```yaml
- name: optional-datasets
  mountPath: /mnt/datasets
  readOnly: true
  required: false

  source:
    type: existing
    claimNameTemplate: shared-datasets
    waitForBound: true
```

Behavior:

- If the claim exists and meets configured expectations, mount it.
- If it is missing or unavailable after the timeout, log the reason and omit it.
- Do not fail notebook provisioning.
- Never create or delete it.

Required mounts fail provisioning when unavailable.

### 4.4 Managed PVC source

Use when the worker should create the claim:

```yaml
- name: user-data
  mountPath: /home/jovyan
  readOnly: false
  workspace: true
  required: true

  source:
    type: managed
    claimNameTemplate: "{pvcName}"
    retentionPolicy: DeleteWithNotebook

    spec:
      storageClassName: rook-ceph
      accessModes:
        - ReadWriteOnce
      volumeMode: Filesystem
      resources:
        requests:
          storage: "{storageSize}"
```

Supported retention policies:

| Policy | Behavior |
|---|---|
| `DeleteWithNotebook` | Delete the claim during sandbox cleanup |
| `Retain` | Keep it after notebook deletion |

The framework can therefore support per-notebook storage, retained worker-created storage, and existing platform-owned claims.

### 4.5 Workspace flag

`workspace: true` identifies the volume used by existing workspace-dependent functionality:

- Demo notebook initialization.
- File URL injection.
- Git repository injection.

This is not a required volume role.

Rules:

- A workload may define zero or one workspace mount.
- Multiple workspace mounts are rejected because helper pods would not know which one to use.
- Without a workspace, the Notebook may still run with image-local or ephemeral storage.
- Demo-copy init containers that need persistence are omitted when there is no workspace.
- A request containing `fileUrl` or `gitUrl` fails clearly when there is no workspace.
- Additional PVCs do not need `workspace: true`.

### 4.6 Optional subpath

Permit projects to mount only part of a PVC:

```yaml
subPathTemplate: "users/{namespace}"
```

Validation must ensure the rendered path:

- Is relative.
- Does not start with `/`.
- Does not contain `..`.
- Does not escape the mounted volume.

For the current static RGW configuration, omit `subPathTemplate` because the CSI `volumeHandle` already selects the user-specific prefix.

### 4.7 Supported templates

Support only:

| Template | Meaning |
|---|---|
| `{namespace}` | Notebook namespace |
| `{notebookName}` | Notebook name |
| `{pvcName}` | Database user-data PVC name |
| `{storageSize}` | Database/category storage request |

Do not support arbitrary Go templates, shell expressions, or environment-variable expansion.

## 5. Generic Validation Rules

Validation should protect the worker from malformed configuration and invalid Kubernetes objects. It should not enforce one project's storage architecture.

### 5.1 Rules that remain

Validate:

- Known `apiVersion`.
- At least one workload policy.
- Strict YAML/JSON field names.
- Valid workload names supported by the worker.
- Valid Kubernetes selector keys and values.
- Structurally valid affinity and tolerations.
- Unique volume names within each workload.
- Unique container mount paths within each workload.
- Kubernetes-compatible volume names.
- Absolute `mountPath`.
- Safe relative `subPathTemplate`.
- Supported template variables.
- Valid source type: `existing` or `managed`.
- Required source fields for the selected type.
- Valid retention policy for managed claims.
- Valid rendered Kubernetes claim names.
- Positive wait timeouts.
- At most one `workspace: true` volume per workload.
- Managed PVC specs contain enough information for Kubernetes creation.
- Existing-claim expectation fields, when supplied, are valid Kubernetes values.

### 5.2 Rules removed

Do not require:

- Both CPU and GPU policies.
- Exactly four PVC mounts.
- Any specific mount count.
- A user-data mount.
- Uploads, artifacts, or CBR mounts.
- Specific claim names or mount paths.
- RWX for external claims.
- RWO for managed claims.
- Filesystem volume mode unless configured.
- A particular StorageClass.
- All external claims to be required.
- External claims to be RGW or NFS.
- A fixed GPU instance selector.
- `subPath` to always be absent.

### 5.3 Project-specific validation

Requirements such as the following belong in deployment configuration tests or platform policy:

```text
TGDex must define four mounts
Artifacts must be RWX
CBR must be read-only
Artifacts must use sandbox-artifacts
GPU must use an instance-type selector
```

The repository can test the supplied TGDex runtime configuration without making those rules globally mandatory.

## 6. Runtime Volume Resolution

### 6.1 Processing order

For the selected workload:

```text
Load configured pvcMounts
        ↓
Resolve templates
        ↓
Check existing required mounts
        ↓
Resolve optional existing mounts
        ↓
Create or reuse managed mounts
        ↓
Build pod volumes and mounts
        ↓
Run workspace helpers when configured
        ↓
Create Notebook
```

Only successfully resolved mounts are included in the generated Notebook.

### 6.2 Existing required claims

For `required: true`:

1. Resolve the claim name.
2. Query the claim in the notebook namespace.
3. If configured, wait for `Bound`.
4. If `expected` is defined, compare access modes and volume mode.
5. Fail provisioning if the claim remains unavailable or incompatible.

Do not infer project-specific expectations when `expected` is absent.

### 6.3 Existing optional claims

For `required: false`:

1. Resolve the claim name.
2. Check whether it exists.
3. Wait only according to its configured behavior.
4. Include it when ready and compatible.
5. Otherwise omit it and continue.

Record a warning when an optional mount is omitted.

### 6.4 Managed claims

For each managed source:

1. Render the claim name and PVC spec.
2. Apply worker-management labels.
3. Create the claim when missing.
4. If it exists, verify ownership and immutable fields.
5. Reuse only when compatible.
6. Reject unrelated same-name claims.

Managed labels should include:

```yaml
sandbox-connect.tgdex.io/managed: "true"
sandbox-connect.tgdex.io/notebook: <notebook-name>
sandbox-connect.tgdex.io/volume: <configured-volume-name>
sandbox-connect.tgdex.io/retention: delete-with-notebook
```

Retained managed claims must use labels that do not associate cleanup ownership with one notebook.

### 6.5 Status semantics

The existing `pvc-applied` event will mean:

> Every required configured PVC is available, and every managed PVC was successfully prepared.

If no PVC mounts are configured, storage preparation succeeds without creating a PVC.

## 7. Generic Manifest Generation

For every resolved mount, generate a pod volume:

```yaml
volumes:
  - name: <configured-name>
    persistentVolumeClaim:
      claimName: <resolved-claim>
      readOnly: <configured-readOnly>
```

Add its main-container mount:

```yaml
volumeMounts:
  - name: <configured-name>
    mountPath: <configured-mountPath>
    readOnly: <configured-readOnly>
    subPath: <rendered-subPath-if-configured>
```

The worker must not add unconfigured project volumes.

Examples:

```text
Project with only user data:
/home/jovyan

Project with user data and datasets:
/home/jovyan
/mnt/datasets

TGDex:
/home/jovyan
/mnt/uploads
/mnt/artifacts
/mnt/cbr

Stateless project:
No PVC volumes
```

Platform-token Secret and `emptyDir` volumes remain separate and are unaffected.

## 8. Generic Cleanup

Supporting arbitrary managed claims means cleanup cannot rely only on the single legacy `notebooks.pvc_name`.

Update cleanup to:

1. Delete the Notebook resource.
2. List PVCs in the notebook namespace carrying sandbox-management labels.
3. Delete those labeled `retention: delete-with-notebook`.
4. Preserve managed claims labeled `retain`.
5. Preserve all existing and platform-owned claims.
6. Attempt deletion of the legacy database `pvc_name` for backward compatibility.
7. Treat NotFound as success.

Apply the same behavior to:

- Worker provisioning rollback.
- User termination.
- Booking reset.
- No-show expiration.
- Slot-end cleanup.
- Direct notebook deletion when booking mode is disabled.

Never attach Notebook owner references to external claims. Deleting a Notebook must not trigger garbage collection of shared storage.

No database migration is needed because Kubernetes labels identify additional managed claims.

## 9. Current TGDex Deployment Profile

The generic worker will not require these mounts, but the current deployment configuration will define them:

```yaml
pvcMounts:
  - name: user-data
    workspace: true
    mountPath: /home/jovyan
    readOnly: false
    required: true
    source:
      type: managed
      claimNameTemplate: "{pvcName}"
      retentionPolicy: DeleteWithNotebook
      spec:
        storageClassName: rook-ceph
        accessModes: [ReadWriteOnce]
        volumeMode: Filesystem
        resources:
          requests:
            storage: "{storageSize}"

  - name: uploads
    mountPath: /mnt/uploads
    readOnly: false
    required: true
    source:
      type: existing
      claimNameTemplate: sandbox-uploads
      waitForBound: true
      expected:
        accessModes: [ReadWriteMany]
        volumeMode: Filesystem

  - name: artifacts
    mountPath: /mnt/artifacts
    readOnly: false
    required: true
    source:
      type: existing
      claimNameTemplate: sandbox-artifacts
      waitForBound: true
      expected:
        accessModes: [ReadWriteMany]
        volumeMode: Filesystem

  - name: cbr-nfs
    mountPath: /mnt/cbr
    readOnly: true
    required: true
    source:
      type: existing
      claimNameTemplate: cbr-nfs
      waitForBound: true
      expected:
        accessModes: [ReadWriteMany]
        volumeMode: Filesystem
```

Platform automation provisions:

- `sandbox-uploads` from `uploads/{namespace}`.
- `sandbox-artifacts` from `no-code-artifacts/{namespace}`.
- `cbr-nfs` from the CBR-provided NFS backend.

Those provisioning details remain outside the generic worker.

## 10. Failure Behavior

### Missing required existing claim

- Wait until the configured timeout.
- Fail notebook provisioning.
- Report the configured mount name and claim.
- Clean up only managed `DeleteWithNotebook` claims.
- Leave all external claims untouched.

### Missing optional existing claim

- Log that the optional mount was omitted.
- Continue provisioning.
- Do not add the pod volume or volume mount.

### Managed PVC failure

- Stop provisioning.
- Delete only managed claims created for the current notebook whose retention policy is `DeleteWithNotebook`.
- Preserve retained and external claims.

### Runtime assets without workspace

When `fileUrl` or `gitUrl` is supplied but the selected workload has no `workspace: true` mount:

- Reject provisioning with a clear error.
- Do not guess which configured volume should receive the files.

### Missing workload policy

If a notebook requires GPU but only CPU is configured:

- Fail that notebook with `no runtime policy configured for workload gpu`.
- Keep the worker healthy so it can continue processing supported workloads.

## 11. Tests

### Generic configuration tests

Cover:

- CPU-only, GPU-only, and combined policies.
- Empty PVC mount list.
- One, two, four, and arbitrary mount counts.
- Arbitrary mount names and paths.
- Existing and managed sources.
- Required and optional existing claims.
- Managed delete and retain policies.
- Configurations without a workspace.
- More than one workspace rejected.
- Strict unknown-field rejection.
- Invalid templates, paths, names, affinity, and tolerations.

### Manifest tests

Verify:

- Only configured volumes appear.
- No project-specific volume is injected automatically.
- Optional missing mounts are absent.
- Read-only and subpath settings are preserved.
- Arbitrary valid access modes are accepted.
- CPU/GPU scheduling matches configuration.
- Instance-type overlay occurs only when enabled.
- Helper pods receive scheduling rules.
- Helper pods mount only the configured workspace.

### Kubernetes interaction tests

Using a fake client, test:

- Existing claim already Bound.
- Missing required claim.
- Missing optional claim.
- Optional claim becoming available.
- Pending claim timeout.
- Expected access-mode mismatch.
- Existing claim with no expectations.
- Managed claim creation and reuse.
- Managed claim collision.
- Multiple managed claims.
- Partial creation cleanup.
- Retained managed claim preservation.
- External claim preservation.

### Project-profile tests

Separately validate the supplied TGDex configuration:

- Four configured mounts.
- Correct claim names and paths.
- Uploads and artifacts RW.
- CBR read-only.
- RGW claims expected to support RWX.
- User-data managed and deleted with the notebook.

These tests protect this deployment without restricting other deployments.

## 12. Rollout and Acceptance

### Rollout

1. Deploy worker code with the config path unset and verify legacy behavior.
2. Add generic runtime configuration parsing.
3. Add label-based cleanup while preserving legacy `pvc_name` deletion.
4. Validate the TGDex configuration in CI.
5. Ensure TGDex external claims are provisioned.
6. Enable `WORKER_RUNTIME_CONFIG_PATH`.
7. Canary CPU and GPU notebooks.
8. Test a minimal profile with only a workspace PVC to prove the framework is not tied to four mounts.

### Acceptance criteria

The implementation is complete when:

- The worker accepts arbitrary valid PVC mount lists.
- Only configured mounts are generated.
- Uploads, artifacts, and CBR are not globally required.
- CPU-only and GPU-only policies are valid.
- Empty PVC lists are valid.
- Project-specific access modes, paths, claim names, and storage technologies remain configuration choices.
- Required mounts fail clearly when unavailable.
- Optional mounts can be omitted safely.
- Arbitrary managed claims follow configured retention policies.
- External claims are never deleted by sandbox cleanup.
- Workspace-dependent functionality uses the explicitly marked workspace volume.
- Legacy deployments continue working without the new config.
- The TGDex four-volume configuration still produces the originally requested behavior.
