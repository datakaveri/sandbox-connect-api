package main

import (
	"encoding/json"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (app *application) checkNotebookExists(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	notebookName := r.PathValue("notebook_name")
	if notebookName == "" {
		logger.Error("notebook name is empty", "notebookName", notebookName)
		sendError(w, r, logger, http.StatusBadRequest, "Notebook name is empty")
		return
	}

	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM notebooks WHERE name = $1 AND namespace = $2)`
	err := app.pgPool.Pool.QueryRow(r.Context(), query, notebookName, namespace).Scan(&exists)
	if err != nil {
		logger.Error("failed to check notebook existence", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to check notebook existence")
		return
	}
	sendResponseJson(w, r, logger, http.StatusOK, map[string]bool{"exists": exists})
}

func (app *application) createNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	notebookReq, err := utils.DecodeAndValidate[NotebookRequest](r.Body, logger)

	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	if notebookReq.Type != "cpu" && notebookReq.Type != "gpu" {
		logger.Error("invalid notebook type", "type", notebookReq.Type)
		sendError(w, r, logger, http.StatusUnprocessableEntity, "Invalid Notebook Type")
		return
	}

	baseArgs := []any{
		namespace,
		notebookReq.Name,
		namespace,
		app.env.NotebookConfig.StorageSize,
		notebookReq.Name + "-pvc",
		app.env.NotebookConfig.CPURequest,
		app.env.NotebookConfig.CPULimit,
		app.env.NotebookConfig.MemoryRequest,
		app.env.NotebookConfig.MemoryLimit,
	}

	var query string
	if notebookReq.Type == "gpu" {
		baseArgs = append(baseArgs, app.env.NotebookConfig.GPUType, app.env.NotebookConfig.GPULimit)
		query = `
			INSERT INTO notebooks (
				user_id, name, namespace, storage_size, pvc_name, 
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_count
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
			) RETURNING id`
	} else {
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9
		) RETURNING id`
	}
	var notebookId int64
	err = app.pgPool.Pool.QueryRow(r.Context(), query, baseArgs...).Scan(&notebookId)
	if err != nil {
		logger.Error("failed to create notebook in database", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to create notebook")
		return
	}
	logger.Info("notebook created successfully", "notebookId", notebookId)
	sendResponse(w, r, logger, http.StatusCreated, "Notebook creation is in process")
}

