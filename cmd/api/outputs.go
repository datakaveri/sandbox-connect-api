package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
)

type OutputConfig struct {
	Enabled            bool   `env:"API_OUTPUTS_ENABLED" envDefault:"false"`
	SourceNotebookPath string `env:"API_OUTPUT_NOTEBOOK_PATH" envDefault:"nha_ps4_output_template.ipynb"`
	ApproverRoles      string `env:"API_OUTPUT_ADMIN_ROLES" envDefault:""`
	MaxManifestBytes   int    `env:"API_OUTPUT_MAX_MANIFEST_BYTES" envDefault:"262144"`
	MaxManifestFiles   int    `env:"API_OUTPUT_MAX_MANIFEST_FILES" envDefault:"1000"`
	MaxFileBytes       int64  `env:"API_OUTPUT_MAX_FILE_BYTES" envDefault:"268435456"`
	MaxOutputBytes     int64  `env:"API_OUTPUT_MAX_BYTES" envDefault:"1073741824"`
}

type FilesConnectConfig struct {
	BaseURL             string `env:"API_FILES_CONNECT_BASE_URL" envDefault:""`
	ServiceToken        string `env:"API_FILES_CONNECT_SERVICE_TOKEN" envDefault:""`
	ReviewDatabankID    string `env:"API_FILES_CONNECT_REVIEW_DATABANK_ID" envDefault:""`
	WorkspaceDatabankID string `env:"API_FILES_CONNECT_WORKSPACE_DATABANK_ID" envDefault:""`
	TimeoutSeconds      int    `env:"API_FILES_CONNECT_TIMEOUT_SECONDS" envDefault:"30"`
	MaxResponseBytes    int64  `env:"API_FILES_CONNECT_MAX_RESPONSE_BYTES" envDefault:"4194304"`
}

type outputStore interface {
	Create(context.Context, output.CreateParams) (output.Record, bool, error)
	MarkNotebookStopped(context.Context, string) error
	MarkStopFailed(context.Context, string, string, string) error
	GetOwned(context.Context, string, string) (output.Record, error)
	GetByID(context.Context, string) (output.Record, error)
	GetApproval(context.Context, string) (output.Approval, bool, error)
	ListPending(context.Context, int) ([]output.Record, error)
	HasActiveHold(context.Context, int64) (bool, error)
	RequestApproval(context.Context, output.ApprovalParams) (output.Approval, bool, error)
}

type outputFiles interface {
	ListWorkspace(context.Context, string) ([]filesconnect.File, error)
	PreviewReviewFile(context.Context, string, string) (filesconnect.Preview, error)
	PreviewWorkspaceFile(context.Context, string, string) (filesconnect.Preview, error)
	DownloadWorkspaceFile(context.Context, string, string) (filesconnect.Download, error)
}

type OutputResponse struct {
	OutputID     string  `json:"outputId"`
	NotebookName string  `json:"notebookName"`
	Status       string  `json:"status"`
	Approval     *string `json:"approvalStatus,omitempty"`
	ErrorCode    *string `json:"errorCode,omitempty"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
}

type OutputFile struct {
	FileID    string `json:"fileId"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
}

type OutputFilesResponse struct {
	OutputID string       `json:"outputId"`
	Status   string       `json:"status"`
	Files    []OutputFile `json:"files"`
}

type PendingOutputsResponse struct {
	Outputs []OutputResponse `json:"outputs"`
}

type WorkspaceFilesResponse struct {
	Files []filesconnect.File `json:"files"`
}

