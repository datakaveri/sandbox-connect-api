# Sandbox Connect Kubeflow overlay template

Copy this entire directory to
`community-distribution/overlays/sandbox-connect` in the pinned upstream
checkout. Its relative resource paths assume that location.

The active Kustomization installs the supported notebook slice and preserves
live culling plus token-readiness policy. It deliberately:

- excludes the upstream cert-manager base because EKS owns cert-manager;
- excludes the default user namespace/static password;
- excludes Pipelines, KServe, Knative, Katib, Trainer, Spark, Hub, and
  experimental Workspaces v2;
- uses upstream root URL paths;
- does not contain a credential.

The active authentication configuration and Secret generators deliberately
contain `REPLACE_...` placeholders. Resolve every one in a private copy before
rendering or applying; never commit the resolved overlay. They delete the
upstream static-password Secret and replace the upstream publicly known OIDC
and cookie generators, preserving correct content-hash references. Create the
referenced `keycloak-dex-connector` Secret from the approved secret manager. A
dedicated target hostname at root path is assumed.

The `examples/ingress.yaml.example` file is not referenced automatically. Copy
and review it only after replacing its host, issuer, and Secret-name values.

Render with `kubectl kustomize`. The Snap-packaged standalone `kustomize` can be
confined from reading `/tmp` or other checkout paths.

If the target enforces CPU/memory limits, add reviewed resource patches for all
rendered Deployments before apply. The upstream manifests do not consistently
specify limits.

Required keys in the externally created `keycloak-dex-connector` Secret are
`KEYCLOAK_CLIENT_ID` and `KEYCLOAK_CLIENT_SECRET`.
