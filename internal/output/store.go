package output

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

const outputColumns = `
	output_id, notebook_id, user_id, notebook_name, namespace, source_pvc,
	source_notebook_path, status, workflow_name, workflow_uid, review_prefix,
	review_manifest_key, output_manifest, hold_active, safe_error_code, safe_error_message,
	created_at, updated_at`

const approvalColumns = `
	output_id, requested_by_admin, status, workspace_prefix, workspace_manifest_key,
	approved_file_ids, attempt_count, safe_error_code, safe_error_message, created_at, updated_at`

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	err := row.Scan(&record.ID, &record.NotebookID, &record.UserID, &record.NotebookName,
		&record.Namespace, &record.SourcePVC, &record.SourceNotebookPath, &record.Status,
		&record.WorkflowName, &record.WorkflowUID, &record.ReviewPrefix, &record.ReviewManifestKey,
		&record.OutputManifest, &record.HoldActive, &record.SafeErrorCode,
		&record.SafeErrorMessage, &record.CreatedAt, &record.UpdatedAt)
	return record, err
}

func scanApproval(row rowScanner) (Approval, error) {
	var approval Approval
	err := row.Scan(&approval.OutputID, &approval.RequestedByAdmin, &approval.Status,
		&approval.WorkspacePrefix, &approval.WorkspaceManifestKey, &approval.ApprovedFileIDs,
		&approval.AttemptCount, &approval.SafeErrorCode, &approval.SafeErrorMessage,
		&approval.CreatedAt, &approval.UpdatedAt)
	return approval, err
}

func (s *Store) Create(ctx context.Context, params CreateParams) (Record, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanRecord(tx.QueryRow(ctx,
		`SELECT `+outputColumns+` FROM output_jobs WHERE user_id=$1 AND idempotency_key=$2`,
		params.UserID, params.IdempotencyKey))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, err
	}

	// Booking lifecycle handlers lock bookings before notebooks. Use the same
	// ordering here to avoid deadlocks with concurrent Reset or Terminate.
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

	outputID := uuid.NewString()
	reviewPrefix := fmt.Sprintf("nha-review/users/%s/sandboxes/%s/outputs/%s/",
		params.UserID, notebookName, outputID)
	record, err := scanRecord(tx.QueryRow(ctx, `INSERT INTO output_jobs (
		output_id, notebook_id, user_id, notebook_name, namespace, source_pvc,
		source_notebook_path, status, review_prefix, review_manifest_key, idempotency_key, hold_active
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true)
	ON CONFLICT DO NOTHING RETURNING `+outputColumns,
		outputID, notebookID, params.UserID, notebookName, namespace, sourcePVC,
		params.SourceNotebookPath, StatusStopping, reviewPrefix, reviewPrefix+"manifest.json",
		params.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingErr := scanRecord(tx.QueryRow(ctx,
			`SELECT `+outputColumns+` FROM output_jobs WHERE user_id=$1 AND idempotency_key=$2`,
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
		return Record{}, false, ErrActiveOutput
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *Store) MarkNotebookStopped(ctx context.Context, outputID string) error {
	result, err := s.pool.Exec(ctx, `UPDATE output_jobs SET status=$2
		WHERE output_id=$1 AND status=$3 AND hold_active=true`,
		outputID, StatusWaitingForPVC, StatusStopping)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOutputNotFound
	}
	return nil
}

func (s *Store) MarkStopFailed(ctx context.Context, outputID, code, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE output_jobs SET status=$2, hold_active=false,
		safe_error_code=$3, safe_error_message=$4, completed_at=NOW()
		WHERE output_id=$1 AND status=$5`, outputID, StatusFailed, code, message, StatusStopping)
	return err
}

func (s *Store) GetOwned(ctx context.Context, outputID, userID string) (Record, error) {
	record, err := scanRecord(s.pool.QueryRow(ctx,
		`SELECT `+outputColumns+` FROM output_jobs WHERE output_id=$1 AND user_id=$2`,
		outputID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrOutputNotFound
	}
	return record, err
}

func (s *Store) GetByID(ctx context.Context, outputID string) (Record, error) {
	record, err := scanRecord(s.pool.QueryRow(ctx,
		`SELECT `+outputColumns+` FROM output_jobs WHERE output_id=$1`, outputID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrOutputNotFound
	}
	return record, err
}

func (s *Store) GetApproval(ctx context.Context, outputID string) (Approval, bool, error) {
	approval, err := scanApproval(s.pool.QueryRow(ctx,
		`SELECT `+approvalColumns+` FROM output_approvals WHERE output_id=$1`, outputID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, nil
	}
	return approval, err == nil, err
}

func (s *Store) ListPending(ctx context.Context, limit int) ([]Record, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+outputColumns+`
		FROM output_jobs
		WHERE status=$1
		  AND NOT EXISTS (
			SELECT 1 FROM output_approvals
			WHERE output_approvals.output_id=output_jobs.output_id
			  AND output_approvals.status IN ('requested','publishing','approved')
		  )
		ORDER BY created_at LIMIT $2`, StatusPendingApproval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) HasActiveHold(ctx context.Context, notebookID int64) (bool, error) {
	var held bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM output_jobs WHERE notebook_id=$1 AND hold_active=true)`,
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
	var ownerID string
	err = tx.QueryRow(ctx, `SELECT status, user_id FROM output_jobs
		WHERE output_id=$1 FOR UPDATE`, params.OutputID).Scan(&status, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, ErrOutputNotFound
	}
	if err != nil {
		return Approval{}, false, err
	}
	if status != StatusPendingApproval {
		return Approval{}, false, ErrOutputNotReady
	}
	workspacePrefix := fmt.Sprintf("user-workspaces/users/%s/outputs/%s/", ownerID, params.OutputID)

	existing, err := scanApproval(tx.QueryRow(ctx,
		`SELECT `+approvalColumns+` FROM output_approvals WHERE output_id=$1`, params.OutputID))
	if err == nil {
		if existing.Status != ApprovalFailed {
			return existing, false, nil
		}
		retried, retryErr := scanApproval(tx.QueryRow(ctx, `UPDATE output_approvals
			SET status=$2, idempotency_key=$3, requested_by_admin=$4,
			    safe_error_code=NULL, safe_error_message=NULL, claimed_by=NULL,
			    claim_expires_at=NULL, workspace_manifest_key=NULL,
			    approved_file_ids=NULL, attempt_count=attempt_count+1, completed_at=NULL
			WHERE output_id=$1 RETURNING `+approvalColumns,
			params.OutputID, ApprovalRequested, params.IdempotencyKey, params.RequestedByAdmin))
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

	approval, err := scanApproval(tx.QueryRow(ctx, `INSERT INTO output_approvals
		(output_id, requested_by_admin, status, workspace_prefix, idempotency_key)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+approvalColumns,
		params.OutputID, params.RequestedByAdmin, ApprovalRequested, workspacePrefix, params.IdempotencyKey))
	if err != nil {
		return Approval{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Approval{}, false, err
	}
	return approval, true, nil
}
