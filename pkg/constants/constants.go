package constants

type Events string

const (
	StatusPicked                Events = "picked"
	StatusPVCApplied            Events = "pvc-applied"
	StatusPVCApplyFailed        Events = "pvc-apply-failed"
	StatusPVCCreated            Events = "pvc-created"
	StatusPVCCreationFailed     Events = "pvc-creation-failed"
	StatusPVCUploadSuccessful   Events = "pvc-upload-successful"
	StatusPVCUploadApplied      Events = "pvc-upload-applied"
	StatusPVCUploadApplyFailed  Events = "pvc-upload-apply-failed"
	StatusPVCUploadFailed       Events = "pvc-upload-failed"
	StatusNotebookApplied       Events = "notebook-applied"
	StatusNotebookAppliedFailed Events = "notebook-applied-failed"
	
	// Cleanup status events
	StatusCleanupRequested      Events = "cleanup-requested"
	StatusCleanupStarted        Events = "cleanup-started"
	StatusNotebookDeleted       Events = "notebook-deleted"
	StatusNotebookDeleteFailed  Events = "notebook-delete-failed"
	StatusPodDeleted            Events = "pod-deleted"
	StatusPodDeleteFailed       Events = "pod-delete-failed"
	StatusPVCDeleted            Events = "pvc-deleted"
	StatusPVCDeleteFailed       Events = "pvc-delete-failed"
	StatusCleanupCompleted      Events = "cleanup-completed"
)
