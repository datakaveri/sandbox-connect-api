package evaluation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type rowScanner interface{ Scan(dest ...any) error }

const evaluationColumns = `
	evaluation_id, notebook_id, user_id, notebook_name, namespace, source_pvc,
	source_notebook_path, status, workflow_name, workflow_uid, output_prefix,
	manifest_key, output_manifest, hold_active, safe_error_code, safe_error_message,
	created_at, updated_at`

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	err := row.Scan(&record.ID, &record.NotebookID, &record.UserID, &record.NotebookName,
		&record.Namespace, &record.SourcePVC, &record.SourceNotebookPath, &record.Status,
		&record.WorkflowName, &record.WorkflowUID, &record.OutputPrefix, &record.ManifestKey,
		&record.OutputManifest, &record.HoldActive, &record.SafeErrorCode,
		&record.SafeErrorMessage, &record.CreatedAt, &record.UpdatedAt)
	return record, err
}

func (s *Store) Create(ctx context.Context, params CreateParams) (Record, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanRecord(tx.QueryRow(ctx,
		`SELECT `+evaluationColumns+` FROM evaluations WHERE user_id=$1 AND idempotency_key=$2`,
		params.UserID, params.IdempotencyKey))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, err
	}

	// Booking lifecycle handlers lock bookings before notebooks. Use the same
	// ordering here to prevent deadlocks with concurrent Reset or Terminate.
	// Re-read the notebook under lock below so this initial lookup is never
	// trusted as the final ownership, lifecycle, or booking check.
	var expectedBookingID *int64
	if params.RequireActiveBooking {
		var notebookID int64
		err = tx.QueryRow(ctx, `SELECT id, booking_id FROM notebooks
			WHERE user_id=$1 AND name=$2 AND events[array_upper(events, 1)] <> 'deleted'`,
			params.UserID, params.NotebookName).Scan(&notebookID, &expectedBookingID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, false, ErrNotebookNotFound
		}
		if err != nil {
			return Record{}, false, err
		}
		if expectedBookingID == nil {
			return Record{}, false, ErrNotebookNotReady
		}

		var bookingStatus string
		err = tx.QueryRow(ctx, `SELECT status FROM bookings
			WHERE id=$1 AND user_id=$2 AND notebook_id=$3 FOR UPDATE`,
			*expectedBookingID, params.UserID, notebookID).Scan(&bookingStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, false, ErrNotebookNotReady
		}
		if err != nil {
			return Record{}, false, err
		}
		if bookingStatus != "active" {
			return Record{}, false, ErrNotebookNotReady
		}
	}

	var notebookID int64
	var bookingID *int64
	var notebookName, namespace, sourcePVC, latestEvent string
	err = tx.QueryRow(ctx, `SELECT id, name, namespace, pvc_name,
		events[array_upper(events, 1)], booking_id FROM notebooks
		WHERE user_id=$1 AND name=$2 AND events[array_upper(events, 1)] <> 'deleted'
		FOR UPDATE`, params.UserID, params.NotebookName).
		Scan(&notebookID, &notebookName, &namespace, &sourcePVC, &latestEvent, &bookingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, ErrNotebookNotFound
	}
	if err != nil {
		return Record{}, false, err
	}
	if latestEvent != "notebook-applied" {
		return Record{}, false, ErrNotebookNotReady
	}
	if params.RequireActiveBooking &&
		(bookingID == nil || expectedBookingID == nil || *bookingID != *expectedBookingID) {
		return Record{}, false, ErrNotebookNotReady
	}

	evaluationID := uuid.NewString()
	outputPrefix := fmt.Sprintf("/user/%s/evaluations/%s/", notebookName, evaluationID)
	record, err := scanRecord(tx.QueryRow(ctx, `INSERT INTO evaluations (
		evaluation_id, notebook_id, user_id, notebook_name, namespace, source_pvc,
		source_notebook_path, status, output_prefix, manifest_key, idempotency_key, hold_active
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true)
	ON CONFLICT DO NOTHING RETURNING `+evaluationColumns,
		evaluationID, notebookID, params.UserID, notebookName, namespace, sourcePVC,
		params.SourceNotebookPath, StatusStopping, outputPrefix, outputPrefix+"manifest.json",
		params.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingErr := scanRecord(tx.QueryRow(ctx,
			`SELECT `+evaluationColumns+` FROM evaluations WHERE user_id=$1 AND idempotency_key=$2`,
			params.UserID, params.IdempotencyKey))
		if existingErr == nil {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Record{}, false, commitErr
			}
			return existing, false, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return Record{}, false, existingErr
		}
		return Record{}, false, ErrActiveEvaluation
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *Store) MarkNotebookStopped(ctx context.Context, evaluationID string) error {
	result, err := s.pool.Exec(ctx, `UPDATE evaluations SET status=$2
		WHERE evaluation_id=$1 AND status=$3 AND hold_active=true`,
		evaluationID, StatusWaitingForPVC, StatusStopping)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (s *Store) MarkStopFailed(ctx context.Context, evaluationID, code, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE evaluations SET status=$2, hold_active=false,
		safe_error_code=$3, safe_error_message=$4, completed_at=NOW()
		WHERE evaluation_id=$1 AND status=$5`, evaluationID, StatusFailed, code, message, StatusStopping)
	return err
}

func (s *Store) GetOwned(ctx context.Context, evaluationID, userID string) (Record, error) {
	record, err := scanRecord(s.pool.QueryRow(ctx,
		`SELECT `+evaluationColumns+` FROM evaluations WHERE evaluation_id=$1 AND user_id=$2`,
		evaluationID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrEvaluationNotFound
	}
	return record, err
}

func (s *Store) HasActiveHold(ctx context.Context, notebookID int64) (bool, error) {
	var held bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM evaluations WHERE notebook_id=$1 AND hold_active=true)`,
		notebookID).Scan(&held)
	return held, err
}

