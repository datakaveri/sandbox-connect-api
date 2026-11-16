# Files Connect output contract

This is the proposed contract implemented by the Sandbox Connect client and uploader.
The Files Connect server still needs to implement and validate these endpoints before rollout.
`FILE_SERVICE_BASE_URL` and `FILES_CONNECT_BASE_URL` include `/v1`.

All three endpoints authenticate a bearer service token, authorize the output ID against
trusted job ownership, and reject mismatched prefixes or inventories. The uploader credential
must authorize uploads and completion only for its output. Publication uses the controller's
separate identity. Client-supplied object keys and prefixes are assertions to verify, never
storage authorization. Files Connect derives databanks, ownership, and permitted keys itself.

## Request uploads

`POST /v1/outputs/{output_id}/uploads`

```json
{
  "reviewPrefix": "nha-review/users/user/sandboxes/notebook/outputs/run/",
  "files": [{
    "fileId": "<lowercase sha256 of the UTF-8 object key>",
    "path": "result.csv",
    "objectKey": "nha-review/users/user/sandboxes/notebook/outputs/run/output/result.csv",
    "size": 15,
    "mediaType": "text/csv",
    "sha256": "<lowercase sha256 of the file bytes>"
  }]
}
```

Successful response:

```json
{
  "success": true,
  "data": {
    "uploads": [{
      "fileId": "<same fileId>",
      "url": "https://<storage-host>/<signed-object-path>",
      "headers": {"Content-Type": "text/csv"}
    }]
  }
}
```

Return exactly one target for each requested file. Targets are HTTPS PUT URLs; redirects
are rejected. The uploader accepts only `Content-Type`, `Content-MD5`, and `x-amz-*`
signing headers and never sends its Files Connect bearer token or cookies to storage.
Files Connect should sign size, content type, and checksum constraints where storage supports
them. Single PUT is the initial transport, bounded by the configured per-file size limit.

Authorization should be idempotent for the same output/inventory and reject a different
inventory once completion succeeds. Expired URLs may be renewed for an unfinished output.

## Complete output

`POST /v1/outputs/{output_id}/complete`

The request has the same `reviewPrefix` and `files` fields as upload authorization.
Files Connect must independently verify that every object exists with the expected size,
CSV media type, and SHA-256 checksum. An ETag is not a substitute for a SHA-256 checksum.
It writes the review manifest last, only after all files have been verified.

```json
{
  "success": true,
  "data": {
    "reviewManifestKey": "nha-review/users/user/sandboxes/notebook/outputs/run/manifest.json",
    "manifest": {"files": [{
      "fileId": "<lowercase sha256 of the UTF-8 object key>",
      "path": "result.csv",
      "objectKey": "nha-review/users/user/sandboxes/notebook/outputs/run/output/result.csv",
      "size": 15,
      "mediaType": "text/csv",
      "sha256": "<lowercase sha256 of the file bytes>"
    }]}
  }
}
```

The actual `manifest.files` entries use exactly the object schema shown in the upload
request. The uploader accepts reordering, but rejects any changed ID, key, path, size,
media type, checksum, or file count. It writes `/workspace/manifest.json` only after this
verification. Argo returns that JSON to the controller, which validates it again before
marking the output `pending_approval`. Completion must be idempotent for the same inventory.
Partial uploads must never be visible as completed review outputs.

## Publish approved output

`POST /v1/outputs/{output_id}/publish`

```json
{
  "outputId": "run",
  "ownerId": "user",
  "reviewDatabankId": "<configured review databank>",
  "workspaceDatabankId": "<configured workspace databank>",
  "reviewPrefix": "nha-review/users/user/sandboxes/notebook/outputs/run/",
  "reviewManifestKey": "nha-review/users/user/sandboxes/notebook/outputs/run/manifest.json",
  "workspacePrefix": "user-workspaces/users/user/outputs/run/",
  "files": [{
      "fileId": "<lowercase sha256 of the UTF-8 object key>",
      "path": "result.csv",
      "objectKey": "nha-review/users/user/sandboxes/notebook/outputs/run/output/result.csv",
      "size": 15,
      "mediaType": "text/csv",
      "sha256": "<lowercase sha256 of the file bytes>"
    }]
}
```

Only the publication identity may invoke this endpoint. Files Connect verifies the completed
review manifest, copies the CSV objects server-side, and writes the workspace manifest last.
Repeated publication for the same output/inventory must return the same result; conflicting
requests must fail rather than overwrite a published inventory.

```json
{
  "success": true,
  "data": {
    "workspacePrefix": "user-workspaces/users/user/outputs/run/",
    "workspaceManifestKey": "user-workspaces/users/user/outputs/run/manifest.json",
    "approvedFileIds": ["<review fileId>"]
  }
}
```

The controller validates both destination paths and the exact approved ID set.
Workspace list/preview/download use opaque IDs derived from workspace object keys, so those
IDs differ from review IDs and remain stable within their respective contexts.

## Limits and errors

Configure the same manifest byte, file count, per-file byte, and total byte limits in the API,
controller, and Files Connect. The controller passes its limits explicitly to the uploader
and uses them in publication validation. Defaults are 262144 manifest bytes, 1000 files,
256 MiB per file, and 1 GiB total. A lower file-count limit may be needed to fit long paths
within the manifest byte limit. Every deliverable must be a nonempty, parseable UTF-8 CSV
with consistent column counts. Initial delivery is flat: directories and all symlinks,
hard links, binary content, and non-CSV files are rejected.

Errors use a non-2xx HTTP status and an optional
`{"error":{"code":"stable_code","message":"safe explanation"}}` body. Never return
credentials or presigned URLs in error messages. Failed uploads/completion must not produce
a success manifest. Files Connect owns cleanup of abandoned partial objects and must exclude
them from review and published workspace listings.

The current runner keeps bounded logs and `execution-summary.json` on scratch storage.
They are internal diagnostics, not CSV deliverables, and this initial upload contract does
not transfer them. Persisting those diagnostics requires a separate trusted metadata contract;
participant-writable scratch files must not be treated as authenticated platform metadata.

## Verification

`go test ./internal/outputupload` exercises real HTTP client requests against a mocked
Files Connect server and HTTPS storage, including upload, completion, publication, failed
PUTs, redirects, changed files, invalid completion manifests, and credential-header rejection.
This verifies the proposed client contract; it is not a test against a deployed Files Connect
implementation or Kubernetes cluster.
