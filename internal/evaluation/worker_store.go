package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ApprovalWork struct {
	Approval   Approval
	Evaluation Record
}

func (s *Store) ClaimNextEvaluation(ctx context.Context, workerID string, lease time.Duration) (Record, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback(ctx)
	record, err := scanRecord(tx.QueryRow(ctx, `SELECT `+evaluationColumns+`
		FROM evaluations
		WHERE hold_active=true
		  AND status NOT IN ('stopping','succeeded','failed')
		  AND (claim_expires_at IS NULL OR claim_expires_at < NOW() OR claimed_by=$1)
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE evaluations SET claimed_by=$2,
		claim_expires_at=NOW()+($3 * INTERVAL '1 second') WHERE evaluation_id=$1`,
		record.ID, workerID, lease.Seconds())
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *Store) SetWorkflow(ctx context.Context, evaluationID, workerID, name, uid string) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluations
		SET workflow_name=$3, workflow_uid=$4, status=$5, started_at=COALESCE(started_at,NOW())
		WHERE evaluation_id=$1 AND claimed_by=$2 AND hold_active=true`,
		evaluationID, workerID, name, uid, StatusPreparing)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) SetPhase(ctx context.Context, evaluationID, workerID string, status Status, lease time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluations SET status=$3,
		claim_expires_at=NOW()+($4 * INTERVAL '1 second')
		WHERE evaluation_id=$1 AND claimed_by=$2 AND hold_active=true`,
		evaluationID, workerID, status, lease.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) CompleteEvaluation(ctx context.Context, evaluationID, workerID string, manifest json.RawMessage) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluations
		SET status=$3, output_manifest=$4, hold_active=false, completed_at=NOW(),
		    claimed_by=NULL, claim_expires_at=NULL
		WHERE evaluation_id=$1 AND claimed_by=$2 AND hold_active=true`,
		evaluationID, workerID, StatusSucceeded, manifest)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) FailEvaluation(ctx context.Context, evaluationID, workerID, code, message string) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluations
		SET status=$3, hold_active=false, safe_error_code=$4, safe_error_message=$5,
		    completed_at=NOW(), claimed_by=NULL, claim_expires_at=NULL
		WHERE evaluation_id=$1 AND claimed_by=$2 AND hold_active=true`,
		evaluationID, workerID, StatusFailed, code, message)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) ClaimNextApproval(ctx context.Context, workerID string, lease time.Duration) (ApprovalWork, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ApprovalWork{}, false, err
	}
	defer tx.Rollback(ctx)
	approval, err := scanApproval(tx.QueryRow(ctx, `SELECT evaluation_id, requested_by,
		status, copy_job_name, destination, attempt_count, safe_error_code, safe_error_message,
		created_at, updated_at FROM evaluation_approvals
		WHERE status IN ('requested','copying')
		  AND (claim_expires_at IS NULL OR claim_expires_at < NOW() OR claimed_by=$1)
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalWork{}, false, nil
	}
	if err != nil {
		return ApprovalWork{}, false, err
	}
	record, err := scanRecord(tx.QueryRow(ctx, `SELECT `+evaluationColumns+`
		FROM evaluations WHERE evaluation_id=$1`, approval.EvaluationID))
	if err != nil {
		return ApprovalWork{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE evaluation_approvals SET claimed_by=$2,
		claim_expires_at=NOW()+($3 * INTERVAL '1 second') WHERE evaluation_id=$1`,
		approval.EvaluationID, workerID, lease.Seconds())
	if err != nil {
		return ApprovalWork{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ApprovalWork{}, false, err
	}
	return ApprovalWork{Approval: approval, Evaluation: record}, true, nil
}

func (s *Store) SetCopyJob(ctx context.Context, evaluationID, workerID, jobName string) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluation_approvals
		SET status=$3, copy_job_name=$4
		WHERE evaluation_id=$1 AND claimed_by=$2 AND status IN ('requested','copying')`,
		evaluationID, workerID, ApprovalCopying, jobName)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) RenewApprovalClaim(ctx context.Context, evaluationID, workerID string, lease time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluation_approvals
		SET claim_expires_at=NOW()+($3 * INTERVAL '1 second')
		WHERE evaluation_id=$1 AND claimed_by=$2 AND status IN ('requested','copying')`,
		evaluationID, workerID, lease.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) CompleteApproval(ctx context.Context, evaluationID, workerID string) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluation_approvals
		SET status=$3, completed_at=NOW(), claimed_by=NULL, claim_expires_at=NULL
		WHERE evaluation_id=$1 AND claimed_by=$2 AND status=$4`,
		evaluationID, workerID, ApprovalApproved, ApprovalCopying)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) FailApproval(ctx context.Context, evaluationID, workerID, code, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE evaluation_approvals
		SET status=$3, safe_error_code=$4, safe_error_message=$5,
		    claimed_by=NULL, claim_expires_at=NULL
		WHERE evaluation_id=$1 AND claimed_by=$2`,
		evaluationID, workerID, ApprovalFailed, code, message)
	return err
}
