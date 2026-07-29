# Istio: platform-token readiness access (admin apply)

These manifests let the **non-mesh `sandbox-api`** reach the
`platform-token-sidecar` readiness endpoint in **meshed notebook pods** at
`http://<podIP>:8081/readyz` in every current and future user namespace. Without
this policy, Istio returns `403` on that endpoint and every
`POST /v1/bookings/{id}/notebook-token-session` call times out after ~40s with a
`503 "Notebook token is still becoming ready"`.

## Why the worker template cannot configure this

The Kubeflow notebook-controller rebuilds the notebook pod and **discards
`spec.template.metadata` from the Notebook CR** (labels and annotations). A
`traffic.sidecar.istio.io/excludeInboundPorts: "8081"` annotation placed in the
worker notebook template therefore never reaches the pod. The live pod keeps
only the Istio-injected default `excludeInboundPorts: 15020`. The exception must
be expressed as mesh policy instead.

Only `GET /readyz` on port 8081 is exposed (status + `sessionId`, no token
material). Other methods, paths, and ports keep their existing authorization.

## Files

| File | Purpose | Needed? |
|---|---|---|
| `notebook-token-ready-authorizationpolicy.yaml` | Mesh-wide ALLOW for `GET /readyz` on port 8081 | Yes |

## Why one policy covers every namespace

The cluster config sets `meshConfig.rootNamespace: istio-system`, so an
`AuthorizationPolicy` stored there applies to workloads in every mesh namespace.
This includes namespaces created after the policy is installed.

The Kubeflow manifests also install `global-deny-all` in `istio-system`. It is an
ALLOW policy with no matching rules and implements mesh-wide deny-by-default.
The readiness policy is an additive, narrowly scoped exception for only
`GET /readyz` on port 8081.

Before applying, confirm those assumptions still hold:

```bash
kubectl get configmap istio -n istio-system -o jsonpath="{.data.mesh}"
kubectl get authorizationpolicy global-deny-all -n istio-system
```

## Rollout

Apply the single root-namespace policy:

```bash
kubectl apply -f notebook-token-ready-authorizationpolicy.yaml
```

No per-user namespace loop is required.

## Verify

```bash
# From the sandbox-api pod, the readiness port should NOT return 403 anymore:
APIPOD=$(kubectl get pod -n sandbox -l app=sandbox-api -o name | head -1)
PODIP=$(kubectl get pod -n <USER_NAMESPACE> <notebook-pod> -o jsonpath="{.status.podIP}")
kubectl exec -n sandbox "$APIPOD" -c sandbox-api -- wget -T4 -qO- "http://$PODIP:8081/readyz?sessionId=x"

# End-to-end: the API call should return 200 {"status":"ready"} quickly.
```

Existing notebooks do not need recreation. The root-namespace policy takes
effect for existing and future notebook namespaces.
