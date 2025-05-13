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
	for true {
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
	for {
		notebookArr, err := FetchAndMarkNotebook(app.pgPool, ctx)
		if err != nil {
			slog.Error("failed fetching notebook", "error", err)
		} else if len(notebookArr) == 0 {
			slog.Info("no notebook found")
		} else {
			notebook = notebookArr[0]
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	logger := slog.With("notebookName", notebook.Name,
		"namespace", notebook.Namespace)

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
