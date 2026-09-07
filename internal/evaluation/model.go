package evaluation

import (
	"encoding/json"
	"errors"
	"time"
)

type Status string

const (
	StatusStopping      Status = "stopping"
	StatusWaitingForPVC Status = "waiting_for_pvc"
	StatusPreparing     Status = "preparing"
	StatusConverting    Status = "converting"
	StatusConfiguring   Status = "configuring"
	StatusExecuting     Status = "executing"
	StatusUploading     Status = "uploading"
	StatusSucceeded     Status = "succeeded"
	StatusFailed        Status = "failed"
)

type ApprovalStatus string

const (
	ApprovalRequested ApprovalStatus = "requested"
	ApprovalCopying   ApprovalStatus = "copying"
	ApprovalApproved  ApprovalStatus = "approved"
	ApprovalFailed    ApprovalStatus = "failed"
)

var (
	ErrNotebookNotFound   = errors.New("notebook not found")
	ErrNotebookNotReady   = errors.New("notebook is not ready")
	ErrEvaluationNotFound = errors.New("evaluation not found")
	ErrActiveEvaluation   = errors.New("an evaluation is already active for this notebook")
	ErrEvaluationNotReady = errors.New("evaluation output is not ready for approval")
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
	OutputPrefix       string
	ManifestKey        string
	OutputManifest     json.RawMessage
	HoldActive         bool
	SafeErrorCode      *string
	SafeErrorMessage   *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Approval struct {
	EvaluationID     string
	RequestedBy      string
	Status           ApprovalStatus
	CopyJobName      *string
	Destination      string
	AttemptCount     int
	SafeErrorCode    *string
	SafeErrorMessage *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type CreateParams struct {
	UserID               string
	NotebookName         string
	SourceNotebookPath   string
	IdempotencyKey       string
	RequireActiveBooking bool
}

type ApprovalParams struct {
	EvaluationID   string
	RequestedBy    string
	IdempotencyKey string
	Destination    string
}
