# Kubeflow Subpath Reference

Use this document as a reference template when configuring Kubeflow behind a URL
subpath. Replace values in angle brackets with environment-specific values before
using the snippets.

Common placeholders:

- `/<subpath>`: the Kubeflow base path, for example `/kubeflow`
- `<kubeflow-host>`: the public Kubeflow host
- `<oidc-issuer-url>`: the external identity provider issuer URL
- `<client-id>`: the OIDC client ID
- `<client-secret>`: the OIDC client secret
- `<notebook-controller-image>`: the notebook controller image with subpath support
- `<dex-namespace>`: the namespace where Dex runs
- `<tls-secret-name>`: the TLS secret used by the ingress
- `<allowed-origin-list>`: comma-separated CORS origins, if CORS is required

## 1. Jupyter Web App VirtualService

Target folder: `apps/jupyter/jupyter-web-app/upstream/overlays/istio`

Target file: `virtual-service.yaml`

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: jupyter-web-app-jupyter-web-app
spec:
  gateways:
  - kubeflow-gateway
  hosts:
  - '*'
  http:
  - headers:
      request:
        add:
          x-forwarded-prefix: /<subpath>/jupyter
    match:
    - uri:
        exact: /<subpath>/jupyter
    - uri:
        prefix: /<subpath>/jupyter/
    rewrite:
      uri: /
    route:
    - destination:
        host: jupyter-web-app-service.$(JWA_NAMESPACE).svc.$(JWA_CLUSTER_DOMAIN)
        port:
          number: 80
```

Apply command:

```bash
kubectl apply -f apps/jupyter/jupyter-web-app/upstream/overlays/istio/virtual-service.yaml -n kubeflow
```

## 2. Notebook Controller Manager

Target folder: `apps/jupyter/notebook-controller/upstream/manager`

Target file: `manager.yaml`

```yaml
apiVersion: v1
kind: Namespace
metadata:
  labels:
    control-plane: controller-manager
  name: system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: deployment
spec:
  template:
    metadata:
      labels:
        app: notebook-controller
        kustomize.component: notebook-controller
    spec:
      containers:
      - name: manager
        image: <notebook-controller-image>
        command:
          - /manager
        env:
          - name: USE_ISTIO
            valueFrom:
              configMapKeyRef:
                name: config
                key: USE_ISTIO
          - name: ISTIO_GATEWAY
            valueFrom:
              configMapKeyRef:
                name: config
                key: ISTIO_GATEWAY
          - name: ISTIO_HOST
            valueFrom:
              configMapKeyRef:
                name: config
                key: ISTIO_HOST
          - name: CLUSTER_DOMAIN
            valueFrom:
              configMapKeyRef:
                name: config
                key: CLUSTER_DOMAIN
          - name: ENABLE_CULLING
            valueFrom:
              configMapKeyRef:
                name: config
                key: ENABLE_CULLING
          - name: CULL_IDLE_TIME
            valueFrom:
              configMapKeyRef:
                name: config
                key: CULL_IDLE_TIME
          - name: IDLENESS_CHECK_PERIOD
            valueFrom:
              configMapKeyRef:
                name: config
                key: IDLENESS_CHECK_PERIOD
          - name: NOTEBOOK_BASE_PATH 
            value: "/<subpath>/"
        imagePullPolicy: IfNotPresent
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
      serviceAccountName: service-account
```

Apply command:

```bash
kustomize build apps/jupyter/notebook-controller/upstream/overlays/kubeflow | kubectl apply -f -
kustomize build apps/jupyter/jupyter-web-app/upstream/overlays/istio | kubectl apply -f -
```


## 3. Profiles VirtualService

Target folder: `apps/profiles/upstream/overlays/kubeflow`

Target file: `virtual-service.yaml`

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: profiles-kfam
spec:
  gateways:
  - kubeflow-gateway
  hosts:
  - '*'
  http:
  - headers:
      request:
        add:
          x-forwarded-prefix: /<subpath>/kfam
    match:
    - uri:
        exact: /<subpath>/kfam
    - uri:
        prefix: /<subpath>/kfam/
    rewrite:
      uri: /kfam/
    route:
    - destination:
        host: profiles-kfam.$(PROFILES_NAMESPACE).svc.cluster.local
        port:
          number: 8081
```

Apply command:

```bash
kubectl apply -f apps/profiles/upstream/overlays/kubeflow/virtual-service.yaml -n kubeflow
```

## 4. Dex Istio VirtualService

Target folder: `common/dex/overlays/istio`

