# Current-state inventory

This is a sanitized snapshot. It intentionally excludes Secret values,
ConfigMap credential values, user owner identifiers, cloud volume IDs, and
kubeconfig data.

## Evidence and source precedence

The effective sources are, in descending order:

1. Live Kubernetes and EKS API state collected on 2026-08-10.
2. The operations manifest tree at `/home/dev-user/sandbox-connect-api/infra`.
3. The application repository at
   `/home/dev-user/naveen/sandbox-connect-api`, branch `stable/v2.3`, commit
   `dcfe7cce4fde27569217bf58986592034a4289f4`.

The repository working tree was clean before this dossier was added.

### Critical environment drift

The repository `infra/` is **not** a copy of the live EKS deployment. It is a
different environment definition:

| Concern | Live EKS/operations tree | Repository `infra/` |
|---|---|---|
| PVC class | `ebs-csi-storage-class` | `ceph-block` |
| CPU scheduling | `t3a.medium` | logical label/value `cpu` |
| GPU scheduling | `g4dn.xlarge` node group; template also lists larger types | logical values `gpu`, `aic` |
| Shared storage | no required shared RWX workspace in live template | pre-existing `workspace` RWX PVC plus private NFS mounts |
| Registry/images | AWS ECR and TGDex images | CBR private registry/images |
| Host/Keycloak | `v2.dev.*` endpoints | `*.cbr-iisc.ac.in` endpoints |

There are 19 non-secret file differences across the two infrastructure trees,
plus files unique to each tree. Do not deploy repository `infra/` to the EKS
test cluster without producing an environment-specific overlay.

## Cluster and EKS baseline

| Item | Observed state |
|---|---|
| EKS cluster | `dev-eks-cluster`, `ap-south-1`, platform `eks.19` |
| Kubernetes API | `v1.35.6-eks-8f14419` |
| API endpoint | public and private enabled; public CIDR currently `0.0.0.0/0` |
| Authentication mode | EKS access API (`API`) |
| Control-plane logs | API, audit, authenticator, controller-manager, scheduler all disabled |
| Network | IPv4; service CIDR `172.20.0.0/16`; AWS VPC CNI 1.21.1 |
| Compute mode | EKS Auto Mode disabled; managed node groups are primary |
| Zones | 19 nodes in `ap-south-1a`, 21 in `ap-south-1b` |
| Worker architecture | 40/40 `linux/amd64` |
| Worker OS | Amazon Linux 2023 |

### Managed node groups

| Node group | Type | Desired/min/max | Disk | Notes |
|---|---:|---:|---:|---|
| `infisicle-node-group` | `t3a.medium` | 30/1/31 | 50 GiB | General workload pool |
| `t3a-large-nodeGroup` | `t3a.large` | 7/1/7 | 50 GiB | General larger pool |
| `infisical-dedicated` | `t3a.medium` | 3/2/4 | 50 GiB | `workload=infisical` taint/label |
| `gpu-nodeGroup` | `g4dn.xlarge` | 0/0/1 | 100 GiB | NVIDIA AL2023 image; no live GPU node |

The Sandbox API advertises `g4dn.xlarge`, `p4d.24xlarge`, and `p5.48xlarge`,
but EKS defines only a `g4dn.xlarge` GPU node group. Requests for the other two
types cannot schedule until target capacity is explicitly added.

Three Karpenter/Auto Mode resources exist but are not the main capacity path:
`general-purpose` is not ready with zero nodes and `system` reports one node.

### EKS-managed add-ons

The live cluster manages VPC CNI, EBS CSI, EFS CSI, Mountpoint for S3 CSI,
cert-manager, CoreDNS, node monitoring, pod identity, external-dns, kube-proxy,
metrics-server, and snapshot-controller as EKS add-ons. Key observed versions:

- EBS CSI 1.57.1, EFS CSI 2.3.1, S3 CSI 2.7.0.
- cert-manager 1.20.1 EKS build.
- CoreDNS 1.13.2, metrics-server 0.8.1, kube-proxy 1.35.0.
- snapshot-controller 8.5.0, but **no VolumeSnapshotClass exists**.

## Current Kubeflow footprint

The live platform was installed from the 1.10 branch as a selective component
installation. Exact workload images establish the effective version:

| Component | Live image/version | Target |
|---|---|---|
| PodDefaults webhook | `poddefaults-webhook:v1.10.0` | Dashboard 2.0.0 bundle |
| Jupyter Web App | `jupyter-web-app:v1.10.0` | Notebooks 1.11.0 |
| Notebook Controller | custom `v1.10-with-subpath-v2` | upstream Notebooks 1.11.0, pending behavior test |
| Profiles/KFAM | `kfam:v1.10.0`, `profile-controller:v1.10.0` | Dashboard 2.0.0 |
| Dex | 2.41.1 | 2.45.1 |
| OAuth2 Proxy | 7.7.1, two replicas | 7.15.2 |
| Istio | 1.26.1 with Istio CNI | 1.30.1 with Istio CNI |
| cert-manager | EKS 1.20.1 | retain EKS ownership; upstream expects 1.20.2 |

All active Kubeflow/Dex/OAuth/Istio pods were ready with zero restarts when
sampled. Old terminal pods remain in `kubeflow`; three were evicted for node
ephemeral-storage pressure and two completed normally. These are cleanup and
node-sizing signals, not active controller failures.

### Installed Kubeflow APIs

Only these Kubeflow CRDs exist:

- `notebooks.kubeflow.org`: serves `v1`, `v1alpha1`, `v1beta1`; stores `v1`.
- `profiles.kubeflow.org`: serves `v1`, `v1beta1`; stores `v1`.
- `poddefaults.kubeflow.org`: serves/stores `v1alpha1`.

