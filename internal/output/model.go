package output

import (
	"encoding/json"
	"errors"
	"time"
)

type Status string

const (
	StatusStopping        Status = "stopping"
	StatusWaitingForPVC   Status = "waiting_for_pvc"
	StatusPreparing       Status = "preparing"
	StatusConverting      Status = "converting"
	StatusConfiguring     Status = "configuring"
	StatusExecuting       Status = "executing"
	StatusUploading       Status = "uploading"
	StatusPendingApproval Status = "pending_approval"
	StatusFailed          Status = "failed"
)

type ApprovalStatus string

const (
	ApprovalRequested  ApprovalStatus = "requested"
	ApprovalPublishing ApprovalStatus = "publishing"
	ApprovalApproved   ApprovalStatus = "approved"
	ApprovalFailed     ApprovalStatus = "failed"
)

var (
	ErrNotebookNotFound = errors.New("notebook not found")
	ErrNotebookNotReady = errors.New("notebook is not ready")
	ErrOutputNotFound   = errors.New("output not found")
	ErrActiveOutput     = errors.New("an output is already active for this notebook")
	ErrOutputNotReady   = errors.New("output is not ready for approval")
)

type Record struct {
	ID                 string
	NotebookID         int64
	UserID             string
	NotebookName       string
	Namespace          string
	SourcePVC          string
	SourceNotebookPath string
	Status             Status
	WorkflowName       *string
	WorkflowUID        *string
	ReviewPrefix       string
	ReviewManifestKey  string
	OutputManifest     json.RawMessage
	HoldActive         bool
	SafeErrorCode      *string
	SafeErrorMessage   *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Approval struct {
	OutputID             string
	RequestedByAdmin     string
	Status               ApprovalStatus
	WorkspacePrefix      string
	WorkspaceManifestKey *string
	ApprovedFileIDs      json.RawMessage
	AttemptCount         int
	SafeErrorCode        *string
	SafeErrorMessage     *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CreateParams struct {
	UserID               string
	NotebookName         string
	SourceNotebookPath   string
	IdempotencyKey       string
	RequireActiveBooking bool
}

type ApprovalParams struct {
	OutputID         string
	RequestedByAdmin string
	IdempotencyKey   string
}

type PublicationResult struct {
	WorkspacePrefix      string
	WorkspaceManifestKey string
	ApprovedFileIDs      []string
}
