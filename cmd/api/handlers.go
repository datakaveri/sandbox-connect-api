package main

import (
	"context"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"

	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (app *application) checkNotebookExists(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	notebookName := r.PathValue("notebook_name")
	if notebookName == "" {
		logger.Error("notebook name is empty", "notebookName", notebookName)
		sendResponse(w, r, logger, http.StatusBadRequest, "Notebook name is empty")
		return
	}
	ctx := context.Background()
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM notebooks WHERE name = $1 AND namespace = $2)`
	err := app.pgPool.Pool.QueryRow(ctx, query, notebookName, app.env.NotebookConfig.Namespace).Scan(&exists)
	if err != nil {
		logger.Error("failed to check notebook existence", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to check notebook existence")
		return
	}
	sendResponseJson(w, r, logger, http.StatusOK, map[string]bool{"exists": exists})
}

func (app *application) createNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	notebookReq, err := utils.DecodeAndValidate[NotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	if notebookReq.Type != "cpu" && notebookReq.Type != "gpu" {
		logger.Error("invalid notebook type", "type", notebookReq.Type)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Notebook Type")
		return
	}

	baseArgs := []any{
		app.env.NotebookConfig.UserID,
		notebookReq.Name,
		app.env.NotebookConfig.Namespace,
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
	ctx := context.Background()
	var notebookId int64
	err = app.pgPool.Pool.QueryRow(ctx, query, baseArgs...).Scan(&notebookId)
	if err != nil {
		logger.Error("failed to create notebook in database", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "internal server error")
		return
	}
	logger.Info("notebook created successfully", "notebookId", notebookId)
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
	logger = logger.With("method", "stopNotebook", "namespace", app.env.NotebookConfig.Namespace, "name", stopReq.Name)

	ctx := context.Background()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, stopReq.Name, app.env.NotebookConfig.Namespace).Scan(&notebookID, &latestEvent)
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

	err = addStoppedAnnotationToNotebook(app.k8sClient, app.env.NotebookConfig.Namespace, stopReq.Name)
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
	logger = logger.With("method", "startNotebook", "namespace", app.env.NotebookConfig.Namespace, "name", startReq.Name)
	ctx := context.Background()
	var notebookID int64
	var latestEvent string
	query := `
		SELECT id, events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	err = app.pgPool.Pool.QueryRow(ctx, query, startReq.Name, app.env.NotebookConfig.Namespace).Scan(&notebookID, &latestEvent)
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

	err = removeStoppedAnnotationFromNotebook(app.k8sClient, app.env.NotebookConfig.Namespace, startReq.Name)
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
	logger = logger.With("method", "deleteNotebook", "namespace", app.env.NotebookConfig.Namespace, "name", deleteReq.Name)
	query := `
		SELECT events[array_upper(events, 1)] as latest_event
		FROM notebooks
		WHERE name = $1 AND namespace = $2
	`
	var latestEvent string
	ctx := context.Background()
	err = app.pgPool.Pool.QueryRow(ctx, query, deleteReq.Name, app.env.NotebookConfig.Namespace).Scan(&latestEvent)
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

	notebookFailed := latestEvent == string(constants.StatusPVCUploadFailed) ||
		latestEvent == string(constants.StatusPVCUploadApplyFailed) ||
		latestEvent == string(constants.StatusNotebookApplyFailed) ||
		latestEvent == string(constants.StatusPVCCreationFailed) ||
		latestEvent == string(constants.StatusPVCApplyFailed)

	if latestEvent != string(constants.StatusNotebookApplied) && !notebookFailed {
		logger.Error("cannot delete notebook that is not in applied state", "currentState", latestEvent)
		sendResponse(w, r, logger, http.StatusBadRequest, "Cannot delete notebook that is not in applied state")
		return
	}

	deleteQuery := `DELETE FROM notebooks WHERE name = $1 AND namespace = $2`
	_, err = app.pgPool.Pool.Exec(ctx, deleteQuery, deleteReq.Name, app.env.NotebookConfig.Namespace)
	if err != nil {
		logger.Error("failed to delete notebook from database", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from database")
		return
	}

	if !notebookFailed {
		err = deleteNotebookFromK8s(app.k8sClient, app.env.NotebookConfig.Namespace, deleteReq.Name)
		if err != nil {
			logger.Error("failed to delete notebook from Kubernetes", "error", err)
			sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to delete notebook from Kubernetes")
			return
		}
		err = deletePVCFromK8s(app.k8sClient, app.env.NotebookConfig.Namespace, deleteReq.Name+"-pvc")
		if err != nil {
			logger.Error("failed to delete PVC from Kubernetes", "error", err)
			sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to delete PVC from Kubernetes")
			return
		}
	}
	sendResponse(w, r, logger, http.StatusOK, "Notebook deleted successfully")
}

func (app *application) listNotebooks(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	logger = logger.With("method", "listNotebooks")
	ctx := context.Background()
	k8sNotebooks, err := getNotebooksJSON(app.k8sClient, app.env.NotebookConfig.Namespace)
	if err != nil {
		logger.Warn("failed to get notebooks from Kubernetes", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	query := `
		SELECT id, name, namespace, storage_size, pvc_name, 
		       cpu_request, cpu_limit, memory_request, memory_limit, 
		       gpu_type, gpu_count, template_name, 
		       events
		FROM notebooks
		WHERE namespace = $1
	`
	rows, err := app.pgPool.Pool.Query(ctx, query, app.env.NotebookConfig.Namespace)
	if err != nil {
		logger.Error("failed to query notebooks", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to query notebooks")
		return
	}
	defer rows.Close()

	response := ListNotebooksResponse{
		Successful: []NotebookDetails{},
		Stopped:    []NotebookDetails{},
		Pending:    []NotebookDetails{},
		Failed:     []NotebookDetails{},
		Orphaned:   []NotebookDetails{},
	}

	for rows.Next() {
		var notebook NotebookDetails
		err := rows.Scan(
			&notebook.ID, &notebook.Name, &notebook.Namespace, &notebook.StorageSize,
			&notebook.PVCName, &notebook.CPURequest, &notebook.CPULimit,
			&notebook.MemoryRequest, &notebook.MemoryLimit, &notebook.GPUType,
			&notebook.GPUCount, &notebook.TemplateName, &notebook.Events,
		)
		if err != nil {
			logger.Error("failed to scan notebook row", "error", err)
			continue
		}

		latestEvent := ""
		if len(notebook.Events) > 0 {
			latestEvent = notebook.Events[len(notebook.Events)-1]
		}

		var k8sSpec any
		if latestEvent == string(constants.StatusNotebookApplied) {
			k8sSpec = k8sNotebooks[notebook.Name]
		}
		state := determineNotebookState(latestEvent, k8sSpec, logger)

		switch state {
		case NotebookStateRunning:
			notebook.URL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, notebook.Namespace, notebook.Name)
			response.Successful = append(response.Successful, notebook)
		case NotebookStateStopped:
			response.Stopped = append(response.Stopped, notebook)
		case NotebookStateFailed:
			response.Failed = append(response.Failed, notebook)
		case NotebookStateOrphaned:
			response.Orphaned = append(response.Orphaned, notebook)
		default:
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

func (app *application) checkNotebookStatus(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	notebookName := r.PathValue("notebook_name")
	if notebookName == "" {
		logger.Error("notebook name is required", "error", "notebook name is required")
		sendResponse(w, r, logger, http.StatusBadRequest, "Notebook name is required")
		return
	}
	logger = logger.With("method", "checkNotebookStatus", "namespace", app.env.NotebookConfig.Namespace, "name", notebookName)
	ctx := context.Background()
	var status NotebookStatus
	query := `
		SELECT id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit,
			gpu_type, gpu_count, template_name, events
		FROM notebooks
		WHERE namespace= $1 and name = $2`

	err := app.pgPool.Pool.QueryRow(ctx, query, app.env.NotebookConfig.Namespace, notebookName).Scan(
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

	latestEvent := ""
	if len(status.Events) > 0 {
		latestEvent = status.Events[len(status.Events)-1]
	}

	var k8sSpec *unstructured.Unstructured
	if latestEvent == string(constants.StatusNotebookApplied) {
		var err error
		k8sSpec, err = getNotebookJSON(app.k8sClient, status.Namespace, status.Name)
		if err != nil {
			logger.Error("failed to get notebook from Kubernetes", "error", err)
			k8sSpec = nil
		}
	}

	var k8sObject map[string]interface{}
	if k8sSpec != nil {
		k8sObject = k8sSpec.Object
	}
	status.Status = string(determineNotebookState(latestEvent, k8sObject, logger))

	if status.Status == string(NotebookStateRunning) {
		status.URL = generateNotebookURL(app.env.NotebookConfig.KubeFlowURL, status.Namespace, status.Name)
	}

	sendResponseJson(w, r, logger, http.StatusOK, status)
}

func (app *application) createProfile(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)

	profileReq, err := utils.DecodeAndValidate[CreateProfileRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body for profile creation", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	logger = logger.With("userId", profileReq.UserID, "email", profileReq.Email)

	profile := unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kubeflow.org/v1",
			"kind":       "Profile",
			"metadata": map[string]any{
				"name": profileReq.UserID,
			},
			"spec": map[string]any{
				"owner": map[string]any{
					"kind": "User",
					"name": profileReq.Email,
				},
				"plugins":           []any{},
				"resourceQuotaSpec": map[string]any{},
			},
		},
	}

	unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&profile)
	if err != nil {
		logger.Error("failed to convert profile to unstructured", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Internal server error during profile conversion")
		return
	}

	unstructuredProfile := &unstructured.Unstructured{Object: unstructuredMap}

	profileGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}

	ctx := context.Background()

	_, err = app.k8sClient.Dynamic.Resource(profileGVR).Create(ctx, unstructuredProfile, metav1.CreateOptions{})
	if err != nil {
		logger.Error("failed to create kubeflow profile", "error", err, "profileName", profileReq.UserID)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to create Kubeflow Profile")
		return
	}

	logger.Info("kubeflow profile created successfully", "profileName", profileReq.UserID)
	sendResponse(w, r, logger, http.StatusCreated, "Kubeflow Profile created successfully")
}