type ApprovalResponse struct {
	OutputID string `json:"outputId"`
	Status   string `json:"status"`
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

func (app *application) outputFeatureReady(w http.ResponseWriter, r *http.Request) bool {
	if !app.env.OutputConfig.Enabled || app.outputStore == nil {
		sendError(w, getLogger(r), http.StatusNotFound, "Not found")
		return false
	}
	return true
}

func (app *application) outputFilesReady(w http.ResponseWriter, r *http.Request) bool {
	if !app.outputFeatureReady(w, r) {
		return false
	}
	if app.outputFiles == nil {
		sendError(w, getLogger(r), http.StatusServiceUnavailable, "Output file service is unavailable")
		return false
	}
	return true
}

// submitOutput godoc
// @Summary      Submit a notebook output run
// @Description  Stops an owned, ready notebook and queues an asynchronous output run. In booking mode the linked booking must be active.
// @Tags         outputs
// @Produce      json
// @Param        notebook_name   path    string  true  "Notebook Name"
// @Param        Idempotency-Key header  string  true  "Unique request key"
// @Success      202  {object}  OutputResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      409  {object}  Error409
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebooks/{notebook_name}/outputs [post]
func (app *application) submitOutput(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.outputFeatureReady(w, r) {
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
	if !output.IsSafeRelativePath(app.env.OutputConfig.SourceNotebookPath) {
		logger.Error("output source notebook path is invalid")
		sendError(w, logger, http.StatusInternalServerError, "Output configuration is invalid")
		return
	}

	record, _, err := app.outputStore.Create(r.Context(), output.CreateParams{
		UserID: user.Sub, NotebookName: notebookName,
		SourceNotebookPath:   app.env.OutputConfig.SourceNotebookPath,
		IdempotencyKey:       idempotencyKey,
		RequireActiveBooking: app.env.BookingsEnabled,
	})
	if err != nil {
		switch {
		case errors.Is(err, output.ErrNotebookNotFound):
			sendError(w, logger, http.StatusNotFound, "Notebook not found")
		case errors.Is(err, output.ErrNotebookNotReady):
			sendError(w, logger, http.StatusConflict, "Notebook is not ready for output")
		case errors.Is(err, output.ErrActiveOutput):
			sendError(w, logger, http.StatusConflict, "An output is already active for this notebook")
		default:
			logger.Error("failed to create output", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to submit output")
		}
		return
	}

	if record.Status == output.StatusStopping {
		if err := app.addStoppedAnnotationToNotebook(r.Context(), record.Namespace, record.NotebookName); err != nil {
			logger.Error("failed to stop notebook for output", "error", err, "output_id", record.ID)
			if markErr := app.outputStore.MarkStopFailed(r.Context(), record.ID,
				"notebook_stop_failed", "The notebook could not be stopped for output"); markErr != nil {
				logger.Error("failed to release output hold after stop failure", "error", markErr, "output_id", record.ID)
			}
			sendError(w, logger, http.StatusInternalServerError, "Failed to stop notebook for output")
			return
		}
		if err := app.outputStore.MarkNotebookStopped(r.Context(), record.ID); err != nil {
			logger.Error("failed to queue stopped notebook for output", "error", err, "output_id", record.ID)
			sendError(w, logger, http.StatusInternalServerError, "Failed to queue output")
			return
		}
		record.Status = output.StatusWaitingForPVC
	}

	sendResponseJson(w, logger, http.StatusAccepted, outputResponse(record, nil))
}

// getOutput godoc
// @Summary      Get an output run
// @Description  Returns an owned output run's status without exposing review-bucket keys.
// @Tags         outputs
// @Produce      json
// @Param        output_id  path  string  true  "Output ID"
// @Success      200  {object}  OutputResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Security     BearerAuth
// @Router       /v1/outputs/{output_id} [get]
func (app *application) getOutput(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.outputFeatureReady(w, r) {
		return
	}
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	outputID, ok := validOutputID(w, r)
	if !ok {
		return
	}
	record, err := app.outputStore.GetOwned(r.Context(), outputID, user.Sub)
	if err != nil {
		handleOutputLookupError(w, r, err)
		return
	}
	approval, found, err := app.outputStore.GetApproval(r.Context(), outputID)
	if err != nil {
		logger.Error("failed to load output approval", "error", err, "output_id", outputID)
		sendError(w, logger, http.StatusInternalServerError, "Failed to load output")
		return
	}
	var approvalStatus *string
	if found {
		status := string(approval.Status)
		approvalStatus = &status
	}
	sendResponseJson(w, logger, http.StatusOK, outputResponse(record, approvalStatus))
}

// listPendingOutputs godoc
// @Summary      List outputs pending NHA approval
// @Tags         output-admin
// @Produce      json
// @Param        limit query int false "Maximum outputs (1-200)"
// @Success      200  {object}  PendingOutputsResponse
// @Failure      403  {object}  Error403
// @Security     BearerAuth
// @Router       /v1/admin/outputs [get]
func (app *application) listPendingOutputs(w http.ResponseWriter, r *http.Request) {
	if !app.requireOutputAdmin(w, r) {
		return
	}
	if status := r.URL.Query().Get("status"); status != "" && status != string(output.StatusPendingApproval) {
		sendError(w, getLogger(r), http.StatusBadRequest, "status must be pending_approval")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 200 {
			limit = parsed
		} else {
			sendError(w, getLogger(r), http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
	}
	records, err := app.outputStore.ListPending(r.Context(), limit)
	if err != nil {
		getLogger(r).Error("failed to list pending outputs", "error", err)
		sendError(w, getLogger(r), http.StatusInternalServerError, "Failed to list pending outputs")
		return
	}
	response := PendingOutputsResponse{Outputs: make([]OutputResponse, len(records))}
	for index, record := range records {
		response.Outputs[index] = outputResponse(record, nil)
	}
	sendResponseJson(w, getLogger(r), http.StatusOK, response)
}

// listAdminOutputFiles godoc
// @Summary      List CSV files for an output awaiting approval
// @Tags         output-admin
// @Produce      json
// @Param        output_id path string true "Output ID"
// @Success      200  {object} OutputFilesResponse
// @Security     BearerAuth
// @Router       /v1/admin/outputs/{output_id}/files [get]
func (app *application) listAdminOutputFiles(w http.ResponseWriter, r *http.Request) {
	if !app.requireOutputAdmin(w, r) {
		return
	}
	record, manifest, ok := app.loadAdminManifest(w, r)
	if !ok {
		return
	}
	files := make([]OutputFile, len(manifest.Files))
	for index, file := range manifest.Files {
		files[index] = OutputFile{
			FileID: file.FileID, Name: file.Path, Size: file.Size,
			MediaType: file.MediaType, SHA256: file.SHA256,
		}
	}
	sendResponseJson(w, getLogger(r), http.StatusOK,
		OutputFilesResponse{OutputID: record.ID, Status: string(record.Status), Files: files})
}

// previewAdminOutputFile godoc
// @Summary      Preview a pending CSV output file
// @Tags         output-admin
// @Produce      json
// @Param        output_id path string true "Output ID"
// @Param        file_id path string true "Stable file ID"
// @Success      200  {object} filesconnect.Preview
// @Security     BearerAuth
// @Router       /v1/admin/outputs/{output_id}/files/{file_id}/preview [get]
func (app *application) previewAdminOutputFile(w http.ResponseWriter, r *http.Request) {
	if !app.outputFilesReady(w, r) || !app.requireOutputAdminRole(w, r) {
		return
	}
	record, manifest, ok := app.loadAdminManifest(w, r)
	if !ok {
		return
	}
	fileID := r.PathValue("file_id")
	for _, file := range manifest.Files {
		if file.FileID != fileID {
			continue
		}
		preview, err := app.outputFiles.PreviewReviewFile(r.Context(), record.ID, file.FileID)
		if err != nil {
			handleFilesConnectError(w, r, err)
			return
		}
		sendResponseJson(w, getLogger(r), http.StatusOK, preview)
		return
	}
	sendError(w, getLogger(r), http.StatusNotFound, "Output file not found")
}

// approveOutput godoc
// @Summary      Approve and publish output files
// @Description  Queues idempotent Files Connect publication into the owner's object-backed workspace.
// @Tags         output-admin
// @Produce      json
// @Param        output_id path string true "Output ID"
// @Param        Idempotency-Key header string true "Unique request key"
// @Success      202  {object} ApprovalResponse
// @Security     BearerAuth
// @Router       /v1/admin/outputs/{output_id}/approve [post]
func (app *application) approveOutput(w http.ResponseWriter, r *http.Request) {
	if !app.requireOutputAdmin(w, r) {
		return
	}
	user := r.Context().Value(UserContextKey).(UserInfo)
	idempotencyKey, ok := validateIdempotencyKey(r)
	if !ok {
		sendError(w, getLogger(r), http.StatusBadRequest, "A valid Idempotency-Key header is required")
		return
	}
	outputID, ok := validOutputID(w, r)
	if !ok {
		return
	}
	approval, _, err := app.outputStore.RequestApproval(r.Context(), output.ApprovalParams{
		OutputID: outputID, RequestedByAdmin: user.Sub, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, output.ErrOutputNotFound):
			sendError(w, getLogger(r), http.StatusNotFound, "Output not found")
		case errors.Is(err, output.ErrOutputNotReady):
			sendError(w, getLogger(r), http.StatusConflict, "Output is not ready for approval")
		default:
			getLogger(r).Error("failed to request output approval", "error", err, "output_id", outputID)
			sendError(w, getLogger(r), http.StatusInternalServerError, "Failed to request approval")
		}
		return
	}
	sendResponseJson(w, getLogger(r), http.StatusAccepted, ApprovalResponse{
		OutputID: approval.OutputID, Status: string(approval.Status),
	})
}

// listWorkspaceFiles godoc
// @Summary      List approved output files in the user's workspace
// @Tags         workspace
// @Produce      json
// @Success      200 {object} WorkspaceFilesResponse
// @Security     BearerAuth
// @Router       /v1/workspace/files [get]
func (app *application) listWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if !app.outputFilesReady(w, r) {
		return
	}
	user := r.Context().Value(UserContextKey).(UserInfo)
	files, err := app.outputFiles.ListWorkspace(r.Context(), user.Sub)
	if err != nil {
		handleFilesConnectError(w, r, err)
		return
	}
	sendResponseJson(w, getLogger(r), http.StatusOK, WorkspaceFilesResponse{Files: files})
}

// previewWorkspaceFile godoc
// @Summary      Preview an approved workspace CSV
// @Tags         workspace
// @Produce      json
// @Param        file_id path string true "Stable file ID"
// @Success      200 {object} filesconnect.Preview
// @Security     BearerAuth
// @Router       /v1/workspace/files/{file_id}/preview [get]
func (app *application) previewWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if !app.outputFilesReady(w, r) {
		return
	}
	user := r.Context().Value(UserContextKey).(UserInfo)
	preview, err := app.outputFiles.PreviewWorkspaceFile(r.Context(), user.Sub, r.PathValue("file_id"))
	if err != nil {
		handleFilesConnectError(w, r, err)
		return
	}
	sendResponseJson(w, getLogger(r), http.StatusOK, preview)
}

// downloadWorkspaceFile godoc
// @Summary      Get a short-lived download URL for an approved workspace CSV
// @Tags         workspace
// @Produce      json
// @Param        file_id path string true "Stable file ID"
// @Success      200 {object} filesconnect.Download
// @Security     BearerAuth
// @Router       /v1/workspace/files/{file_id}/download [get]
func (app *application) downloadWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if !app.outputFilesReady(w, r) {
		return
	}
	user := r.Context().Value(UserContextKey).(UserInfo)
	download, err := app.outputFiles.DownloadWorkspaceFile(r.Context(), user.Sub, r.PathValue("file_id"))
	if err != nil {
		handleFilesConnectError(w, r, err)
		return
	}
	sendResponseJson(w, getLogger(r), http.StatusOK, download)
}

func (app *application) loadAdminManifest(w http.ResponseWriter, r *http.Request) (output.Record, output.Manifest, bool) {
	outputID, ok := validOutputID(w, r)
	if !ok {
		return output.Record{}, output.Manifest{}, false
	}
	record, err := app.outputStore.GetByID(r.Context(), outputID)
	if err != nil {
		handleOutputLookupError(w, r, err)
		return output.Record{}, output.Manifest{}, false
	}
	if record.Status != output.StatusPendingApproval {
		sendError(w, getLogger(r), http.StatusConflict, "Output is not awaiting approval")
		return output.Record{}, output.Manifest{}, false
	}
	manifest, err := output.ParseManifest(record.OutputManifest,
		app.env.OutputConfig.MaxManifestBytes, app.env.OutputConfig.MaxManifestFiles,
		app.env.OutputConfig.MaxFileBytes, app.env.OutputConfig.MaxOutputBytes)
	if err == nil {
		err = output.ValidateManifestPrefix(manifest, record.ReviewPrefix)
	}
	if err != nil {
		getLogger(r).Error("stored output manifest is invalid", "error", err, "output_id", record.ID)
		sendError(w, getLogger(r), http.StatusInternalServerError, "Output manifest is unavailable")
		return output.Record{}, output.Manifest{}, false
	}
	return record, manifest, true
}

func (app *application) requireOutputAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !app.outputFeatureReady(w, r) {
		return false
	}
	return app.requireOutputAdminRole(w, r)
}

