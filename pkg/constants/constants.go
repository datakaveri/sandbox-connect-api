package constants

type Events string

const (
	StatusPicked               Events = "picked"
	StatusPVCApplied           Events = "pvc-applied"
	StatusPVCApplyFailed       Events = "pvc-apply-failed"
	StatusPVCCreated           Events = "pvc-created"
	StatusPVCCreationFailed    Events = "pvc-creation-failed"
	StatusPVCUploadSuccessful  Events = "pvc-upload-successful"
	StatusPVCUploadApplied     Events = "pvc-upload-applied"
	StatusPVCUploadApplyFailed Events = "pvc-upload-apply-failed"
	StatusPVCUploadFailed      Events = "pvc-upload-failed"
	StatusNotebookApplied      Events = "notebook-applied"
	StatusNotebookApplyFailed  Events = "notebook-apply-failed"
)

type ShouldContinue bool

const (
	RetryContinue ShouldContinue = true
	RetryStop     ShouldContinue = false
)
