# Implementation runbook

Commands in this runbook are examples for the **new test cluster**. Set an
explicit context on every command. Never rely on whichever context happens to
be current.

## Phase 0: identify source and target

```bash
export SOURCE_CONTEXT='arn:aws:eks:ap-south-1:135663523795:cluster/dev-eks-cluster'
export TARGET_CONTEXT='REPLACE_WITH_NEW_CLUSTER_CONTEXT'

test -n "$TARGET_CONTEXT"
test "$TARGET_CONTEXT" != "$SOURCE_CONTEXT"
kubectl --context "$SOURCE_CONTEXT" version
kubectl --context "$TARGET_CONTEXT" version
```

Run `scripts/preflight-new-cluster.sh` with `TARGET_CONTEXT` set. Archive its
output with the test evidence.

## Phase 1: pin upstream and create the environment overlay

```bash
export KF_DIST_DIR="$PWD/community-distribution"
git clone https://github.com/kubeflow/community-distribution.git "$KF_DIST_DIR"
git -C "$KF_DIST_DIR" checkout --detach f09f3eeaa25cc852665f460497a42b7fc68639ac

cp -R docs/called-kubeflow-upgrade-docs/overlay-template \
  "$KF_DIST_DIR/overlays/sandbox-connect"
```

Read the overlay README. Replace every `REPLACE_...` marker in a private copy
of the active overlay. The Secret generators intentionally replace upstream
demonstration credentials; never commit the resolved copy. Review the optional
Ingress example separately before adding it to the Kustomization. Keep the
upstream checkout pristine except for `overlays/sandbox-connect`; commit the
unresolved overlay in your platform configuration repository or fork.

Render locally with restrictive permissions. After credentials are resolved, the
rendered file contains base64-encoded Kubernetes Secret data and is sensitive:

```bash
umask 077
kubectl kustomize "$KF_DIST_DIR/overlays/sandbox-connect" >/tmp/kubeflow-rendered.yaml
rg -n 'REPLACE_|CHANGEME|12341234|user@example.com' /tmp/kubeflow-rendered.yaml
```

The `rg` command must return no placeholder or known demonstration credential.
Do not print Secret payload lines during review. Inspect the non-Secret object and
image list before apply, then securely remove the rendered file after installation
evidence has been captured.

## Phase 2: establish the target cluster baseline

The target must have working DNS, NGINX (or an approved alternative), EBS CSI,
snapshot-controller, metrics-server, and cert-manager before Kubeflow.

Because EKS owns cert-manager, confirm it rather than applying the upstream
base:

```bash
kubectl --context "$TARGET_CONTEXT" -n cert-manager get deploy,pod
kubectl --context "$TARGET_CONTEXT" wait --for=condition=Available \
  deployment/cert-manager deployment/cert-manager-webhook \
  deployment/cert-manager-cainjector -n cert-manager --timeout=300s
```

Create a CSI snapshot class through the cluster platform/IaC and perform a
throwaway snapshot/restore test. A snapshot-controller alone is insufficient.

If the target has the live Kyverno `require-resource-limits` policy, stop here.
Render the overlay and add explicit limits for every container, or create a
time-bounded PolicyException scoped only to the Kubeflow bootstrap resources.
Record approval and remove the exception after resource patches are complete.
Never disable all admission controls.

## Phase 3: configure Keycloak and secret delivery

For a dedicated root-path hostname, configure the existing Keycloak realm with:

- Client ID `kubeflow-oidc-authservice`.
- Standard authorization-code flow; PKCE S256.
- Callback `https://TARGET_HOST/dex/callback`.
- Post-logout redirect `https://TARGET_HOST/oauth2/sign_out` or the exact URL
  selected by the Dashboard/OAuth configuration.
- Scopes `openid profile email offline_access`; add groups only if the mapping
  is defined and tested.

Create the connector credential directly from the approved secret manager. Do
not write it to shell history or a manifest. One safe pattern is an External
Secret. If a manual Secret is unavoidable, use an interactive stdin/file flow
and remove the temporary file securely after creation.

Dex already reads `KEYCLOAK_CLIENT_ID` and `KEYCLOAK_CLIENT_SECRET` from the
`keycloak-dex-connector` Secret referenced by the overlay. Supply one shared OIDC
client secret to the Dex and OAuth2 Proxy generators and an independent 32-byte
OAuth cookie secret through the approved private build/secret-delivery process.
Verify values appear only in Secret objects, never a ConfigMap. Remember that a
resolved Kustomize render is secret-bearing even though values are base64 encoded.

## Phase 4: install shared services and Kubeflow core

