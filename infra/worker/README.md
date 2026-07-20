# Worker Notebook templates

The worker requires two independently deployable `SandboxNotebookTemplate` documents:

- `cpu-notebook-template.yaml`
- `gpu-notebook-template.yaml`

They are stored in the `sandbox-worker-notebook-templates` ConfigMap, mounted at `/etc/sandbox-worker/templates`, and selected from the request's CPU/GPU resources before any PVC is prepared. Missing or invalid files stop worker startup; there is no environment or compiled-in fallback.

## Ownership and precedence

Each wrapper contains `spec.lifecycle` plus a complete Kubeflow `Notebook` at `spec.notebook`. The embedded Notebook owns all static Kubernetes configuration: the default image, service account, pull secrets, scheduling, pod/container security, NFS and PVC volumes, mounts, environment, commands, ports, init containers, and sidecars.

The worker deep-copies the selected template for each request and changes only:

- Notebook name, namespace, the dynamic `app` label, and primary-container name.
- CPU and memory requests/limits, plus the requested GPU resource key/count.
- The primary image when a custom request image is present.
- Allowed PVC tokens, optional-PVC removal, and a requested instance-type selector.
- Request-specific runtime/demo helper values and platform-token Secret/session/user values.

Static resource keys such as `ephemeral-storage` are preserved. Worker-generated demo initialization is controlled by `spec.lifecycle.demoFiles.enabled`; static template init containers are always preserved.

## Lifecycle contract

`spec.lifecycle.volumePolicies` contains only behavior that a Notebook volume cannot express. Every entry must reference an embedded `persistentVolumeClaim` volume by name and define exactly one of `managed` or `existing`. Managed policies define the PVC spec and retention. Existing policies define waiting and compatibility checks. NFS, Secret, `emptyDir`, and other native volumes exist only in the embedded Notebook.

`workspaceVolumeName` must be a writable PVC mounted by the primary `notebook` container. Runtime file/Git injection uses that PVC and inherits the selected Notebook's node selector, affinity, and tolerations. An unavailable optional existing PVC causes its volume and every corresponding mount to be removed from the rendered Notebook.

Only these tokens are supported, and only in PVC claim names, volume-mount subpaths, and managed PVC spec strings:

- `{namespace}`
- `{notebookName}`
- `{pvcName}`
- `{storageSize}`

There is no general-purpose YAML or string templating.

## Startup validation and security

Both wrappers are strictly decoded. Startup fails for unknown wrapper fields, wrong API/kind/workload name, multiple YAML documents, invalid lifecycle references, incompatible volume types, invalid workspace configuration, duplicate container/volume/mount names or paths, unsupported token locations, or unsafe security fields.

The embedded Notebook must contain exactly one marker container named `notebook` and a default image. The worker always enforces `automountServiceAccountToken: false`, `privileged: false`, `allowPrivilegeEscalation: false`, and `procMount: Default` on the rendered object. Broader cluster policy remains the responsibility of Kubernetes admission controls.

Platform-token resources are wholly template-owned. When `spec.lifecycle.platformToken` is absent, no platform-token resources are injected or patched. When present, the template must provide the sidecar, Secret/cache volumes, mounts, static environment, image, client configuration, and URLs; the worker patches only the refresh Secret name, token-session URL, and expected user ID.

## Rollout

1. Apply `sandbox-worker-notebook-templates` while retaining the old runtime ConfigMap for rollback.
2. Deploy the new worker image and Deployment with `WORKER_CPU_NOTEBOOK_TEMPLATE_PATH` and `WORKER_GPU_NOTEBOOK_TEMPLATE_PATH`.
3. Validate CPU, GPU, runtime file/Git injection, platform-token refresh, PVC retention, and failure cleanup.
4. Delete the old runtime ConfigMap only after the rollout is stable.

Rollback uses the previous worker image and its matching Deployment configuration; worker images and template interfaces must be rolled back together.
