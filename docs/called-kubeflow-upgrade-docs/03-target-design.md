# Target design

## Version pin

Clone and detach at the immutable release commit:

```bash
git clone https://github.com/kubeflow/community-distribution.git
cd community-distribution
git checkout --detach f09f3eeaa25cc852665f460497a42b7fc68639ac
git status --short --branch
```

Expected core versions are Notebooks 1.11.0, Dashboard 2.0.0, Istio 1.30.1,
cert-manager 1.20.2 upstream compatibility, Dex 2.45.1, and OAuth2 Proxy
7.15.2. The stable release was tested in CI on Kubernetes 1.36; its release
candidates tested 1.35. The live and intended target EKS 1.35 baseline is within
that tested range.

## Ownership boundaries

| Layer | Owner/source |
|---|---|
| EKS API, VPC CNI, CoreDNS, kube-proxy, CSI, snapshot controller, cert-manager | EKS add-ons/IaC, not Kubeflow Kustomize |
| Istio, Dex, OAuth2 Proxy, Kubeflow namespace/roles | pinned Community Distribution overlay |
| Dashboard/Profile/PodDefaults and Notebooks suite | pinned Community Distribution overlay |
| TLS Ingress and DNS | cluster platform overlay/IaC |
| Keycloak realm/client | Keycloak administration/IaC; credentials delivered as Secret |
| Sandbox Connect workloads and RBAC | this repository plus an EKS-specific environment overlay |
| PostgreSQL, OpenCost, RabbitMQ, registries | platform services with separately managed credentials |

One resource must have one owner. In particular, never apply the upstream
cert-manager `base` on a cluster where EKS owns the same Deployments and CRDs.

## Component graph

```text
NGINX/TLS
    |
Istio ingress gateway -- external auth --> OAuth2 Proxy --> Dex --> Keycloak
    |
    +-- Dashboard / Profiles / PodDefaults
    +-- Jupyter / Volumes / Tensorboards UIs
    +-- per-Profile Notebook VirtualServices

Sandbox API/worker/lifecycle
    +-- Profile v1 + Notebook v1beta1 APIs
    +-- PostgreSQL metadata
    +-- EBS workspace PVCs
    +-- platform-token Secret + sidecar
    +-- OpenCost / RabbitMQ / registry integrations
```

## URL strategy

Recommended for the test cluster: use a dedicated hostname and upstream root
paths, for example `https://kubeflow-upgrade.example.test/{dex,oauth2,jupyter}`.
This minimizes auth and UI patching. Add its exact callback and logout URIs to
the Keycloak client.

Compatibility option: retain `https://v2.dev.sandbox.iudx.io/kubeflow`. This
requires coordinated patches for:

- Dex issuer, health route, VirtualService, and callback URI.
- OAuth2 Proxy issuer, login/redeem/JWKS/redirect URLs and VirtualService.
- Istio RequestAuthentication issuer and auth-policy excluded paths.
- Dashboard logout URL and base path.
- Jupyter, Volumes, Tensorboards, KFAM, and Dashboard VirtualServices and
  `x-forwarded-prefix` headers.
- Sandbox `API_KUBEFLOW_URL` and token-session URLs.
- Keycloak valid redirect and post-logout redirect URIs.

Do not mix root-path and prefixed values; OIDC issuer equality is exact.

## Capacity design

The notebook-platform slice is much smaller than the full 12.3 GiB upstream
example, but reserve at least two general nodes for system controllers and keep
them separate from user notebook capacity. For meaningful HA testing:

- Run two Istio ingress and two OAuth2 Proxy replicas across zones.
- Give controllers explicit CPU/memory requests and limits compatible with the
  target admission policy.
- Retain at least one schedulable `t3a.medium`-equivalent CPU notebook node.
- Exercise scale-from-zero of the `g4dn.xlarge` GPU group and set an adequate
  provisioning timeout in tests.
- Use EBS CSI `WaitForFirstConsumer` and a snapshot class with `Retain` deletion
  policy for migration snapshots.

Do not expose `p4d.24xlarge` or `p5.48xlarge` until EKS node groups, account
quotas, subnets, capacity, NVIDIA support, and cost controls have been tested.

## API compatibility

Target Notebook CRD versions are `v1` (storage/served), `v1alpha1` (served),
and `v1beta1` (served). Existing Sandbox Connect `v1beta1` clients therefore
remain functional. Profiles remain `v1` compatible. Apply CRDs and wait for
`Established` before starting any Sandbox controller.

After production stabilization, change worker templates, GVRs, and owner
references to `kubeflow.org/v1` in one separately tested application release.
Do not combine that code migration with the platform cutover.