Use server-side apply because upstream does. The first pass can fail while CRDs
and webhooks establish; retry only after reviewing the error.

```bash
for attempt in 1 2 3 4 5; do
  if kubectl kustomize "$KF_DIST_DIR/overlays/sandbox-connect" | \
     kubectl --context "$TARGET_CONTEXT" apply --server-side \
       --field-manager=kubeflow-upgrade -f -; then
    break
  fi
  test "$attempt" -lt 5
  sleep 20
done
```

Do not add `--force-conflicts` automatically. If there is a field conflict,
identify the existing manager and resolve ownership. Force only a reviewed
Kubeflow-owned resource, never an EKS add-on.

Wait in dependency order:

```bash
kubectl --context "$TARGET_CONTEXT" wait --for=condition=Established \
  crd/notebooks.kubeflow.org crd/profiles.kubeflow.org \
  crd/poddefaults.kubeflow.org --timeout=180s

kubectl --context "$TARGET_CONTEXT" -n istio-system wait \
  --for=condition=Ready pod --all --timeout=300s
kubectl --context "$TARGET_CONTEXT" -n auth rollout status deploy/dex --timeout=300s
kubectl --context "$TARGET_CONTEXT" -n oauth2-proxy rollout status deploy/oauth2-proxy --timeout=300s
kubectl --context "$TARGET_CONTEXT" -n kubeflow rollout status deploy --timeout=600s
```

Confirm the Notebook CRD serves the compatibility version:

```bash
kubectl --context "$TARGET_CONTEXT" get crd notebooks.kubeflow.org \
  -o jsonpath='{range .spec.versions[*]}{.name}{" served="}{.served}{" storage="}{.storage}{"\n"}{end}'
```

## Phase 5: expose and test authentication

Apply an environment-specific TLS Ingress only after system pods are ready.
Use a hosts-file entry or test DNS until acceptance passes; do not move live
DNS yet.

Validate in a clean browser session:

1. Unauthenticated request redirects to OAuth2 Proxy/Dex and then Keycloak.
2. Keycloak returns to the exact registered callback without a loop.
3. Issuer, state, nonce, PKCE, cookie Secure/SameSite, and logout work.
4. A valid user header reaches Dashboard/KFAM; forged headers from outside do
   not grant access.
5. Bearer-token/API behavior works if the product relies on it.

Inspect only configuration and logs that do not print tokens.

## Phase 6: deploy Sandbox Connect in isolation

Create an EKS-specific deployment overlay from the **live inventory**, not the
repository's CBR manifests. Required adaptations include:

- `ebs-csi-storage-class` and the target node instance labels.
- Target ECR/GHCR image pull and per-Profile `registry-cred` creation.
- Target Keycloak, Sandbox API, file API, OpenCost, RabbitMQ, and ingress URLs.
- A cloned test PostgreSQL database.
- CPU/memory limits for the platform-token sidecar and every helper/init
  container to satisfy Kyverno.
- `allow-platform-token-readyz` in Istio's root namespace.
- Current culling values: enabled, 360-minute idle, 10-minute check.

Initially deploy the API with booking writes disabled or isolated. Scale
`sandbox-worker`, `slot-lifecycle`, and `profile-credit-sync` to zero until the
database clone and smoke-test Profile are ready. Then enable one controller at
a time.

Reapply `infra/rbac.yaml` from the chosen application revision and verify the
three `kubectl auth can-i` checks in `scripts/validate-install.sh`.

## Phase 7: controller contract test

Before migrating real Profiles, create a synthetic Profile and exercise
upstream Notebook Controller 1.11.0 with:

- CPU Notebook, EBS PVC, demo init, platform-token sidecar.
- Dynamic file/Git injection.
- A template with a PVC `subPath` and, if the target uses it, a pre-existing
  RWX workspace PVC.
- Pod labels/annotations required by the API and Istio.
- Stop annotation, restart, delete, and PVC retention/deletion behavior.
- GPU scale-from-zero and device availability.

Inspect the generated StatefulSet, Pod, Service, and VirtualService. If upstream
1.11.0 passes, retire the custom controller image. If it fails, document a
minimal source patch against Notebooks 1.11.0, build a signed/pinned image, and
repeat the entire suite. Do not reuse an unrebased 1.10 controller with the new
CRD.

## Phase 8: migration rehearsal and production cutover

Follow [data migration and rollback](05-data-migration-and-rollback.md). A
successful synthetic install is not a migration rehearsal. The rehearsal must
restore a database clone and at least one real-format PVC into the target, then
complete all tests in [the acceptance plan](06-acceptance-test-plan.md).
