package main

import (
	"context"
	"sandbox-backend-service/pkg/constants"
	"time"

	"github.com/google/uuid"
)

func (app *application) worker(ctx context.Context) {
	workerId := uuid.New().String()
	logger := app.logger.With("workerId", workerId)

	var notebook Notebook
	logger.Info("Waiting for notebook")
	running := true
	for running {
		select {
		case <-ctx.Done():
			logger.Info("shutting down gracefully...")
			return
		default:
			notebookArr, err := FetchAndMarkNotebook(app.pgPool, ctx)
			if err != nil {
				logger.Error("failed fetching notebook", "error", err)
			} else if len(notebookArr) > 0 {
				notebook = notebookArr[0]
				running = false
			}
		}
		time.Sleep(constants.PollInterval)
	}
	logger = logger.With("notebookName", notebook.Name,
		"namespace", notebook.Namespace, "templateName", notebook.TemplateName, "notebookId", notebook.ID)
	worker := worker{
		app:      app,
		notebook: notebook,
		logger:   logger,
	}
	logger.Info("Spawning notebook")
	//uploadPodName := notebook.Name + "-upload-pod-" + uuid.New().String()
	cleanupResources := func(failed *bool) {
		if *failed {
			logger.Info("Cleaning up resources due to failure")
			if err := worker.DeleteNotebook(); err != nil {
				logger.Error("Failed to delete notebook during cleanup", "error", err)
			} else {
				logger.Info("Successfully deleted notebook during cleanup")
			}
			/*
				if err := worker.DeletePod(uploadPodName); err != nil {
					logger.Error("Failed to delete upload pod during cleanup", "error", err)
				} else {
					logger.Info("Successfully deleted upload pod during cleanup")
				}
			*/
			if err := worker.DeletePVC(); err != nil {
				logger.Error("Failed to delete PVC during cleanup", "error", err)
			} else {
				logger.Info("Successfully deleted PVC during cleanup")
			}
		}
	}
	var failed bool = true
	var failedPtr *bool = &failed

	defer cleanupResources(failedPtr)

	if err := worker.CreatePVC(); err != nil {
		logger.Error("failed to create pv", "error", err)
		queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCApplyFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCApplyFailed)
		}
		return
	}
	if err := worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCApplied); err != nil {
		logger.Error("failed to update notebook status", "error", err, "status", constants.StatusPVCApplied)
		return
	}

	/*
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
	*/
	logger.Info("PVC created")
	/*
		var presignedUrl *string
		if notebook.TemplateName != nil {
			var err error
			presignedUrl, err = app.s3Client.GetPresignedUrl(app.env.S3_TEMPLATE_BUCKET_NAME, getTemplateKey(*notebook.TemplateName))
			if err != nil {
				logger.Error("failed to get presigned url", "error", err)
				return
			}
			if err := worker.CreateUploadFileToPVPod(uploadPodName, *presignedUrl); err != nil {
				logger.Error("failed to apply pod manifest", "error", err)
				queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCUploadApplyFailed)
				if queryErr != nil {
					logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadApplyFailed)
				}
				return
			}
			queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCUploadApplied)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadApplied)
			}
			err = worker.CheckStatusOfUploadFilePod(uploadPodName)
			if err != nil {
				logger.Error("failed to complete the pod", "error", err)
				queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCUploadFailed)
				if queryErr != nil {
					logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadFailed)
				}
				return
			}
			queryErr = worker.NotebookStatusUpdate(notebook.ID, constants.StatusPVCUploadSuccessful)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusPVCUploadSuccessful)
				return
			}
			logger.Info("PVC uploaded successfully")
		}
	*/
	err := worker.CreateNotebook()
	if err != nil {
		logger.Error("failed to apply notebook manifest", "error", err, "notebookStatus", constants.StatusNotebookApplyFailed)
		queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusNotebookApplyFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookApplyFailed)
		}
		return
	}

	queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusNotebookApplied)
	if queryErr != nil {
		logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookApplied)
	}
	logger.Info("Notebook applied successfully")
	failed = false
}