There are no Pipelines, KServe, Katib, Training, Trainer, Tensorboard, PVC
Viewer, Spark, Knative, or Model Registry CRDs. There is no Central Dashboard
deployment. The target Dashboard/Notebooks bundles will add Dashboard, Volumes,
Tensorboards, and PVC Viewer as new core notebook UI capabilities.

### Tenant and notebook data

| Object | Count/state |
|---|---|
| Profiles | 78, all User-owned |
| Creation cohort | 53 May 2026, 19 June, 6 July |
| Profile namespaces | Istio injection enabled; no Pod Security label |
| Notebook CRs | 4, all returned through `kubeflow.org/v1` and stopped |
| Notebook PVCs | 4 × 10 GiB on `ebs-csi-storage-class` |
| All cluster PVCs | 49: 48 EBS, 1 S3; 47 Bound, 2 Pending |

Exact tenant names and owner identifiers are not committed here. Use the
private inventory script immediately before migration.

### Notebook behavior and Sandbox Connect contracts

- Notebook Controller culling is enabled with `CULL_IDLE_TIME=360` minutes and
  `IDLENESS_CHECK_PERIOD=10` minutes.
- Istio mode is enabled with gateway `kubeflow/kubeflow-gateway`.
- The public base is `https://v2.dev.sandbox.iudx.io/kubeflow`.
- Sandbox Connect creates and manipulates Notebook resources through
  `kubeflow.org/v1beta1`; Profile operations use `kubeflow.org/v1`.
- The target 1.11.0 Notebook CRD still serves `v1beta1` and stores `v1`, so no
  application code change is required for initial migration.
- The worker injects a platform-token sidecar and the API probes `GET /readyz`
  on port 8081. A root-namespace Istio AuthorizationPolicy named
  `allow-platform-token-readyz` is required.
- The service account `sandbox/notebook-manager-sa` can create Notebook and
  Profile CRs and Secrets cluster-wide. Reproduce the repository RBAC, then
  re-check it with `kubectl auth can-i`.

The current custom Notebook Controller is described only by its image tag.
Repository notes say upstream 1.10 discarded Notebook template metadata and a
custom build was needed for volume/subPath behavior. Treat replacement with
upstream 1.11.0 as an unproven behavioral change until CPU, GPU, PVC, subPath,
metadata, and token-sidecar tests pass.

## Authentication and routing

- NGINX Ingress terminates TLS and routes `v2.dev.sandbox.iudx.io` to the Istio
  ingress gateway.
- Kubeflow is hosted below `/kubeflow`; Jupyter is `/kubeflow/jupyter`, KFAM is
  `/kubeflow/kfam`, Dex is `/kubeflow/dex`, and OAuth2 is `/kubeflow/oauth2`.
- Dex issuer: `https://v2.dev.sandbox.iudx.io/kubeflow/dex`.
- Dex connector: OIDC to Keycloak realm `iudx-v2`, client
  `kubeflow-oidc-authservice`, scopes `openid profile email offline_access`.
- OAuth2 Proxy uses PKCE S256, relative redirects, a 24-hour cookie, forwards
  authorization and x-auth-request headers, and skips the provider button.
- Istio has mesh-wide deny-by-default plus external-auth/JWT policies. Each
  Profile namespace has `ns-owner-access-istio`.

Upstream defaults assume root paths (`/dex`, `/oauth2`, `/jupyter`). The safest
test configuration is a dedicated hostname at the root path. Preserving the
shared-host `/kubeflow` prefix requires explicit patches to all routes, issuers,
redirects, logout URL, forwarded prefixes, and authorization exclusions.

## Storage and backup posture

Velero 1.14.0 with AWS plugin 1.13.0 is installed. It has a daily all-namespace
Secret/ConfigMap schedule and namespace-specific PVC schedules for other
services. It has **no schedule for Profile namespaces**, no node-agent/FS-backup
container, and there is no VolumeSnapshotClass. Therefore the four notebook
PVCs are not demonstrated to be recoverable. The existence of the EKS snapshot
controller does not by itself provide snapshots.

The Sandbox Connect PostgreSQL data is also required for a coherent restore:
it maps users, notebooks, bookings, lifecycle events, and profiles. Decide
whether the new cluster uses a cloned database or the same external database;
never allow old and new lifecycle workers to write concurrently.

## Admission and security controls

- Kyverno 1.18.1 is installed.
- `require-resource-limits` is Enforce for every Pod and its generated
  Deployment/StatefulSet/Job/CronJob rules. It requires explicit CPU and memory
  limits on every container.
- `disallow-privileged-containers` is Audit outside `kube-system`.
- Current event history includes resource-limit policy violations in Kubeflow
  and Sandbox namespaces.
- No native ValidatingAdmissionPolicy is installed.
- Current namespaces do not enforce Pod Security labels. Upstream 26.03.1 sets
  restricted for system namespaces and baseline for user namespaces.

The live worker template lacks CPU/memory limits on its platform-token sidecar;
new Notebook Pods can be rejected by the enforced Kyverno rule. Correct the
template before test creation or install a narrowly reviewed exception.

## Baseline test health

The relevant repository test command was run on 2026-08-10:

```text
go test ./cmd/api ./cmd/worker ./cmd/cron/slot-lifecycle ./cmd/cron/profile-credit-sync
```

Worker tests passed and slot-lifecycle has no test files. Two pre-existing test
failures remain:

- `cmd/api`: `TestDetermineNotebookState/nil_k8sSpec,_event_empty`.
- `cmd/cron/profile-credit-sync`: two `TestNormalizeValue` epsilon cases.

Do not attribute these failures to Kubeflow 26.03.1. Record and either fix or
formally accept them before using a green test suite as a migration gate.
