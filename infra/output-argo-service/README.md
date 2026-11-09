# output-argo-service deployment

These manifests are development artifacts with placeholders. Do not apply them
as-is. The controller has no user-facing Service or ingress.

## Storage model

Output files are uploaded to the private NHA review area under
nha-review/users/<user>/sandboxes/<sandbox>/outputs/<output-id>/. Successful
workflow completion changes the database state to pending_approval.
An NHA admin reviews CSV files through Sandbox Connect, then approval causes the
controller to call the proposed idempotent Files Connect
POST /v1/outputs/{output_id}/publish endpoint. Files Connect publishes the
approved files under
user-workspaces/users/<user>/outputs/<output-id>/.

No RWX filesystem is needed for this demo path. The workflow still needs a
normal namespace-local RWO scratch PVC and read-only access to the participant
notebook PVC.

## Safe rollout order

1. Confirm the target checkout and cluster explicitly. Do not copy or apply
   these manifests during feature development.
2. Implement and test the Files Connect output upload/complete/publish contract.
   Its existing list, CSV preview, and presigned-download endpoints are reused.
3. Build the runner, uploader, and controller images and replace image
   placeholders with immutable digest references.
4. Install and validate Argo Workflows in a non-production environment.
5. Reconcile the runner service account, replacement ConfigMap, and namespace
   Secrets for a test user. Review NetworkPolicy against actual service CIDRs.
6. Have the DBA back up a test database, review, and apply
   migrations/001_outputs.sql. Use a restricted controller database role.
7. Exercise direct-notebook and booking-enabled submission, PVC detach,
   workflow restart/failure, invalid/non-CSV output rejection, admin preview,
   idempotent publication, and user workspace isolation.
8. Enable API_OUTPUTS_ENABLED. In booking mode also enable
   SLOT_LIFECYCLE_OUTPUTS_ENABLED; direct mode does not use that lifecycle
   flag.

## Automatic namespace provisioning

Each participant namespace needs the `output-runner` ServiceAccount, its
executor-only token Secret, a RoleBinding to the shared
`output-runner-executor` ClusterRole, the registry (`nha-registry-cred`) and
upload (`output-file-service`) Secrets, the mTLS client Secret
(`output-data-access-mtls`), and create-only `output-replacements-v1` /
`output-production-env` defaults. The controller reconciles these itself,
replacing the manual `prepare-output-namespace.py` procedure:

- before creating each output Workflow (just in time); permanent problems fail
  the output with `namespace_provisioning_failed`, transient API errors retry;
- every `OUTPUT_PROVISION_INTERVAL_SECONDS` for every namespace labelled
  `OUTPUT_PROVISION_PROFILE_LABEL` plus `OUTPUT_PROVISION_ALLOWED_NAMESPACES`.
  This pass also publishes renewed mTLS client certificates, so new namespaces
  no longer need `output-mtls-sync` CronJob targets.

Registry and upload credentials come from `sandbox` Secrets mounted read-only
into the controller; the mTLS leaf is read from `omop-auth` through a Role
limited to that one Secret. Existing replacement maps and production
environments are never modified. A legacy RoleBinding to the namespace Role
`output-runner` is accepted unchanged. Credential values are never logged.

`OUTPUT_PROVISION_MODE` is `off` by default. Roll out with `dry-run` (drift is
logged, nothing is written, workflows are never blocked), then switch to
`apply`. The controller can create Secrets cluster-wide (create cannot be
name-scoped); add a ValidatingAdmissionPolicy restricting it to profile
namespaces and the provisioned object names before enabling `apply` in
production.

## Required decisions and external work

- final notebook path, replacement map, and production environment values;
- immutable runner/uploader/controller images;
- Files Connect review/workspace databank IDs and service authentication;
- implementation of the proposed Files Connect output upload/complete/publish
  endpoints (the supplied OpenAPI is the current baseline and lacks publish);
- exact Keycloak admin roles configured in API_OUTPUT_ADMIN_ROLES;
- Argo installation and network policy for the target non-production cluster.

The controller ClusterRole deliberately has no Secret read permission. Workflow
pods receive only namespace-local Secret references. Sandbox API and controller
service credentials must be injected from Secrets, never ConfigMaps.

