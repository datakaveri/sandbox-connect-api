#!/usr/bin/env bash
set -euo pipefail

LIVE_CONTEXT='arn:aws:eks:ap-south-1:135663523795:cluster/dev-eks-cluster'
TARGET_CONTEXT="${TARGET_CONTEXT:-}"

if [[ -z "$TARGET_CONTEXT" ]]; then
  echo 'Set TARGET_CONTEXT explicitly.' >&2
  exit 2
fi
if [[ "$TARGET_CONTEXT" == "$LIVE_CONTEXT" ]]; then
  echo 'Refusing to run target preflight against the live context.' >&2
  exit 2
fi

for tool in kubectl jq; do
  command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 2; }
done

echo "Target context: $TARGET_CONTEXT"
kubectl --context "$TARGET_CONTEXT" cluster-info

server_minor="$(kubectl --context "$TARGET_CONTEXT" version -o json | jq -r '.serverVersion.minor' | tr -cd '0-9')"
if [[ "$server_minor" != '35' && "$server_minor" != '36' ]]; then
  echo "FAIL: target Kubernetes minor is $server_minor; qualify 1.35 or 1.36." >&2
  exit 1
fi

non_amd64="$(kubectl --context "$TARGET_CONTEXT" get node -o json | jq '[.items[] | select(.status.nodeInfo.architecture != "amd64")]|length')"
if [[ "$non_amd64" != '0' ]]; then
  echo "FAIL: $non_amd64 target nodes are not amd64." >&2
  exit 1
fi

echo 'Storage classes:'
kubectl --context "$TARGET_CONTEXT" get storageclass

if ! kubectl --context "$TARGET_CONTEXT" get storageclass ebs-csi-storage-class >/dev/null 2>&1; then
  echo 'WARN: ebs-csi-storage-class is absent; update Sandbox templates or create it.' >&2
fi
if ! kubectl --context "$TARGET_CONTEXT" get volumesnapshotclass >/dev/null 2>&1; then
  echo 'WARN: VolumeSnapshot CRD/class is unavailable; migration restore is not ready.' >&2
elif [[ "$(kubectl --context "$TARGET_CONTEXT" get volumesnapshotclass -o name | wc -l)" -eq 0 ]]; then
  echo 'WARN: no VolumeSnapshotClass exists; migration restore is not ready.' >&2
fi

for namespace in cert-manager ingress-nginx; do
  if ! kubectl --context "$TARGET_CONTEXT" get namespace "$namespace" >/dev/null 2>&1; then
    echo "WARN: namespace $namespace is absent." >&2
  fi
done

if kubectl --context "$TARGET_CONTEXT" get clusterpolicy require-resource-limits >/dev/null 2>&1; then
  echo 'WARN: enforced resource-limit policy detected; upstream manifests need patches/exception.' >&2
fi

echo 'Node summary:'
kubectl --context "$TARGET_CONTEXT" get node \
  -o custom-columns=NAME:.metadata.name,KUBELET:.status.nodeInfo.kubeletVersion,ARCH:.status.nodeInfo.architecture,INSTANCE:.metadata.labels.node\.kubernetes\.io/instance-type,ZONE:.metadata.labels.topology\.kubernetes\.io/zone,GPU:.status.capacity.nvidia\.com/gpu

echo 'Preflight completed. Resolve every WARN before production qualification.'
