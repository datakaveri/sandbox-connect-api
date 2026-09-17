# Public-release audit

Audit date: 2026-09-17. Source branch: `stable/v2.3` at
`69f3c122cae7f4bfee69d40a52bf03e78bc0e617`, plus the uncommitted cleanup described below.

**Release readiness: blocked by historical credential findings and missing GitHub write
authentication.** GitHub's public metadata API was checked on 2026-09-17 and reports this
repository's visibility as **public** (`private: false`). No visibility change was made during
this work. Treat the historical credential values as publicly exposed pending owner review.
A clean working tree does not establish that existing history is safe. Credential validity and
revocation status have not been checked against live systems.

## Scope and method

- Refreshed remote branches and tags with `git fetch --all --tags` and confirmed the local
  stable branch matches `origin/stable/v2.3`.
- Used Gitleaks 8.30.1, downloaded from its official release and verified against the published
  SHA-256 checksum. Reports and tool installations are outside the repository in `/tmp`.
- Scanned the current directory with full output redaction and without honoring inline
  `gitleaks:allow` markers. The initial current-directory scan reported no findings.
- Scanned reachable history across all local branch, remote-tracking, and tag refs using
  `--log-opts=--all`. Gitleaks processed 190 commits with changes and reported five matches.
- Supplemented the scan by reading all 1,186 reachable Git blobs across 211 reachable commits,
  including historical files and the compiled executable. Checked private-key markers,
  recognizable access-key/token formats, credential-bearing URLs, and credential assignments
  in environment and manifest files. Reviewed Kubernetes Secret representations and Postman
  credential fields. Gitleaks also recursively decodes supported encodings by default.
- Checked the affected files at every fetched branch/tag tip: neither historical credential
  value below remains in those files at any tip.

No credential values are reproduced in this document or the scanner reports. No authentication
requests were made using discovered credentials.

## Historical credential findings

| Finding | File at the introducing commit | Commit | Line | Assessment |
|---|---|---|---|---|
| Confidential Keycloak notebook-client secret | `infra/platform-token-sidecar/sandbox-notebook-client.json` | `1a417652efb50e456bfd6752ccb5f83b59f611eb` | 9 | Literal credential-shaped value, not a marked placeholder; treat as exposed pending owner review |
| Private-registry password | `infra/api/secret.yaml` | `315efc37a9ccbffcac86e4b2459e70e86158507a` | 15 | Literal credential-shaped value, not a marked placeholder; treat as exposed pending owner review |

Both commits remain ancestors of `stable/v2.3`. The Keycloak finding is also reachable through
`origin/dev`, `origin/feature/evaluation-argo-service`, and `v2.3.RC1`. The registry finding is
reachable through multiple older remote branches and both release-candidate tags. Removing only
the current files or publishing only the stable branch does not remove its ancestry.

Before publication, the credential owners must establish whether these were ever valid and
revoke/rotate them if necessary. A separately reviewed cleanup of affected history and hosting
surfaces is needed if the credential values must be removed. No rotation, rewrite of the shared
repository, force-push, or visibility change was performed. An isolated history scrub was
subsequently prepared and verified as described below.

## Reviewed examples and false positives

- Three Gitleaks matches in historical `README.md` curl examples are placeholders, not literal
  bearer tokens: commit `8bb901131a9da684663a0643026922fe5d3b4fa8`, line 286, and commit
  `61d1cf74da2c37d595e6ab58005fd8958a1299d8`, lines 116 and 125.
- AWS access-key pattern matches contain the documented `EXAMPLE` suffix. No non-example AWS
  access-key IDs were identified by the supplementary pattern scan.
- The historical profile-credit-sync password previously flagged for investigation is a
  literal template label ending in `password`, rather than a demonstrated live credential.
- Credential-bearing database URLs in `docs/config/` are labeled illustrative examples. The
  URL in `cmd/api/runtime_assets_test.go` is a test fixture for rejecting URL credentials.
- Current Secret templates use explicit placeholders. The current Keycloak client import uses
  `CHANGE_ME_AFTER_IMPORT`; operators must generate a fresh secret after import.
- Current committed Postman example environments contain empty or placeholder credential
  fields. Authentication endpoints now use example domains.

## Cleanup performed outside docs/

