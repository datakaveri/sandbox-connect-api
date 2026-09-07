package main

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sandbox-backend-service/internal/evaluation"
)

type EvaluationConfig struct {
	Enabled            bool   `env:"API_EVALUATIONS_ENABLED" envDefault:"false"`
	SourceNotebookPath string `env:"API_EVALUATION_NOTEBOOK_PATH" envDefault:"nha_ps4_evaluation_template.ipynb"`
	WorkspaceMountPath string `env:"API_EVALUATION_WORKSPACE_MOUNT_PATH" envDefault:"/home/jovyan/evaluation-workspace"`
	ApproverRoles      string `env:"API_EVALUATION_APPROVER_ROLES" envDefault:""`
	MaxManifestBytes   int    `env:"API_EVALUATION_MAX_MANIFEST_BYTES" envDefault:"262144"`
	MaxManifestFiles   int    `env:"API_EVALUATION_MAX_MANIFEST_FILES" envDefault:"1000"`
}

type evaluationStore interface {
	Create(context.Context, evaluation.CreateParams) (evaluation.Record, bool, error)
	MarkNotebookStopped(context.Context, string) error
	MarkStopFailed(context.Context, string, string, string) error
	GetOwned(context.Context, string, string) (evaluation.Record, error)
	HasActiveHold(context.Context, int64) (bool, error)
	RequestApproval(context.Context, evaluation.ApprovalParams) (evaluation.Approval, bool, error)
}

type EvaluationResponse struct {
	EvaluationID string `json:"evaluationId"`
	NotebookName string `json:"notebookName"`
	Status       string `json:"status"`
}

type EvaluationOutput struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
}

type EvaluationOutputsResponse struct {
	EvaluationID string             `json:"evaluationId"`
	Status       string             `json:"status"`
	Outputs      []EvaluationOutput `json:"outputs"`
	ErrorCode    *string            `json:"errorCode,omitempty"`
	ErrorMessage *string            `json:"errorMessage,omitempty"`
}

type ApprovalResponse struct {
	EvaluationID string `json:"evaluationId"`
	Status       string `json:"status"`
	Destination  string `json:"destination"`
}

func validateIdempotencyKey(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		return "", false
	}
	for _, char := range key {
		if char > unicode.MaxASCII || unicode.IsControl(char) || unicode.IsSpace(char) {
			return "", false
		}
	}
	return key, true
}

func (app *application) evaluationFeatureReady(w http.ResponseWriter, r *http.Request) bool {
	if !app.env.EvaluationConfig.Enabled || app.evaluationStore == nil {
		sendError(w, getLogger(r), http.StatusNotFound, "Not found")
		return false
	}
	return true
}

