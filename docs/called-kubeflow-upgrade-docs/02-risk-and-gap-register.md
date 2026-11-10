# Risk and gap register

| ID | Severity | Finding | Required disposition |
|---|---|---|---|
| R1 | Critical | Credential material is stored in a live ConfigMap and credential-bearing manifest files exist. | Rotate affected credentials now; move them to Secrets/external secret management; scrub Git history where applicable. |
| R2 | Critical | Repo `infra/` targets a different CBR/Ceph/NFS environment. | Build an EKS-specific overlay from live facts; never deploy repo `infra/` directly. |
| R3 | Critical | Four notebook PVCs have no demonstrated backup; no snapshot class or Velero Profile-PVC schedule exists. | Create and restore-test CSI/EBS snapshots or an FS-level copy before cutover. |
| R4 | Critical | Sandbox metadata lives in PostgreSQL; dual old/new workers can mutate lifecycle state. | Clone/freeze/restore the database and guarantee a single active writer set. |
| R5 | High | Custom Notebook Controller `v1.10-with-subpath-v2` behavior is undocumented upstream. | Run the controller contract matrix; forward-port only if upstream 1.11.0 fails it. |
| R6 | High | Kyverno Enforce requires CPU/memory limits; upstream system deployments and the custom sidecar omit them. | Add resource patches and sidecar limits or a temporary, scoped exception. Do not disable Kyverno cluster-wide. |
| R7 | High | Current public `/kubeflow` prefix differs from upstream root-path defaults. | Prefer a dedicated test hostname at `/`; otherwise patch every auth/UI route and test redirects. |
| R8 | High | EKS owns cert-manager while the upstream example also installs it. | Exclude upstream cert-manager base; use only the Kubeflow issuer/policy overlay after validating EKS cert-manager compatibility. |
| R9 | High | API endpoint is publicly reachable from `0.0.0.0/0` and control-plane audit logs are disabled. | Restrict CIDRs/private access and enable EKS control-plane logs before production. |
| R10 | High | GPU API advertises p4d/p5 but only g4dn capacity exists, scaled to zero/max one. | Limit advertised types or provision matching target node groups and quotas. |
| R11 | Medium | Full upstream example would add unneeded products and persistent stores. | Use the minimal overlay; approve product expansion separately. |
| R12 | Medium | Target Notebooks bundle adds PVC Viewer, Volumes UI, and Tensorboards; Dashboard is also new. | Include explicit acceptance/security tests and menu review. |
| R13 | Medium | Live system had old pods evicted for ephemeral-storage pressure. | Set requests/limits where possible and verify node image/ephemeral capacity during load tests. |
| R14 | Medium | Upstream release support is best-effort for about six months. | Re-check stable patches before production and establish a twice-yearly qualification cadence. |
| R15 | Medium | Current CR clients use served `v1beta1`, not storage `v1`. | Keep for initial compatibility; migrate application templates and owner refs to `v1` after cutover. |
| R16 | Medium | Current automated tests have two unrelated failures. | Fix or baseline them so migration regressions can be distinguished. |
| R17 | Medium | Existing Istio route objects contain generated/legacy substitution artifacts and historical annotations. | Generate clean target resources from the pinned release and overlays; do not export/reapply live YAML verbatim. |
| R18 | Low | Terminal evicted/completed Kubeflow pods remain visible. | Clean up after confirming owning ReplicaSets; do not treat them as active outage. |

## Go/no-go ownership

Assign a named owner and evidence link for every Critical/High item. A production
cutover is no-go while any Critical row lacks a completed restore or rotation
record. The test-cluster installation may proceed with R3/R4 incomplete only if
it uses synthetic data and is clearly not treated as a migration rehearsal.