- Rewrote the root README for the stable components, operating modes, local setup, deployment,
  documentation, and credential handling.
- Removed the compiled `slot-lifecycle` executable and added ignore rules for component
  binaries, local dotenv variants, credential files, private overlays, and nested local Postman
  environments. Documented example dotenv files remain tracked.
- Removed obsolete `cmd/api/README.md`, `infra/KUBEFLOW_UNINSTALL.md`, and
  `infra/keycloak-dex-integration.md`. The latter embedded client secrets in generated ConfigMap
  content. Maintained references and preserved migration material are linked from `infra/README.md`.
- Replaced environment-specific hosts, account IDs, realm keys, private image locations, and
  NFS inventory details in deployment examples. Worker volume names and mounts were updated
  together, retaining the template structure. Aligned the example API registry Secret name
  with both Notebook templates.
- Updated Postman example origins/authentication settings, profile-credit-sync scheduling
  instructions, and user-docs build instructions. Replaced the docs site's deployment-specific
  production origin and removed its reference to a nonexistent favicon.
- Removed the fixed local PostgreSQL password. Compose now requires `POSTGRES_PASSWORD`, binds
  only `127.0.0.1`, and uses a health check for the documented startup sequence. Existing database
  volumes retain their original credentials; changing the environment does not rotate them.

## Preserved documentation and remaining exposure

All files under `docs/` were preserved as requested, including generated OpenAPI, overlapping
design drafts, and the dated Kubeflow dossier. Hash comparison verifies preservation.

That directory still contains deployment-specific operational metadata such as cloud account
and cluster identifiers, internal endpoints, dated deployment observations, and inventory
details. These are not authentication secrets, but require organizational review before public
release. This phase does not sanitize or remove that material.

## Limits

Secret scans detect known patterns and cannot prove absence of every possible credential.
Historical credential validity, rotation, cluster configuration, and external secret stores
were not inspected. Unfetched pull-request refs, unreachable Git objects, forks, other clones,
GitHub Actions logs/artifacts, releases, issue/PR content, and other hosting surfaces were not
audited. Those require a separate hosting review before publication.

This phase adds no CI security workflow, policy files, dependency upgrades, or license, in
accordance with the requested scope.

## Validation results

- Final current-directory Gitleaks scan: no findings. Historical findings remain unresolved.
- All 37 files under `docs/` match the SHA-256 hashes captured before edits, byte for byte.
- Infrastructure YAML, Compose YAML, both embedded Notebook/PVC bundles, and all Postman JSON
  parse successfully. Notebook mount names reference existing volumes.
- Credential/binary ignore rules were checked with `git check-ignore`, including local Postman
  files in nested folders. `.env.all.example` remains available for tracking.
- Edited README links resolve, no Markdown links point to the removed guides, shell examples
  pass `bash -n`, and `git diff --check` passes.
- The Docusaurus production build succeeds using Node.js 22.23.2 and dependencies installed
  from the existing lockfile in a temporary copy. OpenAPI source files were not regenerated.
  It emits an existing warning about deprecated `onBrokenMarkdownLinks` configuration.
- `go test ./...` with Go 1.24.2 passes the worker, platform-token sidecar, GPU configuration,
  Kubernetes, and utility packages. The full suite fails these existing subtests:
  - `cmd/api`: `TestDetermineNotebookState/nil_k8sSpec,_event_empty` (expected `running`, got `opening`).
  - `cmd/cron/profile-credit-sync`: `TestNormalizeValue/value_smaller_than_epsilon_should_be_zero`
    and `TestNormalizeValue/value_equal_to_epsilon_should_be_zero`.
  The same failures were reproduced from an untouched archive of the stable commit in `/tmp`.
  Application source, test files, and dependency manifests were not changed by this cleanup.
- No database, cluster, or ingress rollout was performed. Compose was checked statically;
  Docker is not installed in the audit environment, so its startup was not exercised.

## Reproduce the scans

Using the verified Gitleaks version, write redacted reports outside the repository:

```bash
gitleaks dir --redact --ignore-gitleaks-allow \
  --report-format=json --report-path=/tmp/sandbox-current-secrets.json .
gitleaks git --log-opts=--all --redact --ignore-gitleaks-allow \
  --report-format=json --report-path=/tmp/sandbox-history-secrets.json .
```

