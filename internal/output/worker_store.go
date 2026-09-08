package output

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ApprovalWork struct {
	Approval Approval
	Output   Record
}

func (s *Store) ClaimNextOutput(ctx context.Context, workerID string, lease time.Duration) (Record, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback(ctx)
	record, err := scanRecord(tx.QueryRow(ctx, `SELECT `+outputColumns+`
		FROM output_jobs
		WHERE hold_active=true
		  AND status NOT IN ('stopping','pending_approval','failed')
		  AND (claim_expires_at IS NULL OR claim_expires_at < NOW() OR claimed_by=$1)
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE output_jobs SET claimed_by=$2,
		claim_expires_at=NOW()+($3 * INTERVAL '1 second') WHERE output_id=$1`,
		record.ID, workerID, lease.Seconds())
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *Store) SetWorkflow(ctx context.Context, outputID, workerID, name, uid string) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_jobs
		SET workflow_name=$3, workflow_uid=$4, status=$5, started_at=COALESCE(started_at,NOW())
		WHERE output_id=$1 AND claimed_by=$2 AND hold_active=true`,
		outputID, workerID, name, uid, StatusPreparing)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) SetPhase(ctx context.Context, outputID, workerID string, status Status, lease time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_jobs SET status=$3,
		claim_expires_at=NOW()+($4 * INTERVAL '1 second')
		WHERE output_id=$1 AND claimed_by=$2 AND hold_active=true`,
		outputID, workerID, status, lease.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) CompleteOutput(ctx context.Context, outputID, workerID string, manifest json.RawMessage) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_jobs
		SET status=$3, output_manifest=$4, hold_active=false, completed_at=NOW(),
		    claimed_by=NULL, claim_expires_at=NULL
		WHERE output_id=$1 AND claimed_by=$2 AND hold_active=true`,
		outputID, workerID, StatusPendingApproval, manifest)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) FailOutput(ctx context.Context, outputID, workerID, code, message string) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_jobs
		SET status=$3, hold_active=false, safe_error_code=$4, safe_error_message=$5,
		    completed_at=NOW(), claimed_by=NULL, claim_expires_at=NULL
		WHERE output_id=$1 AND claimed_by=$2 AND hold_active=true`,
		outputID, workerID, StatusFailed, code, message)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) ClaimNextApproval(ctx context.Context, workerID string, lease time.Duration) (ApprovalWork, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ApprovalWork{}, false, err
	}
	defer tx.Rollback(ctx)
	approval, err := scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+`
		FROM output_approvals
		WHERE status IN ('requested','publishing')
		  AND (claim_expires_at IS NULL OR claim_expires_at < NOW() OR claimed_by=$1)
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalWork{}, false, nil
	}
	if err != nil {
		return ApprovalWork{}, false, err
	}
	record, err := scanRecord(tx.QueryRow(ctx, `SELECT `+outputColumns+`
		FROM output_jobs WHERE output_id=$1`, approval.OutputID))
	if err != nil {
		return ApprovalWork{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE output_approvals SET claimed_by=$2,
		claim_expires_at=NOW()+($3 * INTERVAL '1 second') WHERE output_id=$1`,
		approval.OutputID, workerID, lease.Seconds())
	if err != nil {
		return ApprovalWork{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ApprovalWork{}, false, err
	}
	return ApprovalWork{Approval: approval, Output: record}, true, nil
}

func (s *Store) SetPublishing(ctx context.Context, outputID, workerID string, lease time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_approvals
		SET status=$3, claim_expires_at=NOW()+($4 * INTERVAL '1 second')
		WHERE output_id=$1 AND claimed_by=$2 AND status IN ('requested','publishing')`,
		outputID, workerID, ApprovalPublishing, lease.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) CompleteApproval(ctx context.Context, outputID, workerID string, result PublicationResult) error {
	approvedFileIDs, err := json.Marshal(result.ApprovedFileIDs)
	if err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE output_approvals
		SET status=$3, workspace_prefix=$4, workspace_manifest_key=$5,
		    approved_file_ids=$6, completed_at=NOW(), claimed_by=NULL, claim_expires_at=NULL
		WHERE output_id=$1 AND claimed_by=$2 AND status=$7`,
		outputID, workerID, ApprovalApproved, result.WorkspacePrefix,
		result.WorkspaceManifestKey, approvedFileIDs, ApprovalPublishing)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) FailApproval(ctx context.Context, outputID, workerID, code, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE output_approvals
		SET status=$3, safe_error_code=$4, safe_error_message=$5,
		    claimed_by=NULL, claim_expires_at=NULL
		WHERE output_id=$1 AND claimed_by=$2`,
		outputID, workerID, ApprovalFailed, code, message)
	return err
}
