BEGIN;

CREATE TABLE IF NOT EXISTS evaluations (
    evaluation_id UUID PRIMARY KEY,
    notebook_id BIGINT NOT NULL REFERENCES notebooks(id) ON DELETE RESTRICT,
    user_id UUID NOT NULL,
    notebook_name VARCHAR(255) NOT NULL,
    namespace VARCHAR(255) NOT NULL,
    source_pvc VARCHAR(255) NOT NULL,
    source_notebook_path TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    workflow_name VARCHAR(253), workflow_uid VARCHAR(255),
    output_prefix TEXT NOT NULL, manifest_key TEXT NOT NULL, output_manifest JSONB,
    idempotency_key VARCHAR(200) NOT NULL,
    hold_active BOOLEAN NOT NULL DEFAULT true,
    safe_error_code VARCHAR(80), safe_error_message TEXT,
    claimed_by VARCHAR(255), claim_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ, completed_at TIMESTAMPTZ,
    CONSTRAINT evaluations_status_check CHECK (status IN (
        'stopping', 'waiting_for_pvc', 'preparing', 'converting', 'configuring',
        'executing', 'uploading', 'succeeded', 'failed'
    )),
    CONSTRAINT evaluations_user_idempotency_unique UNIQUE (user_id, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS evaluations_one_active_per_notebook
    ON evaluations(notebook_id) WHERE hold_active = true;
CREATE INDEX IF NOT EXISTS evaluations_worker_queue
    ON evaluations(status, claim_expires_at, created_at);
CREATE INDEX IF NOT EXISTS evaluations_owner
    ON evaluations(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS evaluation_approvals (
    evaluation_id UUID PRIMARY KEY REFERENCES evaluations(evaluation_id) ON DELETE RESTRICT,
    requested_by UUID NOT NULL,
    status VARCHAR(24) NOT NULL,
    copy_job_name VARCHAR(253), destination TEXT NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 1,
    idempotency_key VARCHAR(200) NOT NULL,
    safe_error_code VARCHAR(80), safe_error_message TEXT,
    claimed_by VARCHAR(255), claim_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), completed_at TIMESTAMPTZ,
    CONSTRAINT evaluation_approvals_status_check CHECK (status IN ('requested', 'copying', 'approved', 'failed')),
    CONSTRAINT evaluation_approvals_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT evaluation_approvals_user_idempotency_unique UNIQUE (requested_by, idempotency_key)
);

CREATE INDEX IF NOT EXISTS evaluation_approvals_worker_queue
    ON evaluation_approvals(status, claim_expires_at, created_at);

CREATE OR REPLACE FUNCTION touch_evaluation_updated_at()
RETURNS TRIGGER AS $$ BEGIN NEW.updated_at = NOW(); RETURN NEW; END; $$ LANGUAGE plpgsql;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'evaluations_touch_updated_at'
                   AND tgrelid = 'evaluations'::regclass) THEN
        CREATE TRIGGER evaluations_touch_updated_at BEFORE UPDATE ON evaluations
        FOR EACH ROW EXECUTE PROCEDURE touch_evaluation_updated_at();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'evaluation_approvals_touch_updated_at'
                   AND tgrelid = 'evaluation_approvals'::regclass) THEN
        CREATE TRIGGER evaluation_approvals_touch_updated_at BEFORE UPDATE ON evaluation_approvals
        FOR EACH ROW EXECUTE PROCEDURE touch_evaluation_updated_at();
    END IF;
END;
$$;

COMMIT;