An exit code of 1 denotes findings. Do not suppress historical findings with broad allowlists
or a baseline in place of investigating the credentials.

## Prepared isolated history cleanup

On 2026-09-17, a fresh remote mirror was fetched into
`/tmp/sandbox-public-release-clean/repository.git`, including all 39 advertised refs and 23
pull-request refs. This expanded verification to 215 reachable remote commits. Its remote was
removed to prevent accidental pushes.

Using the official pinned git-filter-repo v2.47.0, both original credential values were replaced
with `REMOVED_EXPOSED_CREDENTIAL` across text blobs and commit/tag messages. The rewrite changed
125 existing commits while preserving the branch/tag/ref names. All 2,374 objects remaining
after pruning, including unreachable objects, were checked for the two original byte strings;
neither remained. The private temporary replacement file was deleted.

Gitleaks on the cleaned mirror reports only the three historical README placeholders reviewed
above. It reports neither literal credential finding. There are 13 changed branch tips, two
changed tag tips, and 22 changed pull-request refs. GitHub pull-request refs cannot be updated
through ordinary pushes; an owner must review affected pull requests and contact GitHub Support
for retained references/caches as necessary.

The prepared stable branch also includes the current README and cleanup changes. Its `docs/`
tree remains byte-for-byte identical to this workspace. Review artifacts are kept outside the
repository:

- Clean checkout: `/tmp/sandbox-public-release-clean/review/`.
- Portable bundle: `/tmp/sandbox-public-release-clean/sandbox-connect-clean.bundle`.
- Branch/tag update manifest with original and replacement tip IDs:
  `/tmp/sandbox-public-release-clean/push-plan.json`.
- Commit/ref maps and first-changed-commit information:
  `/tmp/sandbox-public-release-clean/repository.git/filter-repo/`.
- Redacted before/after scan reports and exact-value verification:
  `/tmp/sandbox-public-release-clean/`.

Rotation/revocation status is **unknown**, as reported by the repository owner. This local
artifact does not resolve the exposure in the original workspace, shared GitHub repository,
existing clones, or hosting caches. Do not treat it as publication clearance.

To finish:

1. Have the Keycloak and registry owners confirm that both old values were never valid or have
   been revoked/rotated. Check consumers use the replacement credentials.
2. Freeze pushes and review the branch/tag manifest. The owner approved replacing the 13 branch
   tips and two tag tips on 2026-09-17; that approval remains in effect. Branch protection may
   need an owner-managed temporary exception.
3. Apply only the listed branch/tag updates using an atomic push with explicit
   `--force-with-lease=<ref>:<original-tip>` guards. Do not use a blanket mirror force-push.
   Abort if the remote has advanced; refresh and prepare a new scrub instead.
4. Review affected pull-request refs, forks, retained objects, logs, and release artifacts with
   GitHub Support as needed. Re-clone collaborators' checkouts so old history is not merged back.
5. Freshly clone the remote, repeat the scans, and finish the organizational review of preserved
   `docs/` metadata before changing visibility.

See [GitHub's sensitive-data removal procedure](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
for the rotation and hosting-cleanup requirements. No shared refs have been changed by this preparation.

## Approved push attempt and authentication blocker

After owner approval on 2026-09-17, all 15 original remote tips were rechecked and matched the
prepared lease guards. The atomic push was attempted twice, including a retry using any
repository-scoped authentication settings in memory. Both attempts stopped before updating any
refs because Git could not obtain a GitHub HTTPS username/credential in this noninteractive
session. No credential helper or HTTP authentication settings are configured locally or globally.

The existing SSH setup was also checked: the agent has no identities and standard private-key
files are absent. The SSH connection stopped at host-key verification; no credentials or new
host trust were created.

The approved rewrite is still prepared in the isolated copy and portable bundle. Authenticate
Git with repository write access using your normal credential manager or SSH setup, then retry
after rechecking the remote lease guards. No additional owner approval is needed for the
already-reviewed branch/tag update plan. GitHub pull-request references/caches and credential
rotation remain separate, unresolved follow-up work.

Repository visibility evidence:
[GitHub repository metadata](https://api.github.com/repos/datakaveri/sandbox-connect-api).
