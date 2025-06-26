package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// checkNotebookExists godoc
// @Summary      Check if notebook exists
// @Description  Checks if a notebook with the given name exists
// @Tags         notebook
// @Produce      json
// @Param        notebook_name  path  string  true  "Notebook Name"
// @Success      200  {object}  SwaggerExistsResponse
// @Failure      400  {object}  Error400
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/check-exists/{notebook_name} [get]
func (app *application) checkNotebookExists(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	notebookName := r.PathValue("notebook_name")

	if errorMessage, gotError := getErrorMessageForNotebookName(notebookName); gotError {
		logger.Error("invalid notebook name", "notebookName", notebookName)
		sendError(w, logger, http.StatusBadRequest, errorMessage)
		return
	}

	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM notebooks WHERE name = $1 AND namespace = $2)`
	err := app.pgPool.Pool.QueryRow(r.Context(), query, notebookName, namespace).Scan(&exists)
	if err != nil {
		logger.Error("failed to check notebook existence", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to check notebook existence")
		return
	}
	sendResponseJson(w, logger, http.StatusOK, map[string]bool{"exists": exists})
}

// createNotebook godoc
// @Summary      Create notebook
// @Description  Creates a new notebook for the user
// @Tags         notebook
// @Accept       json
// @Produce      json
// @Param        notebook  body  NotebookRequest  true  "Notebook Create Request"
// @Success      201  {object}  SwaggerMessageResponse
// @Failure      422  {object}  Error422
// @Failure      403  {object}  Error403
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      500  {object}  Error500
// @Failure      409  {object}  Error409
// @Security     BearerAuth
// @Router       /v1/notebook/create [post]
func (app *application) createNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub

	notebookReq, err := utils.DecodeAndValidate[NotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("can't parse the body", "Error", err.Error())
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid body")
		return
	}
	if errorMessage, gotError := getErrorMessageForNotebookName(notebookReq.Name); gotError {
		logger.Error("invalid notebook name",
			"notebook_name", notebookReq.Name,
			"validation_error", errorMessage)
		sendError(w, logger, http.StatusUnprocessableEntity, errorMessage)
		return
	}
	if notebookReq.Type != "cpu" && notebookReq.Type != "gpu" {
		logger.Error("invalid notebook type",
			"notebook_name", notebookReq.Name,
			"notebook_type", notebookReq.Type,
			"valid_types", "cpu,gpu")
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid Notebook Type")
		return
	}
	if notebookReq.Type == "gpu" && !contains(userInfo.Roles, "compute") {
		sendError(w, logger, http.StatusForbidden, "You don't have compute permissions")
		return
	}

	ctx := r.Context()

	var profileExists bool
	checkProfileQuery := `SELECT EXISTS(SELECT 1 FROM profiles WHERE user_id = $1)`
	err = app.pgPool.Pool.QueryRow(ctx, checkProfileQuery, userInfo.Sub).Scan(&profileExists)
	if err != nil {
		logger.Error("failed to check if profile exists", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}

	if !profileExists {
		logger.Info("profile doesn't exist, creating profile automatically", "user_id", userInfo.Sub, "email", userInfo.Email)
		err = app.createKubeflowProfile(ctx, logger, userInfo.Sub, userInfo.Email)
		if err != nil && !errors.IsAlreadyExists(err) {
			logger.Error("failed to create kubeflow profile", "error", err, "user_id", userInfo.Sub)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		createProfileQuery := `
			INSERT INTO profiles (user_id, email)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO NOTHING
		`
		_, err = app.pgPool.Pool.Exec(ctx, createProfileQuery, userInfo.Sub, userInfo.Email)
		if err != nil {
			logger.Error("failed to create profile in database", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}

		logger.Info("profile created successfully", "user_id", userInfo.Sub, "email", userInfo.Email)
	}

	tx, err := app.pgPool.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("failed to start transaction", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback(ctx)

	var canCreateGpuNotebook bool
	lockQuery := `SELECT can_create_gpu_notebook FROM profiles WHERE user_id = $1 FOR UPDATE NOWAIT`
	err = tx.QueryRow(ctx, lockQuery, userInfo.Sub).Scan(&canCreateGpuNotebook)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "55P03" {
			logger.Warn("profile is locked by another operation",
				"notebook_name", notebookReq.Name,
				"notebook_type", notebookReq.Type,
				"lock_error_code", pgErr.Code)
			sendError(w, logger, http.StatusTooManyRequests, "Another operation is in progress for your account, please try again shortly.")
			return
		}
		logger.Error("failed to lock profile row",
			"error", err,
			"notebook_name", notebookReq.Name,
			"notebook_type", notebookReq.Type)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := tx.Query(ctx, "SELECT name, gpu_type, gpu_request, gpu_limit, events[array_upper(events, 1)] as latest_event FROM notebooks WHERE user_id = $1", userInfo.Sub)
	if err != nil {
		logger.Error("failed to fetch notebooks for user", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	dbNotebooks := []DBNotebookInfo{}
	for rows.Next() {
		var name string
		var gpuType *string
		var gpuRequest *int
		var gpuLimit *int
		var latestEvent constants.Events
		if err := rows.Scan(&name, &gpuType, &gpuRequest, &gpuLimit, &latestEvent); err != nil {
			logger.Error("failed to scan notebook row", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		nbType := "cpu"
		if utils.CheckGPUResource(gpuType, gpuRequest, gpuLimit) {
			nbType = "gpu"
		}
		dbNotebooks = append(dbNotebooks, DBNotebookInfo{
			Name:        name,
			Type:        nbType,
			LatestEvent: latestEvent,
		})
	}
	if err := rows.Err(); err != nil {
		logger.Error("error iterating notebook rows", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	totalCPU, totalGPU, err := app.getSuccessfullyCreatedNotebookCounts(ctx, dbNotebooks, namespace)
	if err != nil {
		logger.Error("failed to get successfully created notebook counts", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to get successfully created notebook counts")
		return
	}
	runningCPU, runningGPU, err := app.CountEffectiveRunningNotebooks(ctx, dbNotebooks, namespace)
	if err != nil {
		logger.Error("failed to check running notebooks from k8s", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to check running notebooks")
		return
	}
	if runningCPU >= app.env.NotebookConfig.MaxRunningCPU && notebookReq.Type == "cpu" {
		sendError(w, logger, http.StatusBadRequest, "Cannot create notebook: running CPU notebook limit exceeded.")
		return
	}
	if runningGPU >= app.env.NotebookConfig.MaxRunningGPU && notebookReq.Type == "gpu" {
		sendError(w, logger, http.StatusBadRequest, "Cannot create notebook: running GPU notebook limit exceeded.")
		return
	}
	if totalCPU+totalGPU >= app.env.NotebookConfig.MaxTotalCPU+app.env.NotebookConfig.MaxTotalGPU {
		sendError(w, logger, http.StatusBadRequest, "Total notebook limit reached")
		return
	}
	if notebookReq.Type == "cpu" && totalCPU >= app.env.NotebookConfig.MaxTotalCPU {
		sendError(w, logger, http.StatusBadRequest, "CPU notebook limit reached")
		return
	}
	if notebookReq.Type == "gpu" && totalGPU >= app.env.NotebookConfig.MaxTotalGPU {
		sendError(w, logger, http.StatusBadRequest, "GPU notebook limit reached")
		return
	}

	if notebookReq.Type == "gpu" && contains(userInfo.Roles, "compute") {
		if !canCreateGpuNotebook {
			logger.Warn("user doesn't have permission to create GPU notebooks",
				"notebook_name", notebookReq.Name,
				"can_create_gpu_notebook", canCreateGpuNotebook)
			sendError(w, logger, http.StatusForbidden, "You don't have enough credit for this operation")
			return
		}
		logger.Debug("user has permission to create GPU notebooks",
			"notebook_name", notebookReq.Name,
			"can_create_gpu_notebook", canCreateGpuNotebook)
	}

	baseArgs := []any{
		userInfo.Sub,
		notebookReq.Name,
		namespace,
		notebookReq.Name + "-pvc",
	}

	var query string
	if notebookReq.Type == "gpu" {
		baseArgs = append(baseArgs, app.env.NotebookConfig.GPUStorageSize, app.env.NotebookConfig.GPUCPURequest, app.env.NotebookConfig.GPUCPULimit, app.env.NotebookConfig.GPUMemoryRequest, app.env.NotebookConfig.GPUMemoryLimit, app.env.NotebookConfig.GPUType, app.env.NotebookConfig.GPURequest, app.env.NotebookConfig.GPULimit)
		query = `
			INSERT INTO notebooks (
				user_id, name, namespace, pvc_name, storage_size, 
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_request, gpu_limit
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
			) RETURNING id`
	} else {
		baseArgs = append(baseArgs, app.env.NotebookConfig.CPUStorageSize, app.env.NotebookConfig.CPURequest, app.env.NotebookConfig.CPULimit, app.env.NotebookConfig.MemoryRequest, app.env.NotebookConfig.MemoryLimit)
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, pvc_name, storage_size, 
			cpu_request, cpu_limit, memory_request, memory_limit
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9
		) RETURNING id`
	}
	var notebookId int64
	err = tx.QueryRow(ctx, query, baseArgs...).Scan(&notebookId)
	if err != nil {
		if pgErr, isPgError := err.(*pgconn.PgError); isPgError && pgErr.Code == "23505" {
			logger.Warn("notebook already exists", "name", notebookReq.Name)
			sendError(w, logger, http.StatusConflict, "Notebook with this name already exists")
			return
		}
		logger.Error("failed to create notebook in database", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook")
		return
	}

	err = tx.Commit(ctx)
	if err != nil {
		logger.Error("failed to commit transaction", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	logger.Info("notebook created successfully",
		"notebook_id", notebookId,
		"notebook_name", notebookReq.Name,
		"notebook_type", notebookReq.Type)
	sendResponse(w, logger, http.StatusCreated, "Notebook creation is in process")
}

// stopNotebook godoc
// @Summary      Stop notebook
// @Description  Stops a running notebook
// @Tags         notebook
// @Accept       json
// @Produce      json
// @Param        notebook_name  body  StopNotebookRequest  true  "Notebook Stop Request"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      500  {object}  Error500
// @Failure      422  {object}  Error422
// @Failure      404  {object}  Error404
// @Failure      400  {object}  Error400
// @Security     BearerAuth
// @Router       /v1/notebook/stop [patch]
func (app *application) stopNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	stopReq, err := utils.DecodeAndValidate[StopNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("method", "stopNotebook", "namespace", namespace, "name", stopReq.Name)

	ctx := r.Context()
	var latestEvent constants.Events
	query := `
		SELECT events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, stopReq.Name, namespace).Scan(&latestEvent)
	if err != nil {
		logger.Error("failed to find notebook", "error", err)
		sendError(w, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != constants.StatusNotebookApplied {
		logger.Error("cannot stop notebook that is not in applied state", "currentState", latestEvent)
		sendError(w, logger, http.StatusBadRequest, "Cannot stop notebook that is not in applied state")
		return
	}

	err = app.addStoppedAnnotationToNotebook(ctx, namespace, stopReq.Name)
	if err != nil {
		logger.Error("failed to add stopped annotation to notebook", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to stop notebook")
		return
	}

	sendResponse(w, logger, http.StatusOK, "Notebook stopped successfully")
}

// startNotebook godoc
// @Summary      Start notebook
// @Description  Starts an existing notebook
// @Tags         notebook
// @Accept       json
// @Produce      json
// @Param        notebook_name  body  StartNotebookRequest  true  "Notebook Start Request"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      404  {object}  Error404
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      500  {object}  Error500
// @Failure      422  {object}  Error422
// @Security     BearerAuth
// @Router       /v1/notebook/start [patch]
func (app *application) startNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	startReq, err := utils.DecodeAndValidate[StartNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	ctx := r.Context()
	tx, err := app.pgPool.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("failed to start transaction", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback(ctx)

	var canCreateGpuNotebook bool
	lockQuery := `SELECT can_create_gpu_notebook FROM profiles WHERE user_id = $1 FOR UPDATE NOWAIT`
	err = tx.QueryRow(ctx, lockQuery, userInfo.Sub).Scan(&canCreateGpuNotebook)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "55P03" { // lock_not_available
			logger.Warn("profile is locked by another operation",
				"notebook_name", startReq.Name,
				"lock_error_code", pgErr.Code,
				"error", pgErr)
			sendError(w, logger, http.StatusTooManyRequests, "Another operation is in progress for your account, please try again shortly.")
			return
		}
		logger.Error("failed to lock profile row",
			"error", err,
			"notebook_name", startReq.Name)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}

	logger = logger.With("method", "startNotebook", "namespace", namespace, "name", startReq.Name)

	rows, err := tx.Query(ctx, "SELECT id, name, gpu_type, gpu_request, gpu_limit, events[array_upper(events, 1)] as latest_event FROM notebooks WHERE user_id = $1", userInfo.Sub)
	if err != nil {
		logger.Error("failed to fetch notebooks for user", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	dbNotebooks := []DBNotebookInfo{}
	var latestEvent constants.Events
	var gpuType *string
	var gpuRequest, gpuLimit *int
	foundNotebook := false
	for rows.Next() {
		var id int64
		var name string
		var rowGpuType *string
		var rowGpuRequest, rowGpuLimit *int
		var rowLatestEvent constants.Events
		if err := rows.Scan(&id, &name, &rowGpuType, &rowGpuRequest, &rowGpuLimit, &rowLatestEvent); err != nil {
			logger.Error("failed to scan notebook row", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		nbType := "cpu"
		if utils.CheckGPUResource(rowGpuType, rowGpuRequest, rowGpuLimit) {
			nbType = "gpu"
		}
		dbNotebooks = append(dbNotebooks, DBNotebookInfo{
			Name:        name,
			Type:        nbType,
			LatestEvent: rowLatestEvent,
		})
		if name == startReq.Name {
			latestEvent = rowLatestEvent
			gpuType = rowGpuType
			gpuRequest = rowGpuRequest
			gpuLimit = rowGpuLimit
			foundNotebook = true
		}
	}
	if err := rows.Err(); err != nil {
		logger.Error("error iterating notebook rows", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !foundNotebook {
		logger.Error("failed to find notebook", "name", startReq.Name)
		sendError(w, logger, http.StatusNotFound, "Notebook not found")
		return
	}

	if latestEvent != constants.StatusNotebookApplied {
		logger.Error("cannot start notebook that is not in applied state", "currentState", latestEvent)
		sendError(w, logger, http.StatusBadRequest, "Cannot start notebook that is not in applied state")
		return
	}

	isGPUResource := utils.CheckGPUResource(gpuType, gpuRequest, gpuLimit)

	runningCPU, runningGPU, err := app.CountEffectiveRunningNotebooks(ctx, dbNotebooks, namespace)
	if err != nil {
		logger.Error("failed to check running notebooks from k8s", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to check running notebooks")
		return
	}
	if isGPUResource {
		if runningGPU >= app.env.NotebookConfig.MaxRunningGPU {
			sendError(w, logger, http.StatusBadRequest, "Cannot start notebook: running GPU notebook limit exceeded.")
			return
		}
	} else {
		if runningCPU >= app.env.NotebookConfig.MaxRunningCPU {
			sendError(w, logger, http.StatusBadRequest, "Cannot start notebook: running CPU notebook limit exceeded.")
			return
		}
	}

	if isGPUResource {
		if !canCreateGpuNotebook {
			logger.Warn("user doesn't have permission to start GPU notebooks",
				"notebook_name", startReq.Name,
				"can_create_gpu_notebook", canCreateGpuNotebook)
			sendError(w, logger, http.StatusForbidden, "You don't have enough credit for this operation")
			return
		}
		logger.Debug("user has permission to start GPU notebooks",
			"notebook_name", startReq.Name,
			"can_create_gpu_notebook", canCreateGpuNotebook)
	}

	err = app.removeStoppedAnnotationFromNotebook(ctx, namespace, startReq.Name)
	if err != nil {
		logger.Error("failed to remove stopped annotation from notebook", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to start notebook")
		return
	}

	err = tx.Commit(ctx)
	if err != nil {
		logger.Error("failed to commit transaction", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	sendResponse(w, logger, http.StatusOK, "Notebook started successfully")
}

// deleteNotebook godoc
// @Summary      Delete notebook
// @Description  Deletes a notebook by name
// @Tags         notebook
// @Accept       json
// @Produce      json
// @Param        notebook_name  body  DeleteNotebookRequest  true  "Notebook Delete Request"
// @Success      200  {object}  SwaggerMessageResponse
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      422  {object}  Error422
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/delete [delete]
func (app *application) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	namespace := userInfo.Sub
	deleteReq, err := utils.DecodeAndValidate[DeleteNotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("method", "deleteNotebook", "namespace", namespace, "name", deleteReq.Name)
	query := `
		SELECT events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	var latestEvent constants.Events
	ctx := r.Context()
	err = app.pgPool.Pool.QueryRow(ctx, query, deleteReq.Name, namespace).Scan(&latestEvent)
	if err != nil {
		if err == pgx.ErrNoRows {
			logger.Error("notebook not found", "error", err)
			sendError(w, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		logger.Error("failed to select notebook", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to delete notebook")
		return
	}

	notebookFailed := checkNotebookFailed(latestEvent)

	if latestEvent != constants.StatusNotebookApplied && !notebookFailed {
		sendError(w, logger, http.StatusBadRequest, "Cannot delete notebook that is not in applied state")
		return
	}

	deleteQuery := `DELETE FROM notebooks WHERE name = $1 AND namespace = $2`
	_, err = app.pgPool.Pool.Exec(ctx, deleteQuery, deleteReq.Name, namespace)
	if err != nil {
		sendError(w, logger, http.StatusInternalServerError, "Failed to delete notebook")
		return
	}

	if !notebookFailed {
		err = app.deleteNotebookFromK8s(ctx, logger, namespace, deleteReq.Name)
		notebookAlreadyDeleted := false
		if errors.IsNotFound(err) {
			logger.Warn("notebook not found in Kubernetes", "error", err)
			notebookAlreadyDeleted = true
		}
		if err != nil && !notebookAlreadyDeleted {
			sendError(w, logger, http.StatusInternalServerError, "Failed to delete notebook")
			return
		}
		err = app.deletePVCFromK8s(ctx, logger, namespace, deleteReq.Name+"-pvc")
		if errors.IsNotFound(err) {
			if notebookAlreadyDeleted {
				logger.Warn("notebook already deleted from k8s", "error", err)
			}
		}
		if err != nil {
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	sendResponse(w, logger, http.StatusOK, "Notebook deleted successfully")
}

// listNotebooks godoc
// @Summary      List notebooks
// @Description  Lists all notebooks for the user
// @Tags         notebook
// @Produce      json
// @Param        limit  query    int     false  "Maximum number of notebooks to return (max 50)"
// @Param        offset query    int     false  "Offset for pagination"
// @Param        order  query    string  false  "Sort order (asc or desc)"
// @Param        filter query    string  false  "Date filter as JSON array of two date strings [start, end]"
// @Success      200  {object}  NotebookListResponse
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      400  {object}  Error400
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/list [get]
func (app *application) listNotebooks(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	logger = logger.With("method", "listNotebooks")

	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	ctx := r.Context()
	namespace := userInfo.Sub

	// Extract all query parameters for logging
	filterVal := r.URL.Query().Get("filter")
	orderVal := r.URL.Query().Get("order")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	// Log all request parameters and filters in one place
	logger.Info("listNotebooks request initiated",
		"namespace", namespace,
		"filter_raw", filterVal,
		"order", orderVal,
		"limit_raw", limitStr,
		"offset_raw", offsetStr,
		"query_params", r.URL.RawQuery,
	)

	var filterDates [2]string
	var filterActive bool
	var orderDesc bool
	var err error

	if orderVal != "" {
		if orderVal == "desc" {
			orderDesc = true
		} else if orderVal != "asc" {
			logger.Error("invalid order parameter", "order", orderVal)
			sendError(w, logger, http.StatusBadRequest, "order parameter must be 'asc' or 'desc'")
			return
		}
	}

	if filterVal != "" {
		var dates []string
		if err := json.Unmarshal([]byte(filterVal), &dates); err != nil {
			logger.Error("failed to parse filter as JSON array", "error", err, "filter", filterVal)
			sendError(w, logger, http.StatusBadRequest, "filter must be a valid JSON array of two date strings")
			return
		}

		if len(dates) != 2 {
			sendError(w, logger, http.StatusBadRequest, "filter must contain exactly two date strings")
			return
		}

		for i, val := range dates {
			if _, err := time.Parse("2006-01-02", val); err != nil {
				sendError(w, logger, http.StatusBadRequest, "filter values must be valid date strings (YYYY-MM-DD)")
				return
			}
			filterDates[i] = val
		}
		filterActive = true
	}

	limit := app.env.NotebookConfig.DefaultNotebookListLimit
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > 50 {
				limit = 50
			} else {
				limit = l
			}
		}
	}
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Log parsed and processed parameters
	logger.Debug("listNotebooks parameters processed",
		"filter_active", filterActive,
		"filter_start_date", func() string {
			if filterActive {
				return filterDates[0]
			}
			return ""
		}(),
		"filter_end_date", func() string {
			if filterActive {
				return filterDates[1]
			}
			return ""
		}(),
		"order_desc", orderDesc,
		"limit", limit,
		"offset", offset,
	)

	var query string
	var rows pgx.Rows
	var orderClause string

	if orderDesc {
		orderClause = "ORDER BY created_at DESC"
	} else {
		orderClause = "ORDER BY created_at ASC"
	}

	if filterActive {
		query = fmt.Sprintf(`
			SELECT id, name, namespace, storage_size, pvc_name,
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_request, gpu_limit, template_name, events, created_at
			FROM notebooks
			WHERE namespace = $1 AND created_at >= $2 AND created_at <= $3
			%s
			LIMIT $4 OFFSET $5`, orderClause)
		logger.Debug("executing filtered query",
			"query_type", "filtered",
			"start_date", filterDates[0],
			"end_date", filterDates[1],
			"limit", limit,
			"offset", offset)
		rows, err = app.pgPool.Pool.Query(ctx, query, namespace, filterDates[0], filterDates[1], limit, offset)
	} else {
		query = fmt.Sprintf(`
			SELECT id, name, namespace, storage_size, pvc_name,
				cpu_request, cpu_limit, memory_request, memory_limit,
				gpu_type, gpu_request, gpu_limit, template_name, events, created_at
			FROM notebooks
			WHERE namespace = $1
			%s
			LIMIT $2 OFFSET $3`, orderClause)
		logger.Debug("executing unfiltered query",
			"query_type", "unfiltered",
			"limit", limit,
			"offset", offset)
		rows, err = app.pgPool.Pool.Query(ctx, query, namespace, limit, offset)
	}
	if err != nil {
		logger.Error("failed to query notebooks", "error", err, "query_type", func() string {
			if filterActive {
				return "filtered"
			}
			return "unfiltered"
		}())
		sendError(w, logger, http.StatusInternalServerError, "Failed to query notebooks")
		return
	}
	defer rows.Close()

	notebooks := []NotebookStatus{}
	for rows.Next() {
		var nb NotebookStatus
		scanErr := rows.Scan(
			&nb.ID, &nb.Name, &nb.Namespace, &nb.StorageSize, &nb.PVCName,
			&nb.CPURequest, &nb.CPULimit, &nb.MemoryRequest, &nb.MemoryLimit,
			&nb.GPUType, &nb.GPURequest, &nb.GPULimit, &nb.TemplateName, &nb.Events, &nb.CreatedAt,
		)
		if scanErr != nil {
			logger.Error("failed to scan notebook row", "error", scanErr)
			continue
		}
		notebooks = append(notebooks, nb)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		logger.Error("error iterating notebook rows", "error", rowsErr)
		sendError(w, logger, http.StatusInternalServerError, "Failed to fetch notebooks")
		return
	}
	type k8sResult struct {
		index     int
		k8sObject map[string]any
		err       error
	}

	// Create cancellable context for goroutines
	k8sCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultChan := make(chan k8sResult, len(notebooks))
	semaphore := make(chan struct{}, 3) // Limit concurrency to 3
	activeRequests := 0

	// Collect notebooks that need K8s status check
	var notebooksToCheck []struct {
		index int
		name  string
	}

	for i, nb := range notebooks {
		var latestEvent constants.Events
		if len(nb.Events) > 0 {
			latestEvent = nb.Events[len(nb.Events)-1]
		}

		notebookFailed := checkNotebookFailed(latestEvent)

		if !notebookFailed && latestEvent == constants.StatusNotebookApplied {
			notebooksToCheck = append(notebooksToCheck, struct {
				index int
				name  string
			}{index: i, name: nb.Name})
		}
	}

	activeRequests = len(notebooksToCheck)

	for _, nbToCheck := range notebooksToCheck {
		go func(index int, name string) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-k8sCtx.Done():
				return
			}

			select {
			case <-k8sCtx.Done():
				return
			default:
			}

			k8sSpec, k8sErr := app.getNotebookJSON(k8sCtx, logger, namespace, name)
			var k8sObject map[string]any = nil
			if k8sErr == nil && k8sSpec != nil {
				k8sObject = k8sSpec.Object
			}

			select {
			case resultChan <- k8sResult{index: index, k8sObject: k8sObject, err: k8sErr}:
			case <-k8sCtx.Done():
				return
			}
		}(nbToCheck.index, nbToCheck.name)
	}

	k8sResults := make(map[int]map[string]any)
	for i := 0; i < activeRequests; i++ {
		select {
		case result := <-resultChan:
			if result.err != nil {
				if errors.IsNotFound(result.err) {
					logger.Warn("notebook not found in Kubernetes, treating as orphaned", "error", result.err)
					k8sResults[result.index] = nil
				} else {
					logger.Error("failed to get notebook from Kubernetes, returning error", "error", result.err)
					cancel()
					sendError(w, logger, http.StatusInternalServerError, "Internal server error")
					return
				}
			} else {
				k8sResults[result.index] = result.k8sObject
			}
		case <-ctx.Done():
			logger.Error("context timeout while fetching notebook status")
			cancel()
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	for i, nb := range notebooks {
		var latestEvent constants.Events
		if len(nb.Events) > 0 {
			latestEvent = nb.Events[len(nb.Events)-1]
		}

		var k8sObject map[string]any = nil
		if result, exists := k8sResults[i]; exists {
			k8sObject = result
		}

		nb.Status = determineNotebookState(latestEvent, k8sObject)
		if nb.Status == NotebookStateRunning {
			nb.URL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, nb.Namespace, nb.Name)
		}
		notebooks[i] = nb
	}

	nextOffset := -1
	if len(notebooks) == limit {
		nextOffset = offset + limit
	}

	logger.Debug("listNotebooks request completed",
		"notebooks_count", len(notebooks),
		"has_next_page", nextOffset != -1,
		"next_offset", nextOffset,
		"k8s_requests_made", activeRequests,
		"response_status", http.StatusOK,
	)

	var resp NotebookListResponse = NotebookListResponse{
		Notebooks:  notebooks,
		NextOffset: nextOffset,
	}
	sendResponseJson(w, logger, http.StatusOK, resp)
}

// checkNotebookStatus godoc
// @Summary      Get notebook status
// @Description  Gets the status of a notebook
// @Tags         notebook
// @Produce      json
// @Param        notebook_name  path  string  true  "Notebook Name"
// @Success      200  {object}  NotebookStatus
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/status/{notebook_name} [get]
func (app *application) checkNotebookStatus(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}

	namespace := userInfo.Sub
	notebookName := r.PathValue("notebook_name")
	if notebookName == "" {
		logger.Error("notebook name is required", "error", "notebook name is required")
		sendError(w, logger, http.StatusBadRequest, "Notebook name is required")
		return
	}

	logger = logger.With("method", "checkNotebookStatus", "namespace", namespace, "name", notebookName)
	ctx := r.Context()
	var status NotebookStatus
	query := `
		SELECT id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit,
			gpu_type, gpu_request, gpu_limit, template_name, events, created_at
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
		&status.GPURequest,
		&status.GPULimit,
		&status.TemplateName,
		&status.Events,
		&status.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendResponse(w, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		sendResponse(w, logger, http.StatusInternalServerError, "Failed to fetch notebook")
		return
	}

	var latestEvent constants.Events
	if len(status.Events) > 0 {
		latestEvent = status.Events[len(status.Events)-1]
	}

	var k8sSpec *unstructured.Unstructured
	if latestEvent == constants.StatusNotebookApplied {
		var err error
		k8sSpec, err = app.getNotebookJSON(ctx, logger, status.Namespace, status.Name)
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

	sendResponseJson(w, logger, http.StatusOK, status)
}

// createProfile godoc
// @Summary      Create Kubeflow profile
// @Description  Creates a Kubeflow Profile CRD for the user
// @Tags         profile
// @Accept       json
// @Produce      json
// @Success      201  {object}  SwaggerMessageResponse
// @Failure      429  {object}  Error429
// @Failure      401  {object}  Error401
// @Failure      422  {object}  Error422
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/profile/create [post]
func (app *application) createProfile(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)

	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		logger.Error("user info not found in context")
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	email := userInfo.Email
	userId := userInfo.Sub

	logger = logger.With("userId", userId, "email", email, "operation", "createProfile")
	logger.Info("creating kubeflow profile")

	err := app.createKubeflowProfile(r.Context(), logger, userId, email)
	if err != nil && !errors.IsAlreadyExists(err) {
		logger.Warn("failed to create kubeflow profile", "error", err, "profileName", userId)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create Kubeflow Profile")
		return
	}

	query := `
			INSERT INTO profiles (user_id,email)
			VALUES ($1,$2)
			ON CONFLICT (user_id) DO NOTHING
			RETURNING id
		`
	var profileId int64
	err = WithDBRetry(r.Context(), logger, func() (constants.ShouldContinue, error) {
		return constants.RetryContinue, app.pgPool.Pool.QueryRow(r.Context(), query, userId, email).Scan(&profileId)
	})
	if err != nil && err != pgx.ErrNoRows {
		logger.Error("failed to create profile in database", "error", err)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create profile in database")
		return
	}
	sendResponse(w, logger, http.StatusCreated, "The profile has been created successfully or already exists.")
}
