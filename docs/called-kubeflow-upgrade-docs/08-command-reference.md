# Operations command reference

These commands are read-only unless marked otherwise. Always set and inspect the
context first.

## Context guard

```bash
export TARGET_CONTEXT='REPLACE_WITH_NEW_CLUSTER_CONTEXT'
kubectl config get-contexts
kubectl --context "$TARGET_CONTEXT" cluster-info
```

## Versions and ownership

```bash
kubectl --context "$TARGET_CONTEXT" version -o yaml
kubectl --context "$TARGET_CONTEXT" get nodes \
  -o custom-columns=NAME:.metadata.name,ARCH:.status.nodeInfo.architecture,VERSION:.status.nodeInfo.kubeletVersion,TYPE:.metadata.labels.node\.kubernetes\.io/instance-type
kubectl --context "$TARGET_CONTEXT" get crd \
  -o custom-columns=NAME:.metadata.name,VERSIONS:.spec.versions[*].name,STORED:.status.storedVersions
kubectl --context "$TARGET_CONTEXT" get deploy,statefulset,daemonset -A \
  -o custom-columns=KIND:.kind,NS:.metadata.namespace,NAME:.metadata.name,IMAGES:.spec.template.spec.containers[*].image
```

## Kubeflow health

```bash
kubectl --context "$TARGET_CONTEXT" get pod -n istio-system
kubectl --context "$TARGET_CONTEXT" get pod -n auth
kubectl --context "$TARGET_CONTEXT" get pod -n oauth2-proxy
kubectl --context "$TARGET_CONTEXT" get pod -n kubeflow
kubectl --context "$TARGET_CONTEXT" get profile
kubectl --context "$TARGET_CONTEXT" get notebook -A
kubectl --context "$TARGET_CONTEXT" get gateway,virtualservice,authorizationpolicy,requestauthentication -A
```

## Storage

```bash
kubectl --context "$TARGET_CONTEXT" get storageclass,volumesnapshotclass
kubectl --context "$TARGET_CONTEXT" get pvc,volumesnapshot -A
kubectl --context "$TARGET_CONTEXT" get events -A \
  --field-selector type=Warning --sort-by=.metadata.creationTimestamp
```

## Sandbox RBAC

```bash
kubectl --context "$TARGET_CONTEXT" auth can-i \
  --as=system:serviceaccount:sandbox:notebook-manager-sa \
  create notebooks.kubeflow.org --all-namespaces
kubectl --context "$TARGET_CONTEXT" auth can-i \
  --as=system:serviceaccount:sandbox:notebook-manager-sa \
  create profiles.kubeflow.org --all-namespaces
kubectl --context "$TARGET_CONTEXT" auth can-i \
  --as=system:serviceaccount:sandbox:notebook-manager-sa \
  create secrets --all-namespaces
```

## Safe rendering and diff

```bash
kubectl kustomize "$KF_DIST_DIR/overlays/sandbox-connect" >/tmp/kubeflow-rendered.yaml
kubectl --context "$TARGET_CONTEXT" apply --dry-run=server --server-side \
  --field-manager=kubeflow-upgrade -f /tmp/kubeflow-rendered.yaml
kubectl --context "$TARGET_CONTEXT" diff --server-side \
  --field-manager=kubeflow-upgrade -f /tmp/kubeflow-rendered.yaml
```

`apply --dry-run=server` and `diff` do not persist resources but invoke API
validation/admission. Run them only on the target context. Review all deletes,
immutable-field replacements, namespace labels, CRDs, webhooks, ClusterRoles,
and EKS-owned resources before a real apply.

## Never run casually

- Do not delete `profiles.kubeflow.org` or Profile namespaces.
- Do not apply `--prune` without a reviewed ownership label/allowlist and dry
  run.
- Do not use `--force-conflicts` on EKS add-ons or unknown field managers.
- Do not export Secret or ConfigMap values into this repository.
- Do not apply the 26.03-to-next dashboard cleanup commands to this fresh target
  cluster; they are for a different in-place upgrade path.
- Do not delete the independent live `minio` namespace. The upstream
  MinIO-to-SeaweedFS note concerns Kubeflow Pipelines, which is not installed in
  this platform.
