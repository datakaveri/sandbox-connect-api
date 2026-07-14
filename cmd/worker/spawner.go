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
			notebookArr, err := FetchAndMarkNotebook(app.pgPool, logger, ctx)
			if err != nil {
				logger.Error("failed fetching notebook", "error", err)
			} else if len(notebookArr) > 0 {
				notebook = notebookArr[0]
				running = false
			}
		}
		time.Sleep(PollInterval)
	}
	logger = logger.With("notebookName", notebook.Name,
		"namespace", notebook.Namespace, "templateName", notebook.TemplateName, "notebookId", notebook.ID)
	worker := worker{
		app:      app,
		notebook: notebook,
		logger:   logger,
	}
	logger.Info("Spawning notebook")
	runtimeInjectionPodName := ""
	if notebook.HasRuntimeAssets() {
		runtimeInjectionPodName = newRuntimeInjectionPodName(notebook.Name)
	}
	cleanupResources := func(failed *bool) {
		if *failed {
			logger.Info("Cleaning up resources due to failure")
			if runtimeInjectionPodName != "" {
				if err := worker.DeleteRuntimeInjectionPod(runtimeInjectionPodName); err != nil {
					logger.Error("Failed to delete runtime injection pod during cleanup", "error", err)
				}
			}
			if err := worker.DeleteNotebook(); err != nil {
				logger.Error("Failed to delete notebook during cleanup", "error", err)
			}
			/*
				if err := worker.DeletePod(uploadPodName); err != nil {
					logger.Error("Failed to delete upload pod during cleanup", "error", err)
				} else {
					logger.Info("Successfully deleted upload pod during cleanup")
				}
			*/
			if err := worker.DeletePreparedManagedPVCs(); err != nil {
				logger.Error("Failed to delete managed PVCs during cleanup", "error", err)
			}
			logger.Info("Successfully cleaned up")
		}
	}

	var failed bool = true
	var failedPtr *bool = &failed

	defer cleanupResources(failedPtr)

	if err := worker.PreparePVCMounts(); err != nil {
		logger.Error("failed to prepare configured PVC mounts", "error", err)
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
	if notebook.HasRuntimeAssets() {
		if err := worker.CreateRuntimeInjectionPod(runtimeInjectionPodName); err != nil {
			logger.Error("failed to create runtime injection pod", "error", err)
			queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusRuntimeInjectionApplyFailed)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusRuntimeInjectionApplyFailed)
			}
			return
		}
		if err := worker.NotebookStatusUpdate(notebook.ID, constants.StatusRuntimeInjectionApplied); err != nil {
			logger.Error("failed to update notebook status", "error", err, "status", constants.StatusRuntimeInjectionApplied)
			return
		}
		if err := worker.WaitRuntimeInjectionPod(runtimeInjectionPodName); err != nil {
			logger.Error("runtime injection pod failed", "error", err)
			queryErr := worker.NotebookStatusUpdate(notebook.ID, constants.StatusRuntimeInjectionFailed)
			if queryErr != nil {
				logger.Error("failed to update notebook status", "error", queryErr, "status", constants.StatusRuntimeInjectionFailed)
			}
			return
		}
		if err := worker.NotebookStatusUpdate(notebook.ID, constants.StatusRuntimeInjectionSuccessful); err != nil {
			logger.Error("failed to update notebook status", "error", err, "status", constants.StatusRuntimeInjectionSuccessful)
			return
		}
		if err := worker.DeleteRuntimeInjectionPod(runtimeInjectionPodName); err != nil {
			logger.Warn("failed to delete successful runtime injection pod", "error", err)
		}
		runtimeInjectionPodName = ""
	}

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