## Runner and uploader implementation

The workflow binaries now live in `cmd/output-runner` and `cmd/output-uploader`.
Build from the repository root:

```bash
docker build -f infra/output-runner/Dockerfile -t output-runner:dev .
docker build -f infra/output-uploader/Dockerfile -t output-uploader:dev .
docker build -f infra/output-argo-service/Dockerfile -t output-argo-service:dev .
```

Use the resulting registry digests in workflow/controller configuration after scanning and
pushing the images. For now the runner copies the reference project's notebook base and
APT, conda, and pip dependency layers, including its default `PS_TAG=nha-ps1-v3`.
This is a temporary baseline, not verified PS4 compatibility. Select another available notebook
tag with `--build-arg PS_TAG=<notebook-tag>` when its requirements are confirmed.
The copied dependency constraints include unpinned packages, as in the reference; the final
built runner must still be deployed by digest. The runner uses the conda interpreter/tool path
and retains our non-root user and Go entrypoint. The included agent/reporting libraries do not
enable repair or scoring in our pipeline. No packages are installed from participant input
at runtime.

Preparation copies only the configured v4 Python notebook, with `language_info.name=python`.
Support files are not copied in this initial implementation. Conversion must produce
`notebook.py`; notebooks that require interactive IPython features may fail direct Python
execution. Replacements are exact, simultaneous, nonoverlapping strings in the versioned
`required`/`optional` maps. Required keys must exist and must not remain after replacement.
Keep credentials in the execution Secret, not replacement values.

Execution runs from `/workspace/output` and sets `OUTPUT_DIR` to that directory. The notebook
must write its CSV deliverables there. Stage timeouts default to 20 minutes and remain bounded
by the overall workflow deadline. Notebook/script size is capped at 32 MiB and each stage log
at 1 MiB. The uploader rejects directories, symlinks, hard links, empty files, malformed CSV,
non-text data and non-CSV files, checks size/checksum again before PUT, and verifies completion
before exposing the manifest to Argo.

The workflow passes `--stream-logs` to the convert and execute stages. Their bounded stdout and
stderr are retained in scratch and copied to the container log for live Kubernetes/Argo streaming.
Execution uses unbuffered Python output. This stream is raw participant-program output and can
contain values printed from the production environment; remove the flag from the workflow templates
to disable it.

`FILE_SERVICE_BASE_URL` includes `/v1`; `FILE_SERVICE_TOKEN` must be output-scoped.
The exact proposed request/response contract is in
[output-files-connect-contract.md](../../docs/output-files-connect-contract.md).
The server must still implement it. The API/controller/uploader share configured validation
limits. Set equivalent `API_OUTPUT_*` and `OUTPUT_*` limits on the two services.

Workflow stage containers have `automountServiceAccountToken: false`; only Argo's executor
receives its service-account token through `executor.serviceAccountName`. Reconcile the token
Secret in `runner-rbac.example.yaml` using Argo's
[service-account token discovery](https://argo-workflows.readthedocs.io/en/latest/service-account-secrets/).
See the [Argo field reference](https://argoproj.github.io/argo-workflows/fields/) for these fields.
Validate the generated pods against the installed Argo version before rollout, including that
participant code cannot read executor credentials. Do not enable shared process namespaces.

Run the runtime and contract tests with:

```bash
go test ./internal/output ./internal/outputruntime ./internal/outputupload ./internal/filesconnect ./internal/outputargo
python3 -m venv /tmp/output-runner-venv
/tmp/output-runner-venv/bin/pip install -r infra/output-runner/smoke-requirements.txt
OUTPUT_NBCONVERT_TEST=1 PATH="/tmp/output-runner-venv/bin:$PATH" go test ./internal/outputruntime -run TestRealNotebookConversion -count=1
```

The smoke requirements file is only for the local conversion test; the Docker image uses
the copied reference dependency layers. The real conversion test uses a generated minimal notebook; running the actual production
notebook remains a deployment acceptance test. Neither these tests nor image builds apply a
migration, modify a cluster, or publish user outputs.