func (s *Store) RequestApproval(ctx context.Context, params ApprovalParams) (Approval, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Approval{}, false, err
	}
	defer tx.Rollback(ctx)
	var status Status
	err = tx.QueryRow(ctx, `SELECT status FROM evaluations
		WHERE evaluation_id=$1 AND user_id=$2 FOR UPDATE`, params.EvaluationID, params.RequestedBy).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, ErrEvaluationNotFound
	}
	if err != nil {
		return Approval{}, false, err
	}
	if status != StatusSucceeded {
		return Approval{}, false, ErrEvaluationNotReady
	}

	existing, err := scanApproval(tx.QueryRow(ctx, `SELECT evaluation_id, requested_by,
		status, copy_job_name, destination, attempt_count, safe_error_code, safe_error_message,
		created_at, updated_at FROM evaluation_approvals WHERE evaluation_id=$1`, params.EvaluationID))
	if err == nil {
		if existing.Status != ApprovalFailed {
			return existing, false, nil
		}
		retried, retryErr := scanApproval(tx.QueryRow(ctx, `UPDATE evaluation_approvals
			SET status=$2, idempotency_key=$3, safe_error_code=NULL, safe_error_message=NULL,
			    claimed_by=NULL, claim_expires_at=NULL, copy_job_name=NULL,
			    attempt_count=attempt_count+1, completed_at=NULL
			WHERE evaluation_id=$1
			RETURNING evaluation_id, requested_by, status, copy_job_name, destination, attempt_count,
			          safe_error_code, safe_error_message, created_at, updated_at`,
			params.EvaluationID, ApprovalRequested, params.IdempotencyKey))
		if retryErr != nil {
			return Approval{}, false, retryErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Approval{}, false, commitErr
		}
		return retried, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, err
	}

	approval, err := scanApproval(tx.QueryRow(ctx, `INSERT INTO evaluation_approvals
		(evaluation_id, requested_by, status, destination, idempotency_key)
		VALUES ($1,$2,$3,$4,$5) RETURNING evaluation_id, requested_by, status,
		copy_job_name, destination, attempt_count, safe_error_code, safe_error_message, created_at, updated_at`,
		params.EvaluationID, params.RequestedBy, ApprovalRequested, params.Destination, params.IdempotencyKey))
	if err != nil {
		return Approval{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Approval{}, false, err
	}
	return approval, true, nil
}

func scanApproval(row rowScanner) (Approval, error) {
	var approval Approval
	err := row.Scan(&approval.EvaluationID, &approval.RequestedBy, &approval.Status,
		&approval.CopyJobName, &approval.Destination, &approval.AttemptCount, &approval.SafeErrorCode,
		&approval.SafeErrorMessage, &approval.CreatedAt, &approval.UpdatedAt)
	return approval, err
}