// submitEvaluation godoc
// @Summary      Submit a notebook for evaluation
// @Description  Stops an owned, ready notebook and queues an asynchronous PS4 evaluation. In booking mode the linked booking must be active.
// @Tags         evaluations
// @Produce      json
// @Param        notebook_name   path    string  true  "Notebook Name"
// @Param        Idempotency-Key header  string  true  "Unique request key"
// @Success      202  {object}  EvaluationResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      409  {object}  Error409
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebooks/{notebook_name}/evaluations [post]
func (app *application) submitEvaluation(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.evaluationFeatureReady(w, r) {
		return
	}
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	idempotencyKey, ok := validateIdempotencyKey(r)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "A valid Idempotency-Key header is required")
		return
	}
	notebookName := r.PathValue("notebook_name")
	if errorMessage, invalid := getErrorMessageForNotebookName(notebookName); invalid {
		sendError(w, logger, http.StatusBadRequest, errorMessage)
		return
	}
	if !evaluation.IsSafeRelativePath(app.env.EvaluationConfig.SourceNotebookPath) {
		logger.Error("evaluation source notebook path is invalid")
		sendError(w, logger, http.StatusInternalServerError, "Evaluation configuration is invalid")
		return
	}

	record, _, err := app.evaluationStore.Create(r.Context(), evaluation.CreateParams{
		UserID: user.Sub, NotebookName: notebookName,
		SourceNotebookPath:   app.env.EvaluationConfig.SourceNotebookPath,
		IdempotencyKey:       idempotencyKey,
		RequireActiveBooking: app.env.BookingsEnabled,
	})
	if err != nil {
		switch {
		case errors.Is(err, evaluation.ErrNotebookNotFound):
			sendError(w, logger, http.StatusNotFound, "Notebook not found")
		case errors.Is(err, evaluation.ErrNotebookNotReady):
			sendError(w, logger, http.StatusConflict, "Notebook is not ready for evaluation")
		case errors.Is(err, evaluation.ErrActiveEvaluation):
			sendError(w, logger, http.StatusConflict, "An evaluation is already active for this notebook")
		default:
			logger.Error("failed to create evaluation", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to submit evaluation")
		}
		return
	}

	if record.Status == evaluation.StatusStopping {
		if err := app.addStoppedAnnotationToNotebook(r.Context(), record.Namespace, record.NotebookName); err != nil {
			logger.Error("failed to stop notebook for evaluation", "error", err, "evaluation_id", record.ID)
			if markErr := app.evaluationStore.MarkStopFailed(r.Context(), record.ID,
				"notebook_stop_failed", "The notebook could not be stopped for evaluation"); markErr != nil {
				logger.Error("failed to release evaluation hold after stop failure", "error", markErr, "evaluation_id", record.ID)
			}
			sendError(w, logger, http.StatusInternalServerError, "Failed to stop notebook for evaluation")
			return
		}
		if err := app.evaluationStore.MarkNotebookStopped(r.Context(), record.ID); err != nil {
			logger.Error("failed to queue stopped notebook for evaluation", "error", err, "evaluation_id", record.ID)
			sendError(w, logger, http.StatusInternalServerError, "Failed to queue evaluation")
			return
		}
		record.Status = evaluation.StatusWaitingForPVC
	}

	sendResponseJson(w, logger, http.StatusAccepted, EvaluationResponse{
		EvaluationID: record.ID, NotebookName: record.NotebookName, Status: string(record.Status),
	})
}

// listEvaluationOutputs godoc
// @Summary      List evaluation outputs
// @Description  Returns the current evaluation status and, after success, its bounded verified output manifest.
// @Tags         evaluations
// @Produce      json
// @Param        evaluation_id  path  string  true  "Evaluation ID"
// @Success      200  {object}  EvaluationOutputsResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/evaluations/{evaluation_id}/outputs [get]
func (app *application) listEvaluationOutputs(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.evaluationFeatureReady(w, r) {
		return
	}
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	evaluationID := r.PathValue("evaluation_id")
	if _, err := uuid.Parse(evaluationID); err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid evaluation id")
		return
	}
	record, err := app.evaluationStore.GetOwned(r.Context(), evaluationID, user.Sub)
	if err != nil {
		if errors.Is(err, evaluation.ErrEvaluationNotFound) {
			sendError(w, logger, http.StatusNotFound, "Evaluation not found")
		} else {
			logger.Error("failed to load evaluation", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to load evaluation")
		}
		return
	}

	response := EvaluationOutputsResponse{EvaluationID: record.ID, Status: string(record.Status), Outputs: []EvaluationOutput{},
		ErrorCode: record.SafeErrorCode, ErrorMessage: record.SafeErrorMessage}
	if record.Status == evaluation.StatusSucceeded && len(record.OutputManifest) > 0 {
		manifest, err := evaluation.ParseManifest(record.OutputManifest,
			app.env.EvaluationConfig.MaxManifestBytes,
			app.env.EvaluationConfig.MaxManifestFiles)
		if err != nil {
			logger.Error("stored evaluation manifest is invalid", "error", err, "evaluation_id", record.ID)
			sendError(w, logger, http.StatusInternalServerError, "Evaluation output manifest is unavailable")
			return
		}
		response.Outputs = make([]EvaluationOutput, len(manifest.Files))
		for index, file := range manifest.Files {
			response.Outputs[index] = EvaluationOutput{
				Path: file.Path, Size: file.Size, MediaType: file.MediaType, SHA256: file.SHA256,
			}
		}
	}
	sendResponseJson(w, logger, http.StatusOK, response)
}

