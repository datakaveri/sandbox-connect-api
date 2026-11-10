#!/usr/bin/env bash
set -euo pipefail

# This collector intentionally does not export Secret values, ConfigMap values,
# kubeconfig content, PV volume handles, or Profile owner values. Its output can
# still contain internal names and must be treated as private operational data.

SOURCE_CONTEXT="${SOURCE_CONTEXT:-$(kubectl config current-context)}"
EXPECTED_SOURCE_CONTEXT="${EXPECTED_SOURCE_CONTEXT:-}"
INVENTORY_DIR="${1:-kubeflow-live-inventory-$(date -u +%Y%m%dT%H%M%SZ)}"

if [[ -n "$EXPECTED_SOURCE_CONTEXT" && "$SOURCE_CONTEXT" != "$EXPECTED_SOURCE_CONTEXT" ]]; then
  echo "Refusing: SOURCE_CONTEXT does not match EXPECTED_SOURCE_CONTEXT" >&2
  exit 2
fi

umask 077
mkdir -p "$INVENTORY_DIR"

run() {
  local output_file="$1"
  shift
  "$@" >"$INVENTORY_DIR/$output_file"
}

printf '%s\n' "$SOURCE_CONTEXT" >"$INVENTORY_DIR/context.txt"
date -u +%Y-%m-%dT%H:%M:%SZ >"$INVENTORY_DIR/collected-at.txt"

run kubernetes-version.yaml kubectl --context "$SOURCE_CONTEXT" version -o yaml
run api-resources.txt kubectl --context "$SOURCE_CONTEXT" api-resources
run namespaces.tsv kubectl --context "$SOURCE_CONTEXT" get namespace \
  -o custom-columns=NAME:.metadata.name,STATUS:.status.phase,ISTIO:.metadata.labels.istio-injection,PSS:.metadata.labels.pod-security\.kubernetes\.io/enforce,CREATED:.metadata.creationTimestamp
run nodes.tsv kubectl --context "$SOURCE_CONTEXT" get node \
  -o custom-columns=NAME:.metadata.name,KUBELET:.status.nodeInfo.kubeletVersion,ARCH:.status.nodeInfo.architecture,OS:.status.nodeInfo.osImage,INSTANCE:.metadata.labels.node\.kubernetes\.io/instance-type,ZONE:.metadata.labels.topology\.kubernetes\.io/zone,GPU:.status.capacity.nvidia\.com/gpu,TAINTS:.spec.taints
run workloads.tsv kubectl --context "$SOURCE_CONTEXT" get deployment,statefulset,daemonset -A \
  -o custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGES:.spec.template.spec.containers[*].image
run cronjobs.tsv kubectl --context "$SOURCE_CONTEXT" get cronjob -A \
  -o custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGES:.spec.jobTemplate.spec.template.spec.containers[*].image
run crds.tsv kubectl --context "$SOURCE_CONTEXT" get crd \
  -o custom-columns=NAME:.metadata.name,GROUP:.spec.group,SCOPE:.spec.scope,VERSIONS:.spec.versions[*].name,STORED:.status.storedVersions
run pods.tsv kubectl --context "$SOURCE_CONTEXT" get pod -A \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName,IMAGES:.spec.containers[*].image
run profiles-sanitized.tsv kubectl --context "$SOURCE_CONTEXT" get profiles.kubeflow.org \
  -o custom-columns=NAME:.metadata.name,OWNER_KIND:.spec.owner.kind,CREATED:.metadata.creationTimestamp
run notebooks-sanitized.tsv kubectl --context "$SOURCE_CONTEXT" get notebooks.kubeflow.org -A \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,API:.apiVersion,READY:.status.readyReplicas,IMAGE:.spec.template.spec.containers[0].image,STOPPED:.metadata.annotations.kubeflow-resource-stopped,CREATED:.metadata.creationTimestamp
run storage.tsv kubectl --context "$SOURCE_CONTEXT" get storageclass,volumesnapshotclass,pvc -A -o wide
run istio-security.tsv kubectl --context "$SOURCE_CONTEXT" get \
  gateway.networking.istio.io,virtualservice.networking.istio.io,requestauthentication.security.istio.io,authorizationpolicy.security.istio.io,peerauthentication.security.istio.io \
  -A -o custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name