func (app *application) stopNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	stopReq, err := utils.DecodeAndValidate[StopNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("method", "stopNotebook", "namespace", namespace, "name", stopReq.Name)

	ctx := r.Context()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(r.Context(), query, stopReq.Name, namespace).Scan(&notebookID, &latestEvent)
	if err != nil {
		logger.Error("failed to find notebook", "error", err)
		sendError(w, r, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != string(constants.StatusNotebookApplied) {
		logger.Error("cannot stop notebook that is not in applied state", "currentState", latestEvent)
		sendError(w, r, logger, http.StatusBadRequest, "Cannot stop notebook that is not in applied state")
		return
	}

	err = app.addStoppedAnnotationToNotebook(ctx, namespace, stopReq.Name)
	if err != nil {
		logger.Error("failed to add stopped annotation to notebook", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to stop notebook")
		return
	}

	sendResponse(w, r, logger, http.StatusOK, "Notebook stopped successfully")
}

func (app *application) startNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	startReq, err := utils.DecodeAndValidate[StartNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("method", "startNotebook", "namespace", namespace, "name", startReq.Name)
	ctx := r.Context()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, startReq.Name, namespace).Scan(&notebookID, &latestEvent)
	if err != nil {
		logger.Error("failed to find notebook", "error", err)
		sendError(w, r, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != string(constants.StatusNotebookApplied) {
		logger.Error("cannot start notebook that is not in applied state", "currentState", latestEvent)
		sendError(w, r, logger, http.StatusBadRequest, "Cannot start notebook that is not in applied state")
		return
	}

	err = app.removeStoppedAnnotationFromNotebook(ctx, namespace, startReq.Name)
	if err != nil {
		logger.Error("failed to remove stopped annotation from notebook", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to start notebook")
		return
	}
	sendResponse(w, r, logger, http.StatusOK, "Notebook started successfully")
}

func (app *application) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	deleteReq, err := utils.DecodeAndValidate[DeleteNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("method", "deleteNotebook", "namespace", namespace, "name", deleteReq.Name)
	query := `
		SELECT events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	var latestEvent string
	ctx := r.Context()
	err = app.pgPool.Pool.QueryRow(ctx, query, deleteReq.Name, namespace).Scan(&latestEvent)
	if err != nil {
		if err == pgx.ErrNoRows {
			logger.Error("notebook not found", "error", err)
			sendError(w, r, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		logger.Error("failed to select notebook", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook")
		return
	}

	notebookFailed := latestEvent == string(constants.StatusPVCUploadFailed) ||
		latestEvent == string(constants.StatusPVCUploadApplyFailed) ||
		latestEvent == string(constants.StatusNotebookApplyFailed) ||
		latestEvent == string(constants.StatusPVCCreationFailed) ||
		latestEvent == string(constants.StatusPVCApplyFailed)

	if latestEvent != string(constants.StatusNotebookApplied) && !notebookFailed {
		logger.Error("cannot delete notebook that is not in applied state", "currentState", latestEvent)
		sendError(w, r, logger, http.StatusBadRequest, "Cannot delete notebook that is not in applied state")
		return
	}

	deleteQuery := `DELETE FROM notebooks WHERE name = $1 AND namespace = $2`
	_, err = app.pgPool.Pool.Exec(ctx, deleteQuery, deleteReq.Name, namespace)
	if err != nil {
		logger.Error("failed to delete notebook from database", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from database")
		return
	}

	if !notebookFailed {
		err = app.deleteNotebookFromK8s(ctx, namespace, deleteReq.Name)
		if err != nil {
			logger.Error("failed to delete notebook from Kubernetes", "error", err)
			sendError(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from Kubernetes")
			return
		}
		err = app.deletePVCFromK8s(ctx, namespace, deleteReq.Name+"-pvc")
		if err != nil {
			logger.Error("failed to delete PVC from Kubernetes", "error", err)
			sendError(w, r, logger, http.StatusInternalServerError, "Failed to delete PVC from Kubernetes")
			return
		}
	}
	sendResponse(w, r, logger, http.StatusOK, "Notebook deleted successfully")
}

func (app *application) listNotebooks(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	logger = logger.With("method", "listNotebooks")
	ctx := r.Context()

	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	filterVal := r.URL.Query().Get("filter")
	var filterDates [2]string
	var filterActive bool

	if filterVal != "" {
		var dates []string
		if err := json.Unmarshal([]byte(filterVal), &dates); err != nil {
			logger.Error("failed to parse filter as JSON array", "error", err, "filter", filterVal)
			sendError(w, r, logger, http.StatusBadRequest, "filter must be a valid JSON array of two date strings")
			return
		}

		if len(dates) != 2 {
			sendError(w, r, logger, http.StatusBadRequest, "filter must contain exactly two date strings")
			return
		}

		for i, val := range dates {
			if _, err := time.Parse("2006-01-02", val); err != nil {
				sendError(w, r, logger, http.StatusBadRequest, "filter values must be valid date strings (YYYY-MM-DD)")
				return
			}
			filterDates[i] = val
		}
		filterActive = true
	}
	limit := app.env.NotebookConfig.DefaultNotebookListLimit
	if limStr := r.URL.Query().Get("limit"); limStr != "" {
		if l, err := strconv.Atoi(limStr); err == nil && l > 0 {
			limit = l
		}
	}
	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	k8sNotebooks, err := app.getNotebooksJSON(r.Context(), namespace)
	if err != nil {
		logger.Warn("failed to get notebooks from Kubernetes", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	var query string
	var rows pgx.Rows
	if filterActive {
		query = `
			SELECT id, name, namespace, storage_size, pvc_name,
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_count, template_name, events
			FROM notebooks
			WHERE namespace = $1 AND created_at >= $2 AND created_at <= $3
			ORDER BY id
			LIMIT $4 OFFSET $5`
		rows, err = app.pgPool.Pool.Query(ctx, query, namespace, filterDates[0], filterDates[1], limit, offset)
	} else {
		query = `
			SELECT id, name, namespace, storage_size, pvc_name,
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_count, template_name, events
			FROM notebooks
			WHERE namespace = $1
			ORDER BY id
			LIMIT $2 OFFSET $3`
		rows, err = app.pgPool.Pool.Query(ctx, query, namespace, limit, offset)
	}
	if err != nil {
		logger.Error("failed to query notebooks", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to query notebooks")
		return
	}
	defer rows.Close()

	notebooks := []NotebookStatus{}
	for rows.Next() {
		var nb NotebookStatus
		err := rows.Scan(
			&nb.ID, &nb.Name, &nb.Namespace, &nb.StorageSize, &nb.PVCName,
			&nb.CPURequest, &nb.CPULimit, &nb.MemoryRequest, &nb.MemoryLimit,
			&nb.GPUType, &nb.GPUCount, &nb.TemplateName, &nb.Events,
		)
		if err != nil {
			logger.Error("failed to scan notebook row", "error", err)
			continue
		}
		var latestEvent constants.Events
		if len(nb.Events) > 0 {
			latestEvent = nb.Events[len(nb.Events)-1]
		}

		var k8sObject map[string]any = nil
		if latestEvent == constants.StatusNotebookApplied {
			if raw, ok := k8sNotebooks[nb.Name]; ok && raw != nil {
				if obj, ok := raw.(map[string]any); ok {
					k8sObject = obj
				} else {
					logger.Warn("k8sNotebooks entry is not a map[string]any", "name", nb.Name)
				}
			}
		}
		nb.Status = determineNotebookState(latestEvent, k8sObject)
		if nb.Status == NotebookStateRunning {
			nb.URL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, nb.Namespace, nb.Name)
		}
		notebooks = append(notebooks, nb)
	}
	if err := rows.Err(); err != nil {
		logger.Error("error iterating notebook rows", "error", err)
		sendError(w, r, logger, http.StatusInternalServerError, "internal server error")
		return
	}

	nextOffset := -1
	if len(notebooks) == limit {
		nextOffset = offset + limit
	}
	resp := struct {
		Notebooks  []NotebookStatus `json:"notebooks"`
		NextOffset int              `json:"next_offset"`
	}{
		Notebooks:  notebooks,
		NextOffset: nextOffset,
	}
	sendResponseJson(w, r, logger, http.StatusOK, resp)
}

func (app *application) checkNotebookStatus(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	namespace := userInfo.Sub
	notebookName := r.PathValue("notebook_name")
	if notebookName == "" {
		logger.Error("notebook name is required", "error", "notebook name is required")
		sendError(w, r, logger, http.StatusBadRequest, "Notebook name is required")
		return
	}

	logger = logger.With("method", "checkNotebookStatus", "namespace", namespace, "name", notebookName)
	ctx := r.Context()
	var status NotebookStatus
	query := `
		SELECT id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit,
			gpu_type, gpu_count, template_name, events
		FROM notebooks
		WHERE namespace= $1 and name = $2`

	err := app.pgPool.Pool.QueryRow(ctx, query, namespace, notebookName).Scan(
		&status.ID,
		&status.Name,
		&status.Namespace,
		&status.StorageSize,
		&status.PVCName,
		&status.CPURequest,
		&status.CPULimit,
		&status.MemoryRequest,
		&status.MemoryLimit,
		&status.GPUType,
		&status.GPUCount,
		&status.TemplateName,
		&status.Events,
	)
	if err != nil {
		sendResponse(w, r, logger, http.StatusInternalServerError, "internal server error")
		return
	}

	var latestEvent constants.Events
	if len(status.Events) > 0 {
		latestEvent = status.Events[len(status.Events)-1]
	}

	var k8sSpec *unstructured.Unstructured
	if latestEvent == constants.StatusNotebookApplied {
		var err error
		k8sSpec, err = app.getNotebookJSON(ctx, status.Namespace, status.Name)
		if err != nil {
			logger.Error("failed to get notebook from Kubernetes", "error", err)
			k8sSpec = nil
		}
	}

	var k8sObject map[string]any = nil
	if k8sSpec != nil {
		k8sObject = k8sSpec.Object
	}
	status.Status = determineNotebookState(latestEvent, k8sObject)

	if status.Status == NotebookStateRunning {
		status.URL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, status.Namespace, status.Name)
	}

	sendResponseJson(w, r, logger, http.StatusOK, status)
}

func (app *application) createProfile(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)

	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, r, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	email := userInfo.Email
	userId := userInfo.Sub

	logger = logger.With("userId", userId, "email", email)

	err := app.createKubeflowProfile(r.Context(), userId, email)
	if err != nil {
		logger.Error("failed to create kubeflow profile", "error", err, "profileName", userId)
		sendError(w, r, logger, http.StatusInternalServerError, "Failed to create Kubeflow Profile")
		return
	}

	logger.Info("kubeflow profile created successfully", "profileName", userId)
	sendResponse(w, r, logger, http.StatusCreated, "Kubeflow Profile created successfully")
}