// approveEvaluation godoc
// @Summary      Approve evaluation outputs
// @Description  Queues an asynchronous verified copy into the owner's shared workspace. Requires the configured approver role and an enabled workspace.
// @Tags         evaluations
// @Produce      json
// @Param        evaluation_id  path    string  true  "Evaluation ID"
// @Param        Idempotency-Key header  string  true  "Unique request key"
// @Success      202  {object}  ApprovalResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      403  {object}  Error403
// @Failure      404  {object}  Error404
// @Failure      409  {object}  Error409
// @Failure      500  {object}  Error500
// @Failure      503  {object}  Error503
// @Security     BearerAuth
// @Router       /v1/evaluations/{evaluation_id}/approve [post]
func (app *application) approveEvaluation(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.evaluationFeatureReady(w, r) {
		return
	}
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !hasConfiguredApproverRole(user.Roles, app.env.EvaluationConfig.ApproverRoles) {
		sendError(w, logger, http.StatusForbidden, "You are not permitted to approve evaluation outputs")
		return
	}
	if !app.env.EvaluationWorkspaceConfig.Enabled {
		sendError(w, logger, http.StatusServiceUnavailable, "Evaluation workspace is not enabled")
		return
	}
	idempotencyKey, ok := validateIdempotencyKey(r)
	if !ok {
		sendError(w, logger, http.StatusBadRequest, "A valid Idempotency-Key header is required")
		return
	}
	evaluationID := r.PathValue("evaluation_id")
	if _, err := uuid.Parse(evaluationID); err != nil {
		sendError(w, logger, http.StatusBadRequest, "Invalid evaluation id")
		return
	}
	record, err := app.evaluationStore.GetOwned(r.Context(), evaluationID, user.Sub)
	if err != nil {
		if errors.Is(err, evaluation.ErrEvaluationNotFound) {
			sendError(w, logger, http.StatusNotFound, "Evaluation not found")
		} else {
			sendError(w, logger, http.StatusInternalServerError, "Failed to load evaluation")
		}
		return
	}
	if !path.IsAbs(app.env.EvaluationConfig.WorkspaceMountPath) {
		logger.Error("evaluation workspace mount path is invalid")
		sendError(w, logger, http.StatusInternalServerError, "Evaluation configuration is invalid")
		return
	}
	if err := app.ensureEvaluationWorkspacePVC(r.Context(), logger, record.Namespace); err != nil {
		logger.Error("evaluation workspace is unavailable", "error", err, "evaluation_id", record.ID)
		sendError(w, logger, http.StatusServiceUnavailable, "Evaluation workspace is unavailable")
		return
	}
	destination := path.Join(app.env.EvaluationConfig.WorkspaceMountPath,
		"evaluations", record.NotebookName, record.ID)
	approval, _, err := app.evaluationStore.RequestApproval(r.Context(), evaluation.ApprovalParams{
		EvaluationID: record.ID, RequestedBy: user.Sub,
		IdempotencyKey: idempotencyKey, Destination: destination,
	})
	if err != nil {
		switch {
		case errors.Is(err, evaluation.ErrEvaluationNotFound):
			sendError(w, logger, http.StatusNotFound, "Evaluation not found")
		case errors.Is(err, evaluation.ErrEvaluationNotReady):
			sendError(w, logger, http.StatusConflict, "Evaluation output is not ready for approval")
		default:
			logger.Error("failed to request evaluation approval", "error", err, "evaluation_id", record.ID)
			sendError(w, logger, http.StatusInternalServerError, "Failed to request approval")
		}
		return
	}
	sendResponseJson(w, logger, http.StatusAccepted, ApprovalResponse{
		EvaluationID: approval.EvaluationID, Status: string(approval.Status), Destination: approval.Destination,
	})
}

func hasConfiguredApproverRole(userRoles []string, configured string) bool {
	required := strings.Split(configured, ",")
	if strings.TrimSpace(configured) == "" {
		return true
	}
	for _, role := range required {
		for _, userRole := range userRoles {
			if strings.EqualFold(strings.TrimSpace(role), strings.TrimSpace(userRole)) {
				return true
			}
		}
	}
	return false
}

func (app *application) hasEvaluationHold(ctx context.Context, notebookID int64) (bool, error) {
	if !app.env.EvaluationConfig.Enabled || app.evaluationStore == nil {
		return false, nil
	}
	return app.evaluationStore.HasActiveHold(ctx, notebookID)
}

func (app *application) hasEvaluationHoldInTx(ctx context.Context, tx pgx.Tx, notebookID int64) (bool, error) {
	if !app.env.EvaluationConfig.Enabled || app.evaluationStore == nil {
		return false, nil
	}
	var held bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM evaluations WHERE notebook_id=$1 AND hold_active=true)`,
		notebookID).Scan(&held)
	return held, err
}
