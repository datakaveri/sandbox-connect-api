# Uninstalling Kubeflow

This guide provides instructions for completely removing Kubeflow and its components from your Kubernetes cluster. Components should be removed in reverse order of installation to ensure proper cleanup.

## Uninstallation Steps

### 1. Remove User Namespaces
```bash
kustomize build common/user-namespace/base | kubectl delete -f -
```

### 2. Remove Profiles & KFAM
```bash
kustomize build apps/profiles/upstream/overlays/kubeflow | kubectl delete -f -
```

### 3. Remove Notebooks
```bash
# Remove Jupyter Web Application
kustomize build apps/jupyter/jupyter-web-app/upstream/overlays/istio | kubectl delete -f -

# Remove Notebook Controller
kustomize build apps/jupyter/notebook-controller/upstream/overlays/kubeflow | kubectl delete -f -
```

### 4. Remove Admission Webhook
```bash
kustomize build apps/admission-webhook/upstream/overlays/cert-manager | kubectl delete -f -
```

### 5. Remove Kubeflow Istio Resources
```bash
kustomize build common/istio-1-24/kubeflow-istio-resources/base | kubectl delete -f -
```

### 6. Remove Kubeflow Roles
```bash
kustomize build common/kubeflow-roles/base | kubectl delete -f -
```

### 7. Remove Network Policies
```bash
kustomize build common/networkpolicies/base | kubectl delete -f -
```

### 8. Remove Kubeflow Namespace
```bash
kustomize build common/kubeflow-namespace/base | kubectl delete -f -
```

### 9. Remove Dex
```bash
kustomize build common/dex/overlays/oauth2-proxy | kubectl delete -f -
```

### 10. Remove OAuth2 Proxy
```bash
# If you used Option 1 (m2m-dex-only)
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl delete -f -
```

### 11. Remove Istio
```bash
# Remove Istio installation
kustomize build common/istio-1-24/istio-install/overlays/oauth2-proxy | kubectl delete -f -

# Remove Istio namespace
kustomize build common/istio-1-24/istio-namespace/base | kubectl delete -f -

# Remove Istio CRDs
kustomize build common/istio-1-24/istio-crds/base | kubectl delete -f -
```

### 12. Remove Cluster Issuer
```bash
kustomize build common/cert-manager/kubeflow-issuer/base | kubectl delete -f -
```
