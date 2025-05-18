package main

import (
	"context"
	"fmt"
	"net/http"

	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"

	"github.com/jackc/pgx/v5"
)

func (app *application) checkNotebookExists(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	checkReq, err := utils.DecodeAndValidate[CheckExistsRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	ctx := context.Background()
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM notebooks WHERE name = $1 AND namespace = $2)`
	err = app.pgPool.Pool.QueryRow(ctx, query, checkReq.Name, checkReq.Namespace).Scan(&exists)
	if err != nil {
		logger.Error("failed to check notebook existence", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to check notebook existence")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"exists": exists})
}

func (app *application) checkPVCExists(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	checkReq, err := utils.DecodeAndValidate[CheckExistsRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	ctx := context.Background()
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM notebooks WHERE pvc_name = $1 AND namespace = $2)`
	err = app.pgPool.Pool.QueryRow(ctx, query, checkReq.Name, checkReq.Namespace).Scan(&exists)
	if err != nil {
		logger.Error("failed to check PVC existence", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to check PVC existence")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"exists": exists})
}

func (app *application) createNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	notebookReq, err := utils.DecodeAndValidate[NotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	if notebookReq.GPU.Limit > 0 && !IsValidGPUResource(notebookReq.GPU.Type) {
		logger.Error("invalid gpu type", "gpu_type", notebookReq.GPU.Type)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid GPU Type")
		return
	}
	if notebookReq.TemplateName != "ai" && notebookReq.TemplateName != "ml" {
		logger.Error("invalid template name", "template_name", notebookReq.TemplateName)
		jsonResponse(w, http.StatusUnprocessableEntity, map[string]string{"error": "Invalid Template Name"})
		return
	}

	memoryRequest := fmt.Sprintf("%dGi", int(notebookReq.MemoryInGi.Request))
	memoryLimit := fmt.Sprintf("%dGi", int(notebookReq.MemoryInGi.Limit))
	storageSize := fmt.Sprintf("%.2fGi", notebookReq.StorageSizeInGi)

	userID := "00000000-0000-0000-0000-000000000000"
	baseArgs := []any{
		userID,
		notebookReq.Name,
		notebookReq.Namespace,
		storageSize,
		notebookReq.PVCName,
		notebookReq.CPU.Request,
		notebookReq.CPU.Limit,
		memoryRequest,
		memoryLimit,
	}
	var query string
	var args []any

	ctx := context.Background()

	var notebookId int64
	if notebookReq.GPU.Limit > 0 {
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit, 
			gpu_type, gpu_count, template_name
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		) RETURNING id`

		args = append(baseArgs, notebookReq.GPU.Type, notebookReq.GPU.Limit, notebookReq.TemplateName)
	} else {
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit, 
			template_name
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		) RETURNING id`

		args = append(baseArgs, notebookReq.TemplateName)
	}

	err = app.pgPool.Pool.QueryRow(ctx, query, args...).Scan(&notebookId)
	if err != nil {
		logger.Error("failed to create notebook", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to create notebook")
		return
	}
	sendResponse(w, r, logger, http.StatusCreated, "notebook creation is in process")
}
func (app *application) stopNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	stopReq, err := utils.DecodeAndValidate[StopNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}
	logger = logger.With("method", "stopNotebook", "namespace", stopReq.Namespace, "name", stopReq.Name)

	ctx := context.Background()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, stopReq.Name, stopReq.Namespace).Scan(&notebookID, &latestEvent)
	if err != nil {
		logger.Error("failed to find notebook", "error", err)
		sendResponse(w, r, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != string(constants.StatusNotebookApplied) {
		logger.Error("cannot stop notebook that is not in applied state", "currentState", latestEvent)
		sendResponse(w, r, logger, http.StatusBadRequest, "Cannot stop notebook that is not in applied state")
		return
	}

	err = addStoppedAnnotationToNotebook(app.k8sClient, stopReq.Namespace, stopReq.Name)
	if err != nil {
		logger.Error("failed to add stopped annotation to notebook", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to stop notebook")
		return
	}

	sendResponse(w, r, logger, http.StatusOK, "Notebook stopped successfully")
}

func (app *application) startNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	startReq, err := utils.DecodeAndValidate[StartNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}
	logger = logger.With("method", "startNotebook", "namespace", startReq.Namespace, "name", startReq.Name)
	ctx := context.Background()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, startReq.Name, startReq.Namespace).Scan(&notebookID, &latestEvent)
	if err != nil {
		logger.Error("failed to find notebook", "error", err)
		sendResponse(w, r, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != string(constants.StatusNotebookApplied) {
		logger.Error("cannot start notebook that is not in applied state", "currentState", latestEvent)
		sendResponse(w, r, logger, http.StatusBadRequest, "Cannot start notebook that is not in applied state")
		return
	}

	err = removeStoppedAnnotationFromNotebook(app.k8sClient, startReq.Namespace, startReq.Name)
	if err != nil {
		logger.Error("failed to remove stopped annotation from notebook", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to start notebook")
		return
	}

	sendResponse(w, r, logger, http.StatusOK, "Notebook started successfully")
}

func (app *application) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	deleteReq, err := utils.DecodeAndValidate[DeleteNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}
	logger = logger.With("method", "deleteNotebook", "namespace", deleteReq.Namespace, "name", deleteReq.Name)
	query := `
		SELECT events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	var latestEvent string
	ctx := context.Background()
	err = app.pgPool.Pool.QueryRow(ctx, query, deleteReq.Name, deleteReq.Namespace).Scan(&latestEvent)
	if err != nil {
		if err == pgx.ErrNoRows {
			logger.Error("notebook not found", "error", err)
			sendResponse(w, r, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		logger.Error("failed to select notebook", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "internal server error")
		return
	}

	if latestEvent != string(constants.StatusNotebookApplied) {
		logger.Error("cannot delete notebook that is not in applied state", "currentState", latestEvent)
		sendResponse(w, r, logger, http.StatusBadRequest, "Cannot delete notebook that is not in applied state")
		return
	}

	deleteQuery := `DELETE FROM notebooks WHERE name = $1 AND namespace = $2`
	_, err = app.pgPool.Pool.Exec(ctx, deleteQuery, deleteReq.Name, deleteReq.Namespace)
	if err != nil {
		logger.Error("failed to delete notebook from database", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from database")
		return
	}

	err = deleteNotebookFromK8s(app.k8sClient, deleteReq.Namespace, deleteReq.Name)
	if err != nil {
		logger.Error("failed to delete notebook from Kubernetes", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from Kubernetes")
		return
	}

	sendResponse(w, r, logger, http.StatusOK, "Notebook deleted successfully")
}

func (app *application) listNotebooks(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	listReq, err := utils.DecodeAndValidate[ListNotebooksRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}
	logger = logger.With("method", "listNotebooks", "namespace", listReq.Namespace)
	ctx := context.Background()

	k8sNotebooks, err := getNotebooksJSON(app.k8sClient, listReq.Namespace)
	if err != nil {
		logger.Warn("failed to get notebooks from Kubernetes", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	query := `
		SELECT id, name, namespace, storage_size, pvc_name, 
		       cpu_request, cpu_limit, memory_request, memory_limit, 
		       gpu_type, gpu_count, template_name, 
		       events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE namespace = $1
	`
	rows, err := app.pgPool.Pool.Query(ctx, query, listReq.Namespace)
	if err != nil {
		logger.Error("failed to query notebooks", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to query notebooks")
		return
	}
	defer rows.Close()

	response := ListNotebooksResponse{
		Successful: []NotebookDetails{},
		Pending:    []NotebookDetails{},
		Failed:     []NotebookDetails{},
	}

	for rows.Next() {
		var notebook NotebookDetails
		err := rows.Scan(
			&notebook.ID, &notebook.Name, &notebook.Namespace, &notebook.StorageSize,
			&notebook.PVCName, &notebook.CPURequest, &notebook.CPULimit,
			&notebook.MemoryRequest, &notebook.MemoryLimit, &notebook.GPUType,
			&notebook.GPUCount, &notebook.TemplateName, &notebook.LatestEvent,
		)
		if err != nil {
			logger.Error("failed to scan notebook row", "error", err)
			continue
		}

		if notebook.LatestEvent == string(constants.StatusNotebookApplied) {
			k8sSpec, exists := k8sNotebooks[notebook.Name]
			if !exists {
				logger.Warn("notebook marked as applied but not found in k8s", "name", notebook.Name)
				response.Pending = append(response.Pending, notebook)
				continue
			}
			k8sSpecMap, ok := k8sSpec.(map[string]any)
			if !ok {
				logger.Warn("expected map[string]any for k8s notebook spec", "name", notebook.Name)
				response.Pending = append(response.Pending, notebook)
				continue
			}
			notebook.K8sSpec = k8sSpecMap
			response.Successful = append(response.Successful, notebook)
		} else if notebook.LatestEvent == string(constants.StatusNotebookAppllyFailed) ||
			notebook.LatestEvent == string(constants.StatusPVCUploadFailed) ||
			notebook.LatestEvent == string(constants.StatusPVCUploadApplyFailed) {
			response.Failed = append(response.Failed, notebook)
		} else {
			response.Pending = append(response.Pending, notebook)
		}
	}

	if err := rows.Err(); err != nil {
		logger.Error("error iterating notebook rows", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "internal server error")
		return
	}

	sendResponseJson(w, r, logger, http.StatusOK, response)
}