Target file: `virtual-service.yaml`

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: dex
spec:
  gateways:
  - kubeflow/kubeflow-gateway
  hosts:
  - '*'
  http:
  - match:
    - uri:
        exact: /<subpath>/dex
    - uri:
        prefix: /<subpath>/dex/
    route:
    - destination:
        host: DEX_SERVICE.DEX_NAMESPACE.svc.cluster.local
        port:
          number: 5556
```
Apply command:

```bash
kubectl apply -f common/dex/overlays/istio/virtual-service.yaml -n auth
```

## 5. Dex OAuth2 Proxy ConfigMap

Target folder: `common/dex/overlays/oauth2-proxy`

Target file: `config-map.yaml`


```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: dex
data:
  config.yaml: |
    issuer: https://<kubeflow-host>/<subpath>/dex
    storage:
      type: kubernetes
      config:
        inCluster: true
    web:
      http: 0.0.0.0:5556
    logger:
      level: "debug"
      format: text
    oauth2:
      skipApprovalScreen: true
    enablePasswordDB: false
    # staticPasswords:
    # - email: user@example.com
    #   hashFromEnv: DEX_USER_PASSWORD
    #   username: user
    #   userID: "15841185641784"
    staticClients:
    - idEnv: OIDC_CLIENT_ID
      redirectURIs: ["/<subpath>/oauth2/callback"]
      name: 'Dex Login Application'
      secretEnv: OIDC_CLIENT_SECRET
    connectors:
    - type: oidc
      id: keycloak
      name: keycloak
      config:
        issuer: <oidc-issuer-url>
        clientID: <client-id>
        clientSecret: <client-secret>
        redirectURI: https://<kubeflow-host>/<subpath>/dex/callback
        insecure: false
        insecureSkipEmailVerified: true
        userNameKey: email       
        scopes:
          - openid
          - profile
          - email
          - offline_access
```
Apply command:

```bash
kustomize build common/dex/overlays/oauth2-proxy | kubectl delete -f -
kustomize build common/dex/overlays/oauth2-proxy | kubectl apply -f -
```

## 6. OAuth2 Proxy Config

Target folder: `common/oauth2-proxy/base`

Target file: `oauth2_proxy.cfg`

```conf
provider = "oidc"
oidc_issuer_url = "https://<kubeflow-host>/<subpath>/dex"
scope = "profile email offline_access openid"
email_domains = "*"
insecure_oidc_allow_unverified_email = "true"

upstreams = [ "static://200" ]

skip_auth_routes = [
  "^/<subpath>/dex/",
]

api_routes = [
  "/<subpath>/api/",
  "/<subpath>/apis/",
  "^/<subpath>/ml_metadata",
]

skip_oidc_discovery = true
login_url = "/<subpath>/dex/auth"
redeem_url = "http://dex.<dex-namespace>.svc.cluster.local:5556/<subpath>/dex/token"
oidc_jwks_url = "http://dex.<dex-namespace>.svc.cluster.local:5556/<subpath>/dex/keys"

skip_provider_button = true

provider_display_name = "Dex"
custom_sign_in_logo = "/custom-theme/kubeflow-logo.svg"
banner = "-"
footer = "-"

prompt = "none"

set_authorization_header = true
set_xauthrequest = true

cookie_name = "oauth2_proxy_<subpath>"
cookie_expire = "24h"
cookie_refresh = 0

code_challenge_method = "S256"

redirect_url = "/<subpath>/oauth2/callback"
relative_redirect_url = true

```

Apply command:
```bash
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl delete -f -
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl apply -f -
```

## 7. OAuth2 Proxy VirtualService

Target folder: `common/oauth2-proxy/base`

Target file: `virtualservice.yaml`

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: oauth2-proxy
spec:
  gateways:
  - kubeflow/kubeflow-gateway
  hosts:
  - '*'
  http:
  - match:
    - uri:
        prefix: /<subpath>/oauth2/
    rewrite:
      uri: /oauth2/
    route:
    - destination:
        host: OAUTH2_PROXY_SERVICE.OAUTH2_PROXY_NAMESPACE.svc.cluster.local
        port:
          number: 80
```

Apply command:

```bash
kubectl apply -f common/oauth2-proxy/base/virtualservice.yaml -n oauth2-proxy
```

## 8. OAuth2 Proxy External Auth AuthorizationPolicy

Target folder: `common/oauth2-proxy/components/istio-external-auth`

Target file: `authorizationpolicy.istio-ingressgateway-oauth2-proxy.yaml`


