package main

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"time"

	"github.com/google/uuid"
)

func (app *application) spinner() {
	sem := make(chan struct{}, app.env.MAX_CONCURRENT_WORKER)
	
	// Start workers for notebook creation
	for i := 0; i < app.env.MAX_CONCURRENT_WORKER/2; i++ {
		sem <- struct{}{}
		go app.worker(sem, false)
	}
	
	// Start workers for cleanup operations
	for i := 0; i < app.env.MAX_CONCURRENT_WORKER/2; i++ {
		sem <- struct{}{}
		go app.worker(sem, true)
	}
}

func (app *application) worker(sem chan struct{}, isCleanupWorker bool) {
	defer func() {
		<-sem
	}()
	ctx := context.Background()
	var notebook Notebook
	
	for {
		var notebookArr []Notebook
		var err error
		
		if isCleanupWorker {
			// Fetch notebooks marked for cleanup
			notebookArr, err = FetchNotebooksForCleanup(app.pgPool, ctx)
		} else {
			// Fetch notebooks for creation
			notebookArr, err = FetchAndMarkNotebook(app.pgPool, ctx)
		}
		
		if err != nil {
			slog.Error("failed fetching notebook", "error", err, "isCleanupWorker", isCleanupWorker)
		} else if len(notebookArr) == 0 {
			slog.Info("no notebook found", "isCleanupWorker", isCleanupWorker)
		} else {
			notebook = notebookArr[0]
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	logger := slog.With(
		"notebookName", notebook.Name,
		"namespace", notebook.Namespace,
		"notebookId", notebook.ID,
		"isCleanupWorker", isCleanupWorker,
	)

	// Handle cleanup if this is a cleanup worker
	if isCleanupWorker {
		app.cleanupNotebookResources(ctx, notebook, logger)
		return
	}

	// Regular notebook creation flow
	uploadPodName := notebook.Name + "-upload-pod-" + uuid.New().String()
	if err := CreatePVC(app.k8sClient, notebook.Namespace, notebook.PVCname, notebook.StorageSize); err != nil {
		logger.Error("failed to create pv", "error", err)
		queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCApplyFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCApplyFailed)
		}
		return
	}
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCApplied); err != nil {
		logger.Error("failed to update notebook status", "error", err, "status", constants.StatusPVCApplied)
		return
	}
	if err := PVCWatcher(app.k8sClient, notebook.Namespace, notebook.PVCname); err != nil {
		logger.Error("failed to create pv", "error", err)
		queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCCreationFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCCreationFailed)
		}
		return
	}
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCCreated); err != nil {
		logger.Error("failed to update notebook status", "error", err, "status", constants.StatusPVCCreated)
		return
	}

	var presignedUrl *string
	if notebook.TemplateName != nil {
		var err error
		presignedUrl, err = app.s3Client.GetPresignedUrl(app.env.S3_TEMPLATE_BUCKET_NAME, getTemplateKey(*notebook.TemplateName))
		if err != nil {
			logger.Error("failed to get presigned url", "error", err)
			return
		}
		if err := CreateUploadFileToPVPod(app.k8sClient, notebook.Namespace, notebook.PVCname, uploadPodName, *presignedUrl); err != nil {
			logger.Error("failed to apply pod manifest", "error", err)
			queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCUploadApplyFailed)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadApplyFailed)
			}
			return
		}
		queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCUploadApplied)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadApplied)
		}
		err = CheckStatusOfUploadFilePod(app.k8sClient, notebook.Namespace, notebook.Name, uploadPodName)
		if err != nil {
			logger.Error("failed to complete the pod", "error", err)
			queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCUploadFailed)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadFailed)
			}
			return
		}
		queryErr = NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCUploadSuccessful)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadSuccessful)
			return
		}
	}
	err := CreateNotebook(app.k8sClient, notebook, notebook.PVCname)
	if err != nil {
		logger.Error("failed to apply notebook manifest", "error", err, "notebookStatus", constants.StatusNotebookAppliedFailed)
		queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookAppliedFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookAppliedFailed)
		}
		return
	}

	queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookApplied)
	if queryErr != nil {
		logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookApplied)
	}
}

// cleanupNotebookResources handles the cleanup of all Kubernetes resources associated with a notebook
func (app *application) cleanupNotebookResources(ctx context.Context, notebook Notebook, logger *slog.Logger) {
	// Mark the notebook as being cleaned up
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusCleanupStarted); err != nil {
		logger.Error("failed to update notebook cleanup status", "error", err)
		return
	}
	
	// 1. Delete the Notebook CR first
	logger.Info("Starting cleanup of notebook resources")
	if err := DeleteNotebook(app.k8sClient, notebook.Namespace, notebook.Name); err != nil {
		logger.Error("failed to delete notebook CR", "error", err)
		if updateErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookDeleteFailed); updateErr != nil {
			logger.Error("failed to update notebook status", "error", updateErr)
		}
		return
	}
	
	// Update status after notebook deletion
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookDeleted); err != nil {
		logger.Error("failed to update notebook status after deletion", "error", err)
		return
	}
	
	// 2. Find and delete any upload pods associated with this notebook
	// We'll look for pods with the naming pattern: notebook.Name + "-upload-pod-"
	podPrefix := notebook.Name + "-upload-pod-"
	
	// This would require listing pods in the namespace and filtering by name prefix
	// For now, we'll use the PVC name which we have
	if err := DeletePod(app.k8sClient, notebook.Namespace, podPrefix); err != nil {
		logger.Error("failed to delete upload pod", "error", err)
		if updateErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPodDeleteFailed); updateErr != nil {
			logger.Error("failed to update notebook status", "error", updateErr)
		}
		// Continue with PVC deletion even if pod deletion fails
	} else {
		if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPodDeleted); err != nil {
			logger.Error("failed to update notebook status after pod deletion", "error", err)
		}
	}
	
	// 3. Delete the PVC
	if err := DeletePVC(app.k8sClient, notebook.Namespace, notebook.PVCname); err != nil {
		logger.Error("failed to delete PVC", "error", err)
		if updateErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCDeleteFailed); updateErr != nil {
			logger.Error("failed to update notebook status", "error", updateErr)
		}
		return
	}
	
	// Update status after PVC deletion
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusPVCDeleted); err != nil {
		logger.Error("failed to update notebook status after PVC deletion", "error", err)
		return
	}
	
	// Mark cleanup as completed
	if err := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusCleanupCompleted); err != nil {
		logger.Error("failed to mark cleanup as completed", "error", err)
		return
	}
	
	logger.Info("Successfully completed cleanup of all notebook resources")
}
