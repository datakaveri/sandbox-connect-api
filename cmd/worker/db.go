package main

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/db"

	"github.com/jackc/pgx/v5"
)

func FetchAndMarkNotebook(pg *db.PgPool, logger *slog.Logger, originalCtx context.Context) ([]Notebook, error) {
	logger = logger.With("operation", "FetchAndMarkNotebook")
	ctx, cancel := WithTimeoutContext(originalCtx, DBTransactionTimeout)
	defer cancel()

	var notebooks []Notebook
	err := WithDBRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		tx, err := pg.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			logger.Warn("failed to begin transaction, will retry", "error", err)
			return constants.RetryContinue, err
		}
		defer tx.Rollback(ctx)

		notebooks = nil

		query := `
			SELECT id, name, namespace, storage_size, pvc_name, cpu_request, cpu_limit, 
			       memory_request, memory_limit, gpu_type, gpu_request, gpu_limit, instance_type, template_name, image_name,
			       file_url, git_url, git_token_secret_name
			FROM notebooks
			WHERE picked_at IS NULL
			  AND events[array_upper(events, 1)] <> 'deleted'
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`

		queryCtx, queryCancel := WithTimeoutContext(ctx, DBReadTimeout)
		defer queryCancel()

		rows, err := tx.Query(queryCtx, query)
		if err != nil {
			logger.Warn("failed to query notebooks, will retry", "error", err)
			return constants.RetryContinue, err
		}
		defer rows.Close()

		if rows.Err() != nil {
			logger.Warn("rows error, will retry", "error", rows.Err())
			return constants.RetryContinue, rows.Err()
		}

		for rows.Next() {
			var newNotebook Notebook
			if err := rows.Scan(
				&newNotebook.ID,
				&newNotebook.Name,
				&newNotebook.Namespace,
				&newNotebook.StorageSize,
				&newNotebook.PVCname,
				&newNotebook.CPURequest,
				&newNotebook.CPULimit,
				&newNotebook.MemoryRequest,
				&newNotebook.MemoryLimit,
				&newNotebook.GPUType,
				&newNotebook.GPURequest,
				&newNotebook.GPULimit,
				&newNotebook.InstanceType,
				&newNotebook.TemplateName,
				&newNotebook.ImageName,
				&newNotebook.FileURL,
				&newNotebook.GitURL,
				&newNotebook.GitTokenSecretName,
			); err != nil {
				logger.Warn("failed to scan notebook row, will retry", "error", err)
				return constants.RetryContinue, err
			}
			notebooks = append(notebooks, newNotebook)
		}

		if len(notebooks) == 0 {
			if err = tx.Commit(ctx); err != nil {
				logger.Warn("failed to commit empty transaction, will retry", "error", err)
				return constants.RetryContinue, err
			}
			return constants.RetryStop, nil
		}

		updateCtx, updateCancel := WithTimeoutContext(ctx, DBWriteTimeout)
		defer updateCancel()

		updateQuery := `
		UPDATE notebooks
		SET picked_at = NOW(),
			events = array_append(events, $2)
		WHERE id = $1
		`
		_, err = tx.Exec(updateCtx, updateQuery, notebooks[0].ID, constants.StatusPicked)
		if err != nil {
			logger.Warn("failed to update notebook status, will retry", "error", err)
			return constants.RetryContinue, err
		}

		if err = tx.Commit(ctx); err != nil {
			logger.Warn("failed to commit transaction, will retry", "error", err)
			return constants.RetryContinue, err
		}

		return constants.RetryStop, nil
	})

	if err != nil {
		logger.Error("failed to fetch and mark notebook after retries", "error", err)
		return nil, err
	}

	if len(notebooks) > 0 {
		logger.Info("notebook marked for processing", "notebookId", notebooks[0].ID, "name", notebooks[0].Name)
	}
	return notebooks, nil
}

func (w *worker) NotebookStatusUpdate(id int64, status constants.Events) error {
	logger := w.logger.With("operation", "NotebookStatusUpdate", "notebookId", id, "status", status)
	logger.Debug("updating notebook status")

	ctx, cancel := WithTimeoutContext(context.Background(), DBWriteTimeout)
	defer cancel()

	updateQuery := `
	UPDATE notebooks
	SET events = array_append(events, $2)
	WHERE id = $1
    `

	err := WithDBRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := w.app.pgPool.Pool.Exec(ctx, updateQuery, id, status)
		return constants.RetryStop, err
	})

	return err
}
