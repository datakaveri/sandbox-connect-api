# Output submission and approval flow

Import both files into Postman:

- `sandbox-connect-api.output-approval-flow.postman_collection.json`
- `sandbox-connect-api.output-approval-flow.example.postman_environment.json`

Select **Sandbox Connect API - Output Approval Flow - Example** as the active environment.

## Required variables

- `notebook_name`: an owned, running notebook containing `nha_ps4_output_template.ipynb` at its workspace root.
- `owner_id`: the notebook owner's Keycloak subject ID. It normally matches the user namespace.
- `owner_token`: a fresh token for the notebook owner.
- `admin_token`: a fresh token containing the `cos_admin` realm role.
- `other_user_token`: a fresh token for a different user; required only for the explicit cross-user tests.
- `uploader_token`: the output-scoped File Connect uploader credential; required only for its direct permission-boundary test.

Tokens are intentionally blank and marked secret in the example environment. Never save exported environments containing real credentials. The deployed access tokens may expire after five minutes, so refresh them between folders when necessary.

## Run order

1. Run **01 - Owner Submission** in order. Submission intentionally stops the notebook. Poll the final request until `status` becomes `pending_approval`.
2. Inspect the CSV in **02 - COS Admin Review and Approval**, then run the approval request. Approval intentionally publishes the files. Retry **Verify Output Leaves Pending Queue** until it passes.
3. Refresh `owner_token`, then run **03 - Owner Workspace After Approval**. The requests capture the workspace file ID and presigned download URL automatically.
4. Populate the additional credentials and run **04 - Access-Control Checks**.

The collection saves these response values automatically:

- `output_id`
- `review_file_id`
- `expected_sha256`
- `workspace_file_id`
- `download_url`

`submit_idempotency_key` and `approval_idempotency_key` are generated only when blank. Clear both IDs and all captured values before beginning a genuinely new notebook-output run.

## Current API limitations

- There is no output rejection endpoint.
- Admins can preview pending CSV files but do not have an admin download endpoint.
- Output generation status and approval status are separate fields. After approval, use `approvalStatus: approved`; the generation `status` may remain `pending_approval`.
