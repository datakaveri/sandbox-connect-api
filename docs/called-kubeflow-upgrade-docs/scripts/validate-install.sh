#!/usr/bin/env bash
set -euo pipefail

TARGET_CONTEXT="${TARGET_CONTEXT:-}"
LIVE_CONTEXT='arn:aws:eks:ap-south-1:135663523795:cluster/dev-eks-cluster'
if [[ -z "$TARGET_CONTEXT" ]]; then
  echo 'Set TARGET_CONTEXT explicitly.' >&2
  exit 2
fi

if [[ "$TARGET_CONTEXT" == "$LIVE_CONTEXT" ]]; then
  echo 'Refusing to run target validation against the live context.' >&2
  exit 2
fi

for crd in notebooks.kubeflow.org profiles.kubeflow.org poddefaults.kubeflow.org; do
  kubectl --context "$TARGET_CONTEXT" wait --for=condition=Established "crd/$crd" --timeout=60s
done

kubectl --context "$TARGET_CONTEXT" -n istio-system get deploy,daemonset,pod
kubectl --context "$TARGET_CONTEXT" -n auth get deploy,pod
kubectl --context "$TARGET_CONTEXT" -n oauth2-proxy get deploy,pod
kubectl --context "$TARGET_CONTEXT" -n kubeflow get deploy,pod

kubectl --context "$TARGET_CONTEXT" -n auth rollout status deploy/dex --timeout=180s
kubectl --context "$TARGET_CONTEXT" -n oauth2-proxy rollout status deploy/oauth2-proxy --timeout=180s

while read -r deployment; do
  kubectl --context "$TARGET_CONTEXT" -n kubeflow rollout status "$deployment" --timeout=300s
done < <(kubectl --context "$TARGET_CONTEXT" -n kubeflow get deploy -o name)

echo 'Notebook CRD versions:'
kubectl --context "$TARGET_CONTEXT" get crd notebooks.kubeflow.org \
  -o jsonpath='{range .spec.versions[*]}{.name}{" served="}{.served}{" storage="}{.storage}{"\n"}{end}'

echo 'Sandbox effective permissions:'
for resource in notebooks.kubeflow.org profiles.kubeflow.org secrets; do
  printf '%s: ' "$resource"
  kubectl --context "$TARGET_CONTEXT" auth can-i \
    --as=system:serviceaccount:sandbox:notebook-manager-sa \
    create "$resource" --all-namespaces
done

echo 'Kubeflow objects:'
kubectl --context "$TARGET_CONTEXT" get profile
kubectl --context "$TARGET_CONTEXT" get notebook -A
kubectl --context "$TARGET_CONTEXT" get gateway,virtualservice,requestauthentication,authorizationpolicy -A

echo 'Recent warnings:'
kubectl --context "$TARGET_CONTEXT" get event -A --field-selector type=Warning \
  --sort-by=.metadata.creationTimestamp

echo 'Read-only install validation completed. Browser/API and workload tests remain.'
