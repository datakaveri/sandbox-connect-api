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

	for {
		sem <- struct{}{}
		go app.worker(sem)
	}
}

func (app *application) worker(sem chan struct{}) {
	defer func() {
		<-sem
	}()
	ctx := context.Background()
	var notebook Notebook
	slog.Info("Waiting for notebook")
	for {
		notebookArr, err := FetchAndMarkNotebook(app.pgPool, ctx)
		if err != nil {
			slog.Error("failed fetching notebook", "error", err)
		} else if len(notebookArr) > 0 {
			notebook = notebookArr[0]
			break
		}
		time.Sleep(constants.PollInterval)
	}

	logger := slog.With("notebookName", notebook.Name,
		"namespace", notebook.Namespace)
	logger.Info("Spawning notebook")

	uploadPodName := notebook.Name + "-upload-pod-" + uuid.New().String()
	cleanupResources := func(failed *bool) {
		if *failed {
			logger.Info("Cleaning up resources due to failure")

			if err := DeleteNotebook(app.k8sClient, notebook.Namespace, notebook.Name); err != nil {
				logger.Error("Failed to delete notebook during cleanup", "error", err)
			} else {
				logger.Info("Successfully deleted notebook during cleanup")
			}

			if err := DeletePod(app.k8sClient, notebook.Namespace, uploadPodName); err != nil {
				logger.Error("Failed to delete upload pod during cleanup", "error", err)
			} else {
				logger.Info("Successfully deleted upload pod during cleanup")
			}

			if err := DeletePVC(app.k8sClient, notebook.Namespace, notebook.PVCname); err != nil {
				logger.Error("Failed to delete PVC during cleanup", "error", err)
			} else {
				logger.Info("Successfully deleted PVC during cleanup")
			}
		}
	}
	var failed bool = true
	var failedPtr *bool = &failed

	defer cleanupResources(failedPtr)

	if err := CreatePVC(app.k8sClient, notebook.Namespace, notebook.PVCname, notebook.StorageSize, app.env.STORAGE_CLASS_NAME); err != nil {
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
		logger.Info("PVC uploaded successfully")
	}
	err := CreateNotebook(app.k8sClient, notebook)
	if err != nil {
		logger.Error("failed to apply notebook manifest", "error", err, "notebookStatus", constants.StatusNotebookApplyFailed)
		queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookApplyFailed)
		if queryErr != nil {
			logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookApplyFailed)
		}
		return
	}

	queryErr := NotebookStatusUpdate(app.pgPool, ctx, notebook.ID, constants.StatusNotebookApplied)
	if queryErr != nil {
		logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusNotebookApplied)
	}
	logger.Info("Notebook applied successfully")
	failed = false
}
