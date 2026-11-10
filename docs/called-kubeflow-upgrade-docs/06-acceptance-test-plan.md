# Acceptance test plan

Record command output, timestamp, target context, release commit, and result for
every case. Use synthetic users/data first, then a restored non-production copy.

## Gate A: cluster and rendering

- Target is Kubernetes 1.35/1.36 and every intended node is AMD64.
- EBS CSI provisioning, expansion, snapshot, and restore pass.
- GPU group scales from zero and advertises `nvidia.com/gpu`.
- Rendered manifests contain the pinned image versions and no `latest`, default
  passwords, placeholder markers, or credential values.
- Client dry-run succeeds; server dry-run succeeds against the target.
- Kyverno/Pod Security reports no unexpected blocked Kubeflow object.
- All containers meet CPU/memory-limit policy or have a documented exception.

## Gate B: platform control plane

- All CRDs are Established; Notebook serves `v1beta1` and stores `v1`.
- Istio CNI DaemonSet covers target Linux nodes; no privileged Istio init
  container is added to notebook Pods.
- Dex, OAuth2 Proxy, Istio, Dashboard/Profile/PodDefaults, and all Notebook suite
  Deployments are Available.
- Webhook certificates are Ready and admission webhook calls do not time out.
- No CrashLoopBackOff, ImagePullBackOff, pending PVC, or repeated restart exists.
- NetworkPolicy and Istio deny-by-default behavior is active.

## Gate C: authentication and authorization

- Anonymous browser follows one redirect chain to Keycloak and back.
- Login, refresh after cookie aging, logout, and second login pass.
- Wrong redirect host/path is rejected by Keycloak.
- Forged `kubeflow-userid`/groups headers from outside cannot impersonate.
- User A cannot list/open/change User B's Profile resources or notebook URL.
- Profile owner can use Jupyter/KFAM and expected UIs.
- OAuth/JWT issuer values match byte-for-byte across Dex, OAuth2 Proxy, Istio,
  and tokens.
- Machine-to-machine bearer access is either tested or explicitly unsupported;
  the live setup uses the `m2m-dex-only` overlay.

## Gate D: Profile lifecycle

- `POST /v1/profile/create` creates Profile, namespace, service accounts, RBAC,
  Istio owner policy, and registry Secret without duplicate reconciliation.
- Repeated create is idempotent/returns the documented response.
- Namespace labels include Istio injection and target Pod Security posture.
- Profile Controller restart does not delete/recreate valid namespaces.
- All 78 private Profile exports can be server-dry-run applied before real apply.
- Profile count and namespace count reconcile after restore.

## Gate E: CPU Notebook contract

- Create a CPU booking/direct Notebook through the real API and worker.
- EBS PVC binds in the correct zone and the configured retention policy holds.
- Demo/runtime init writes expected files and preserves ownership.
- Notebook StatefulSet/Pod becomes Ready and `readyReplicas` becomes 1.
- Generated URL loads Jupyter and the intended startup file/workspace.
- Platform-token sidecar reaches `/readyz`; API token session returns 200, not
  Istio 403 or timeout 503; no token is logged.
- Refresh-token rotation persists correctly and old credentials stop working.
- Idle culling uses 360-minute/10-minute configuration (use a reduced value only
  in a disposable timing test).
- Stop annotation scales the Notebook down; start removes it and preserves PVC
  contents; delete/terminate obeys retention and cleans the token Secret.

## Gate F: custom controller regression matrix

Compare upstream 1.11.0 output with the live custom-controller expectations:

- PVC volume and volumeMount `subPath` survive reconciliation.
- Pod template labels and safe annotations needed by Sandbox/Istio survive.
- Primary and platform-token sidecar containers, mounts, security contexts, and
  emptyDir/Secret volumes survive.
- ImagePullSecrets, service account token disablement, affinity, tolerations,
  NFS/RWX mounts, supplemental groups, init containers, and ephemeral-storage
  settings survive.
- Notebook stop/start and VirtualService base path behave correctly.

Any failed row blocks retirement of the custom build. Patch against 1.11.0 and
rerun every row; do not cherry-pick an opaque 1.10 binary.

## Gate G: GPU

- A `g4dn.xlarge` node scales from zero within the allowed wait window.
- NVIDIA device plugin becomes Ready and advertises one GPU.
- GPU Notebook schedules only to the GPU group and sees the device/runtime.
- GPU image is AMD64-compatible and pulls from target registry.
- Stop/delete allows node scale-down and does not leave EBS attachment.
- API does not offer p4d/p5 unless matching target capacity exists.

## Gate H: data restore

- Restore one representative 10 GiB snapshot/copy to an isolated namespace.
- Compare path/size/mode/checksum manifest with source.
- Open representative notebooks and execute a harmless read/write/save cycle.
- Restore the full test DB and reconcile Profile/Notebook/booking invariants.
- Perform an actual rollback rehearsal, not just a written review.

## Gate I: resilience and operations

- Restart each controller and one Istio/OAuth pod; user service recovers.
- Drain a general node and validate controller rescheduling and EBS behavior.
- Expire/reissue a test TLS certificate.
- Confirm metrics, logs, alerts, audit logs, and cost metrics cover the target.
- Run a modest concurrent CPU Notebook create/stop/delete load; watch API rate
  limit, scheduler, webhook, EBS quota, node pressure, and controller queues.
- No unexplained warning events remain for 30 minutes after the run.

## Production approval criteria

All gates pass, every Critical/High risk has evidence and an owner, database/PVC
restore time fits the maintenance window, rollback was rehearsed, secrets were
rotated, and the target release commit/images are immutable and recorded.