```yaml
apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: istio-ingressgateway-oauth2-proxy
  namespace: istio-system
spec:
  action: CUSTOM
  provider:
    name: oauth2-proxy
  selector:
    matchLabels:
      app: istio-ingressgateway
  rules:
  # We ONLY authenticate requests that DON'T have an `Authorization` header using oauth2-proxy.
  # This is because we use RequestAuthentication to authenticate requests with an `Authorization` header.
  - when:
    - key: request.headers[authorization]
      notValues: ["*"]
    to:
    - operation:
        notPaths:
        # Exclude dex paths, otherwise users won't be able to log in.
        - /<subpath>/dex
        - /<subpath>/dex/*
        - /<subpath>/dex/**
        - /<subpath>/oauth2/*
```

Apply command:

```bash
kubectl apply -f common/oauth2-proxy/components/istio-external-auth/authorizationpolicy.istio-ingressgateway-oauth2-proxy.yaml -n istio-system
```


## 9. JWT AuthorizationPolicy

Target folder: `common/oauth2-proxy/components/istio-external-auth`

Target file: `authorizationpolicy.istio-ingressgateway-require-jwt.yaml`


```yaml
apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: istio-ingressgateway-require-jwt
  namespace: istio-system
spec:
  action: DENY
  selector:
    matchLabels:
      app: istio-ingressgateway
  rules:
  # Deny requests that don't have a verified JWT (from a RequestAuthentication)
  # Note, even user requests that have been authenticated by oauth2-proxy will have a JWT,
  # because oauth2-proxy injects a Dex JWT into the request.
  - from:
    - source:
        notRequestPrincipals: ["*"]
    to:
    - operation:
        notPaths:
        # Exclude dex paths, otherwise users won't be able to log in.
        - /<subpath>/dex
        - /<subpath>/dex/*
        - /<subpath>/dex/**
        - /<subpath>/oauth2/*
```

Apply command:

```bash
kubectl apply -f common/oauth2-proxy/components/istio-external-auth/authorizationpolicy.istio-ingressgateway-require-jwt.yaml -n istio-system
```


## 10. Dex JWT RequestAuthentication

Target folder: `common/oauth2-proxy/components/istio-external-auth`

Target file: `requestauthentication.dex-jwt.yaml`

```yaml
apiVersion: security.istio.io/v1beta1
kind: RequestAuthentication
metadata:
  name: dex-jwt
  namespace: istio-system
spec:
  selector:
    matchLabels:
      app: istio-ingressgateway
  jwtRules:
  - issuer: https://<kubeflow-host>/<subpath>/dex
    forwardOriginalToken: true
    outputClaimToHeaders:
    - header: kubeflow-userid
      claim: email
    - header: kubeflow-groups
      claim: groups
    fromHeaders:
    - name: Authorization
      prefix: "Bearer "
```

Apply command:

```bash
kustomize build common/istio-1-24/istio-install/overlays/oauth2-proxy | kubectl delete -f -
kustomize build common/istio-1-24/istio-install/overlays/oauth2-proxy | kubectl apply -f -
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl delete -f -
kustomize build common/oauth2-proxy/overlays/m2m-dex-only/ | kubectl apply -f -
```

## 11. NGINX Ingress

Target file: `ingress.yaml`

Apply command:

```bash
kubectl apply -f ingress.yaml -n istio-system
```

Use this when an NGINX ingress fronts the Istio ingress gateway and Kubeflow is
served from `/<subpath>`.

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: istio-gateway-ingress
  namespace: istio-system
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/enable-modsecurity: "false"
    nginx.ingress.kubernetes.io/enable-cors: "true"
    nginx.ingress.kubernetes.io/cors-allow-origin: "<allowed-origin-list>"
    nginx.ingress.kubernetes.io/cors-allow-methods: "GET, POST, PUT, DELETE, OPTIONS"
    nginx.ingress.kubernetes.io/cors-allow-headers: "Content-Type, Authorization, X-Request-ID"
    nginx.ingress.kubernetes.io/cors-expose-headers: "X-Request-ID, X-Execution-Time"
    nginx.ingress.kubernetes.io/global-rate-limit: "1000"
    nginx.ingress.kubernetes.io/global-rate-limit-key: $server_name
    nginx.ingress.kubernetes.io/global-rate-limit-window: 1s
    nginx.ingress.kubernetes.io/limit-burst-multiplier: "1"
    nginx.ingress.kubernetes.io/limit-connections: "150"
    nginx.ingress.kubernetes.io/limit-rps: "100"
spec:
  ingressClassName: nginx
  rules:
  - host: <kubeflow-host>
    http:
      paths:
      - path: /<subpath>
        pathType: Prefix
        backend:
          service:
            name: istio-ingressgateway
            port:
              number: 80
  tls:
  - hosts:
    - <kubeflow-host>
    secretName: <tls-secret-name>
```

