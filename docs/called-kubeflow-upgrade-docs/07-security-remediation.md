# Security remediation

## Immediate credential response

During inventory, credential values were found in a ConfigMap-style manifest.
Assume those credentials are exposed to every subject that can read ConfigMaps
and to any location where the manifest was copied.

1. Inventory each affected AWS, RabbitMQ, Keycloak, database, registry, and
   integration credential without printing values.
2. Disable/rotate credentials at the source system.
3. Update consumers through the approved Secret manager.
4. Verify old credentials no longer authenticate.
5. Remove values from current files and Git history where tracked; coordinate
   history rewriting because it affects every clone.
6. Add secret scanning and a CI block for high-confidence credentials.

Moving the same leaked value from a ConfigMap into a Secret is not rotation.
Kubernetes Secrets are transport/storage objects, not a complete secret manager.

## Target secret architecture

- Prefer External Secrets/Secrets Store CSI/Infisical or the organization's
  existing system with EKS Pod Identity/IRSA.
- Keep cloud access on workload identity; avoid static AWS access keys.
- Store Dex OIDC client and Keycloak connector secrets separately with minimum
  readers.
- Recreate per-Profile registry credentials with scoped, short-lived tokens.
- Enable envelope encryption for Kubernetes Secret data and restrict `get/list`
  through RBAC.
- Never include Secret/ConfigMap values, kubeconfigs, database dumps, volume
  handles, tokens, or Profile owner identities in this Git directory.

The current `notebook-manager-role` has cluster-wide wildcard verbs on Notebook
and Profile objects plus broad Pod/PVC access and Secret create/update/delete.
After migration, split API, worker, lifecycle, and credit-sync service accounts
and reduce verbs/resources. Secret access should be label/name/namespace scoped
where the platform design allows it.

## EKS hardening

- Restrict the public API CIDR from `0.0.0.0/0`, or disable public access after
  private administration is working.
- Enable control-plane API, audit, authenticator, controller-manager, and
  scheduler logs with retention and alerting.
- Use EKS access entries and least privilege; review cluster-admin subjects.
- Enable GuardDuty/EKS audit analysis or the approved equivalent.
- Keep managed add-ons and node AMIs on a scheduled patch cadence.
- Use signed/pinned images and scan the custom Notebook Controller and notebook
  images for architecture and vulnerabilities.

## Workload and mesh hardening

- Keep upstream restricted Pod Security labels for system namespaces and test
  baseline/restricted policy for user namespaces.
- Add CPU/memory/ephemeral-storage requests and limits. Fix the custom token
  sidecar rather than weakening the cluster-wide rule indefinitely.
- Retain `automountServiceAccountToken: false` for notebook workloads.
- Keep non-root, no privilege escalation, RuntimeDefault seccomp, read-only root
  filesystem where possible, and drop all capabilities.
- Keep Istio mesh-wide deny by default. The token readiness exception must allow
  only `GET /readyz` on port 8081 and expose no token material.
- Review whether the sandbox API should join the mesh; if not, document and
  monitor every root-namespace exception.
- Use NetworkPolicy egress restrictions for notebooks, metadata service, token
  endpoints, registries, DNS, and approved data services.

## Authentication hardening

- Use HTTPS public issuer URLs consistently; never set insecure issuer/TLS flags
  in production.
- Require exact Keycloak redirect URIs rather than wildcards and restrict web
  origins.
- Keep authorization code + PKCE, state, nonce, Secure/HttpOnly cookies, and a
  reviewed SameSite value.
- Verify group/role claims deliberately; do not trust arbitrary headers from
  outside the Istio gateway.
- Do not log authorization headers, refresh tokens, Secret contents, connector
  configs containing values, or full token claims.

## Backup security

Encrypt EBS snapshots, Velero object storage, database dumps, and private
migration bundles with controlled KMS keys. Test cross-cluster restore access
without granting the target broad access to all source volumes. Apply retention
and delete migration artifacts after the rollback window and approval.