func (app *application) requireOutputAdminRole(w http.ResponseWriter, r *http.Request) bool {
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, getLogger(r), http.StatusUnauthorized, "Unauthorized")
		return false
	}
	if !hasConfiguredApproverRole(user.Roles, app.env.OutputConfig.ApproverRoles) {
		sendError(w, getLogger(r), http.StatusForbidden, "You are not permitted to approve outputs")
		return false
	}
	return true
}

func hasConfiguredApproverRole(userRoles []string, configured string) bool {
	if strings.TrimSpace(configured) == "" {
		return false
	}
	for _, role := range strings.Split(configured, ",") {
		for _, userRole := range userRoles {
			if strings.EqualFold(strings.TrimSpace(role), strings.TrimSpace(userRole)) {
				return true
			}
		}
	}
	return false
}

func validOutputID(w http.ResponseWriter, r *http.Request) (string, bool) {
	outputID := r.PathValue("output_id")
	if _, err := uuid.Parse(outputID); err != nil {
		sendError(w, getLogger(r), http.StatusBadRequest, "Invalid output id")
		return "", false
	}
	return outputID, true
}

func outputResponse(record output.Record, approvalStatus *string) OutputResponse {
	return OutputResponse{
		OutputID: record.ID, NotebookName: record.NotebookName, Status: string(record.Status),
		Approval: approvalStatus, ErrorCode: record.SafeErrorCode, ErrorMessage: record.SafeErrorMessage,
	}
}

func handleOutputLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, output.ErrOutputNotFound) {
		sendError(w, getLogger(r), http.StatusNotFound, "Output not found")
		return
	}
	getLogger(r).Error("failed to load output", "error", err)
	sendError(w, getLogger(r), http.StatusInternalServerError, "Failed to load output")
}

func handleFilesConnectError(w http.ResponseWriter, r *http.Request, err error) {
	if filesconnect.IsNotFound(err) {
		sendError(w, getLogger(r), http.StatusNotFound, "Output file not found")
		return
	}
	getLogger(r).Error("Files Connect request failed", "error", err)
	sendError(w, getLogger(r), http.StatusBadGateway, "Output file service request failed")
}

func (app *application) hasOutputHold(ctx context.Context, notebookID int64) (bool, error) {
	if !app.env.OutputConfig.Enabled || app.outputStore == nil {
		return false, nil
	}
	return app.outputStore.HasActiveHold(ctx, notebookID)
}

func (app *application) hasOutputHoldInTx(ctx context.Context, tx pgx.Tx, notebookID int64) (bool, error) {
	if !app.env.OutputConfig.Enabled || app.outputStore == nil {
		return false, nil
	}
	var held bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM output_jobs WHERE notebook_id=$1 AND hold_active=true)`,
		notebookID).Scan(&held)
	return held, err
}
