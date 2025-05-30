package main

import (
	"context"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/db"

	"github.com/jackc/pgx/v5"
)

func FetchAndMarkNotebook(pg *db.PgPool, ctx context.Context) ([]Notebook, error) {
	tx, err := pg.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}

	defer tx.Rollback(ctx)
	query := `
		SELECT id, name, namespace, storage_size, pvc_name, cpu_request, cpu_limit, 
		       memory_request, memory_limit, gpu_type, gpu_count, template_name
		FROM notebooks
		WHERE picked_at IS NULL
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	var notebooks []Notebook
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
			&newNotebook.GPUCount,
			&newNotebook.TemplateName,
		); err != nil {
			return nil, err
		}
		notebooks = append(notebooks, newNotebook)
	}

	if len(notebooks) == 0 {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return notebooks, nil
	}

	updateQuery := `
	UPDATE notebooks
	SET picked_at = NOW(),
            events = array_append(events, $2)
	WHERE id = $1
        `
	_, err = tx.Exec(ctx, updateQuery, notebooks[0].ID, constants.StatusPicked)
	if err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}

	return notebooks, nil
}

func (w *worker) NotebookStatusUpdate(id int64, status constants.Events) error {
	updateQuery := `
	UPDATE notebooks
	SET picked_at = NOW(),
	    events = array_append(events, $2)
	WHERE id = $1
        `
	_, err := w.app.pgPool.Pool.Exec(context.Background(), updateQuery, id, status)
	return err
}