run admission.tsv kubectl --context "$SOURCE_CONTEXT" get \
  mutatingwebhookconfiguration,validatingwebhookconfiguration,validatingadmissionpolicy,validatingadmissionpolicybinding \
  -o custom-columns=KIND:.kind,NAME:.metadata.name
run kyverno-policies.tsv kubectl --context "$SOURCE_CONTEXT" get clusterpolicy,policy -A \
  -o custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,ACTION:.spec.validationFailureAction
run recent-warnings.txt kubectl --context "$SOURCE_CONTEXT" get event -A \
  --field-selector type=Warning --sort-by=.metadata.creationTimestamp
run config-and-secret-names.json kubectl --context "$SOURCE_CONTEXT" get configmap,secret -A -o json

jq '{items:[.items[] | {
      kind:.kind,
      namespace:.metadata.namespace,
      name:.metadata.name,
      type:(.type // null),
      keys:((.data // {}) | keys)
    }]}' "$INVENTORY_DIR/config-and-secret-names.json" \
  >"$INVENTORY_DIR/config-and-secret-key-names.json"
rm "$INVENTORY_DIR/config-and-secret-names.json"

{
  echo "notebooks/create: $(kubectl --context "$SOURCE_CONTEXT" auth can-i --as=system:serviceaccount:sandbox:notebook-manager-sa create notebooks.kubeflow.org --all-namespaces)"
  echo "profiles/create: $(kubectl --context "$SOURCE_CONTEXT" auth can-i --as=system:serviceaccount:sandbox:notebook-manager-sa create profiles.kubeflow.org --all-namespaces)"
  echo "secrets/create: $(kubectl --context "$SOURCE_CONTEXT" auth can-i --as=system:serviceaccount:sandbox:notebook-manager-sa create secrets --all-namespaces)"
} >"$INVENTORY_DIR/sandbox-effective-rbac.txt"

if command -v helm >/dev/null 2>&1; then
  helm --kube-context "$SOURCE_CONTEXT" list -A >"$INVENTORY_DIR/helm-releases.tsv"
fi

if [[ -n "${EKS_CLUSTER_NAME:-}" && -n "${AWS_REGION:-}" ]] && command -v aws >/dev/null 2>&1; then
  aws eks describe-cluster --name "$EKS_CLUSTER_NAME" --region "$AWS_REGION" \
    --query 'cluster.{version:version,status:status,platformVersion:platformVersion,resourcesVpcConfig:resourcesVpcConfig,kubernetesNetworkConfig:kubernetesNetworkConfig,accessConfig:accessConfig,upgradePolicy:upgradePolicy,computeConfig:computeConfig,storageConfig:storageConfig,logging:logging}' \
    --output json >"$INVENTORY_DIR/eks-cluster.json"
  aws eks list-nodegroups --cluster-name "$EKS_CLUSTER_NAME" --region "$AWS_REGION" \
    --output json >"$INVENTORY_DIR/eks-nodegroups.json"
  aws eks list-addons --cluster-name "$EKS_CLUSTER_NAME" --region "$AWS_REGION" \
    --output json >"$INVENTORY_DIR/eks-addons.json"
fi

cat >"$INVENTORY_DIR/README.txt" <<'EOF'
PRIVATE OPERATIONAL INVENTORY

No Secret or ConfigMap values are intentionally present. The bundle still
contains internal names, topology, URLs in event messages, images, and tenant
namespace identifiers. Keep it out of Git, encrypt it in transit/at rest, and
delete it after the migration retention window.
EOF

echo "Inventory written to: $INVENTORY_DIR"
