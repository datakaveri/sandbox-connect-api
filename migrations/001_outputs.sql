BEGIN;

CREATE TABLE IF NOT EXISTS output_jobs (
    output_id UUID PRIMARY KEY,
    notebook_id BIGINT NOT NULL REFERENCES notebooks(id) ON DELETE RESTRICT,
    user_id UUID NOT NULL,
    notebook_name VARCHAR(255) NOT NULL,
    namespace VARCHAR(255) NOT NULL,
    source_pvc VARCHAR(255) NOT NULL,
    source_notebook_path TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    workflow_name VARCHAR(253),
    workflow_uid VARCHAR(255),
    review_prefix TEXT NOT NULL,
    review_manifest_key TEXT NOT NULL,
    output_manifest JSONB,
    idempotency_key VARCHAR(200) NOT NULL,
    hold_active BOOLEAN NOT NULL DEFAULT true,
    safe_error_code VARCHAR(80),
    safe_error_message TEXT,
    claimed_by VARCHAR(255),
    claim_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CONSTRAINT output_jobs_status_check CHECK (status IN (
        'stopping', 'waiting_for_pvc', 'preparing', 'converting', 'configuring',
        'executing', 'uploading', 'pending_approval', 'failed'
    )),
    CONSTRAINT output_jobs_user_idempotency_unique UNIQUE (user_id, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS output_jobs_one_active_per_notebook
    ON output_jobs(notebook_id) WHERE hold_active = true;
CREATE INDEX IF NOT EXISTS output_jobs_worker_queue
    ON output_jobs(status, claim_expires_at, created_at);
CREATE INDEX IF NOT EXISTS output_jobs_owner
    ON output_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS output_jobs_admin_pending
    ON output_jobs(created_at) WHERE status = 'pending_approval';

CREATE TABLE IF NOT EXISTS output_approvals (
    output_id UUID PRIMARY KEY REFERENCES output_jobs(output_id) ON DELETE RESTRICT,
    requested_by_admin UUID NOT NULL,
    status VARCHAR(24) NOT NULL,
    workspace_prefix TEXT NOT NULL,
    workspace_manifest_key TEXT,
    approved_file_ids JSONB,
    attempt_count INTEGER NOT NULL DEFAULT 1,
    idempotency_key VARCHAR(200) NOT NULL,
    safe_error_code VARCHAR(80),
    safe_error_message TEXT,
    claimed_by VARCHAR(255),
    claim_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT output_approvals_status_check CHECK (status IN ('requested', 'publishing', 'approved', 'failed')),
    CONSTRAINT output_approvals_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT output_approvals_admin_idempotency_unique UNIQUE (requested_by_admin, idempotency_key)
);

CREATE INDEX IF NOT EXISTS output_approvals_worker_queue
    ON output_approvals(status, claim_expires_at, created_at);

CREATE OR REPLACE FUNCTION touch_output_updated_at()
RETURNS TRIGGER AS $$ BEGIN NEW.updated_at = NOW(); RETURN NEW; END; $$ LANGUAGE plpgsql;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'output_jobs_touch_updated_at'
                   AND tgrelid = 'output_jobs'::regclass) THEN
        CREATE TRIGGER output_jobs_touch_updated_at BEFORE UPDATE ON output_jobs
        FOR EACH ROW EXECUTE PROCEDURE touch_output_updated_at();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'output_approvals_touch_updated_at'
                   AND tgrelid = 'output_approvals'::regclass) THEN
        CREATE TRIGGER output_approvals_touch_updated_at BEFORE UPDATE ON output_approvals
        FOR EACH ROW EXECUTE PROCEDURE touch_output_updated_at();
    END IF;
END;
$$;

COMMIT;
