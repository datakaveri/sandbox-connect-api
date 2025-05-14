package main

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/k8s"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		time.Sleep(constants.PollInterval)
	}

	logger := slog.With("notebookName", notebook.Name,
		"namespace", notebook.Namespace)

	uploadPodName := notebook.Name + "-upload-pod-" + uuid.New().String()
	cleanupResources := func(failed bool) {
		if failed {
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

	defer cleanupResources(failed)

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

	failed = false
}

func DeleteNotebook(k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	logger := slog.With(
		"notebookName", notebookName,
		"namespace", namespace,
		"action", "delete",
	)

	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1alpha1",
		Resource: "notebooks",
	}

	logger.Info("Deleting Notebook resource")

	_, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
	if err != nil {
		logger.Warn("Notebook not found, may have been already deleted", "error", err)
		return nil
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(context.Background(), notebookName, deleteOptions)
	if err != nil {
		logger.Error("Failed to delete Notebook resource", "error", err)
		return err
	}

	logger.Info("Successfully deleted Notebook resource")
	return nil
}
func DeletePod(k8sClient *k8s.K8sClient, namespace, podName string) error {
	logger := slog.With(
		"podName", podName,
		"namespace", namespace,
		"action", "delete",
	)

	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}

	_, err := k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		logger.Warn("Pod not found, may have been already deleted", "error", err)
		return nil
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Delete(context.Background(), podName, deleteOptions)
	if err != nil {
		logger.Error("Failed to delete Pod", "error", err)
		return err
	}

	logger.Info("Successfully deleted Pod")
	return nil
}

func DeletePVC(k8sClient *k8s.K8sClient, namespace, pvcName string) error {
	logger := slog.With(
		"pvcName", pvcName,
		"namespace", namespace,
		"action", "delete",
	)

	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	logger.Info("Deleting PersistentVolumeClaim")

	_, err := k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		logger.Warn("PVC not found, may have been already deleted", "error", err)
		return nil
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(context.Background(), pvcName, deleteOptions)
	if err != nil {
		logger.Error("Failed to delete PVC", "error", err)
		return err
	}

	logger.Info("Successfully deleted PVC")
	return nil
}
