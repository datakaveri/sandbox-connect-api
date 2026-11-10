package main

import (
	"context"
	"fmt"
	"sandbox-backend-service/pkg/constants"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// notebookImageProjectNotebookDir maps project-specific notebook images to the
// folder they bake into the image. NHA images keep ps1/ps2/ps3 material in
// separate folders, so the worker must copy the matching folder into the PVC.
func notebookImageProjectNotebookDir(imageName string) string {
	lowerImageName := strings.ToLower(imageName)
	switch {
	case strings.Contains(lowerImageName, "nha-ps1"):
		return "ps1_notebooks"
	case strings.Contains(lowerImageName, "nha-ps2"):
		return "ps2_notebooks"
	case strings.Contains(lowerImageName, "nha-ps3"):
		return "ps3_notebooks"
	default:
		return ""
	}
}

func buildInitImageDemoCopyScript(notebookFlavor string) string {
	return fmt.Sprintf(`set -eu
copy_demo_files() {
  src_dir="$1"
  dest_dir="$2"
  if [ ! -d "$src_dir" ]; then
    return 1
  fi

  copied_any=1
  for f in "$src_dir"/*; do
    if [ -f "$f" ]; then
      copied_any=0
      filename=$(basename "$f")
      if [ ! -f "$dest_dir/$filename" ]; then
        cp "$f" "$dest_dir/$filename"
        chown 1000:1000 "$dest_dir/$filename"
        chmod 644 "$dest_dir/$filename"
        echo "[init] Copied $filename"
      else
        echo "[init] $filename already exists, skipping."
      fi
    fi
  done
  return "$copied_any"
}

# Init/demo image layouts:
# - mahaagx-style images: /tmp/demo_notebooks/cpu or /tmp/demo_notebooks/gpu
# - alternate flavor split: /tmp/cpu or /tmp/gpu
# - cbr-style flat copy: demo_notebooks/* baked directly into /tmp
SOURCE_DIR="/tmp/demo_notebooks/%s"
FLAVOR_DIR="/tmp/%s"
copy_demo_files "$SOURCE_DIR" /home/jovyan || true
copy_demo_files "$FLAVOR_DIR" /home/jovyan || true
copy_demo_files /tmp /home/jovyan || true
`, notebookFlavor, notebookFlavor)
}

func buildNotebookImageDemoCopyScript(notebookFlavor, projectNotebookDir string) string {
	return fmt.Sprintf(`set -eu
copy_demo_files() {
  src_dir="$1"
  dest_dir="$2"
  if [ ! -d "$src_dir" ]; then
    return 1
  fi

  copied_any=1
  for f in "$src_dir"/*; do
    if [ -f "$f" ]; then
      copied_any=0
      filename=$(basename "$f")
      if [ ! -f "$dest_dir/$filename" ]; then
        cp "$f" "$dest_dir/$filename"
        chown 1000:1000 "$dest_dir/$filename"
        chmod 644 "$dest_dir/$filename"
        echo "[init] Copied $filename"
      else
        echo "[init] $filename already exists, skipping."
      fi
    fi
  done
  return "$copied_any"
}

# The main container will mount the PVC at /home/jovyan, which shadows any files baked into the image.
# This init container uses the SAME notebook image, mounts the PVC elsewhere, and copies demo files into the persistent volume.

# 1. Legacy NHA images baked notebooks directly into /home/jovyan.
if ls /home/jovyan/*.ipynb 1> /dev/null 2>&1; then
  echo '[init] Extracting compiled .ipynb files from /home/jovyan to PVC...'
  for f in /home/jovyan/*.ipynb; do
    filename=$(basename "$f")
    if [ ! -f "/mnt/data/$filename" ]; then
      cp "$f" "/mnt/data/$filename"
      chown 1000:1000 "/mnt/data/$filename"
      chmod 644 "/mnt/data/$filename"
    fi
  done
fi

# Legacy NHA helper module, used by the skeletal notebooks.
if [ -f "/home/jovyan/nha_client.py" ]; then
  echo '[init] Extracting nha_client.py to PVC...'
  if [ ! -f "/mnt/data/nha_client.py" ]; then
    cp "/home/jovyan/nha_client.py" "/mnt/data/nha_client.py"
    chown 1000:1000 "/mnt/data/nha_client.py"
    chmod 644 "/mnt/data/nha_client.py"
  fi
fi

# 2. Current NHA images keep each problem statement in ps1/ps2/ps3 folders.
# The Go helper chooses one of ps1_notebooks, ps2_notebooks, or ps3_notebooks
# from the image tag, then this script copies that folder from either common bake location.
PROJECT_NOTEBOOK_DIR="%s"
if [ -n "$PROJECT_NOTEBOOK_DIR" ]; then
  copy_demo_files "/home/jovyan/$PROJECT_NOTEBOOK_DIR" /mnt/data || true
  copy_demo_files "/tmp/$PROJECT_NOTEBOOK_DIR" /mnt/data || true
fi

# 3. MahaAGX/multikernel images may split demo files by CPU/GPU flavor.
SOURCE_DIR="/tmp/demo_notebooks/%s"
FLAVOR_DIR="/tmp/%s"
copy_demo_files "$SOURCE_DIR" /mnt/data || true
copy_demo_files "$FLAVOR_DIR" /mnt/data || true

# 4. CBR/multikernel Dockerfiles can also copy demo_notebooks/* directly into /tmp.
copy_demo_files /tmp /mnt/data || true
`, projectNotebookDir, notebookFlavor, notebookFlavor)
}

/*
func (w *worker) PVCWatcher() error {
	namespace := w.notebook.Namespace
	pvcName := w.notebook.PVCname
	logger := w.logger.With("operation", "PVCWatcher")
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	ctx, cancel := WithTimeoutContext(context.Background(), K8sPVCWatcherTimeout)
	defer cancel()

	var watcher watch.Interface
	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		var watchErr error
		watcher, watchErr = w.app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", pvcName),
		})
		if k8serrors.IsNotFound(watchErr) {
			logger.Warn("PVC not found, will not retry watcher", "error", watchErr)
			cancel()
		} else if watchErr != nil {
			logger.Warn("failed to start PVC watcher, will retry", "error", watchErr)
			return constants.RetryContinue, watchErr
		}
		return constants.RetryStop, nil
	})

	if err != nil {
		logger.Error("failed to start PVC watcher after retries", "error", err)
		return err
	}
	defer watcher.Stop()

	logger.Info("watching PVC status", "pvcName", pvcName)
	for event := range watcher.ResultChan() {
		unstructuredPVC, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			logger.Warn("unexpected type, ignoring")
			continue
		}

		status, found, err := unstructured.NestedString(unstructuredPVC.Object, "status", "phase")

		if err != nil || !found {
			logger.Warn("could not get status phase for PVC")
			continue
		}
		logger.Info("current status", "status", status)
		if status == "Bound" {
			logger.Info("PVC is now Bound.")
			return nil
		}
		if event.Type == watch.Deleted {
			return errors.New("PVC was deleted before becoming bound")
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		logger.Error("watch timed out waiting for PVC to become bound", "timeout", K8sPVCWatcherTimeout)
		return fmt.Errorf("watch timed out after %v waiting for PVC to become bound", K8sPVCWatcherTimeout)
	} else if ctx.Err() == context.Canceled {
		return fmt.Errorf("watch was canceled")
	} else {
		return errors.New("watch ended unexpectedly")
	}
}


func (w *worker) CreateUploadFileToPVPod(fileUploadPodName, fileDownloadURL string) error {
	namespace := w.notebook.Namespace
	pvcName := w.notebook.PVCname
	logger := w.logger.With("operation", "CreateUploadFileToPVPod", "namespace", namespace, "pod", fileUploadPodName)

	ctx, cancel := WithTimeoutContext(context.Background(), K8sCreationTimeout)
	defer cancel()

	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}

	podDefinition := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      fileUploadPodName,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"volumes": []any{
					map[string]any{
						"name": "data-volume",
						"persistentVolumeClaim": map[string]any{
							"claimName": pvcName,
						},
					},
				},
				"containers": []any{
					map[string]any{
						"name":  "main-downloader",
						"image": "alpine:latest",
						"command": []string{
							"/bin/sh",
							"-c",
							fmt.Sprintf(`
							echo "Installing wget and unzip..."
							apk add --no-cache wget unzip

							echo "Downloading file from %s..."
							wget -O /workspace/downloaded_file.zip "%s"

							echo "Unzipping file..."
							unzip /workspace/downloaded_file.zip -d /workspace/template

							echo "Setting permissions..."
							chown -R 1000:1000 /workspace
							`, fileDownloadURL, fileDownloadURL),
						},
						"volumeMounts": []any{
							map[string]any{
								"name":      "data-volume",
								"mountPath": "/workspace",
							},
						},
					},
				},
			},
		},
	}

	logger.Info("creating upload file pod")

	err := WithK8sRetry(ctx, func() error {
		_, err := w.app.k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Create(ctx, podDefinition, metav1.CreateOptions{})
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				logger.Warn("pod already exists, will not retry creation", "error", err)
				cancel()
			} else {
				logger.Warn("failed to create upload pod, will retry", "error", err)
			}
			return err
		}
		return nil
	})

	if err != nil {
		logger.Error("failed to create upload pod after retries", "error", err)
		return err
	}

	logger.Info("successfully created upload file pod")
	return nil
}

func (w *worker) CheckStatusOfUploadFilePod(fileUploadPodName string) error {
	namespace := w.notebook.Namespace
	logger := w.logger.With("operation", "CheckStatusOfUploadFilePod", "namespace", namespace, "pod", fileUploadPodName)

	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}

	ctx, cancel := WithTimeoutContext(context.Background(), UploadPodWatcherTimeout)
	defer cancel()

	logger.Info("checking upload file pod status")

	var watcher watch.Interface
	err := WithK8sRetry(ctx, func() error {
		var watchErr error
		watcher, watchErr = w.app.k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", fileUploadPodName),
		})
		if k8serrors.IsNotFound(watchErr) {
			logger.Warn("pod not found, will not retry watcher", "error", watchErr)
			cancel()
		} else if watchErr != nil {
			logger.Warn("failed to start pod watcher, will retry", "error", watchErr)
			return watchErr
		}
		return nil
	})

	if err != nil {
		logger.Error("failed to start pod watcher after retries", "error", err)
		return err
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		unstructuredPod, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			logger.Warn("unexpected type in watch event, ignoring")
			continue
		}

		status, found, err := unstructured.NestedString(unstructuredPod.Object, "status", "phase")
		if err != nil || !found {
			logger.Warn("could not get status phase for pod")
			continue
		}

		logger.Info("current pod status", "status", status)

		if status == "Succeeded" {
			logger.Info("pod successfully finished")
			return nil
		}

		if status == "Failed" {
			logger.Error("pod failed")
			return errors.New("pod execution failed")
		}

		if event.Type == watch.Deleted {
			logger.Error("pod was unexpectedly deleted")
			return errors.New("pod was unexpectedly deleted")
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		logger.Error("watch timed out waiting for pod to succeed", "timeout", UploadPodWatcherTimeout)
		return fmt.Errorf("watch timed out after %v waiting for pod to succeed", UploadPodWatcherTimeout)
	} else if ctx.Err() == context.Canceled {
		logger.Error("watch was canceled")
		return fmt.Errorf("watch was canceled")
	} else {
		logger.Error("watch ended unexpectedly")
		return errors.New("watch ended unexpectedly")
	}
}
*/

func (w *worker) CreateNotebook() error {
	nb := w.notebook
	logger := w.logger.With("operation", "CreateNotebook")

	notebookObj, err := w.BuildNotebook()
	if err != nil {
		return fmt.Errorf("build notebook manifest: %w", err)
	}

	ctx, cancel := WithTimeoutContext(context.Background(), NotebookCreationTimeout)
	defer cancel()
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	err = WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := w.app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(nb.Namespace).Create(ctx, notebookObj, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			logger.Warn("notebook already exists, will not retry creation", "error", err)
			return constants.RetryStop, err
		} else if err != nil {
			logger.Warn("failed to create notebook, will retry", "error", err)
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})

	if err != nil {
		logger.Error("failed to create notebook after retries", "error", err)
		return err
	}

	logger.Info("notebook created successfully", "name", nb.Name, "namespace", nb.Namespace)
	return nil
}

func (w *worker) DeleteNotebook() error {
	namespace := w.notebook.Namespace
	notebookName := w.notebook.Name
	logger := w.logger.With("operation", "DeleteNotebook", "namespace", namespace, "name", notebookName)

	ctx, cancel := WithTimeoutContext(context.Background(), NotebookDeletionTimeout)
	defer cancel()

	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		err := w.app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(ctx, notebookName, deleteOptions)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return constants.RetryStop, nil
			}
			logger.Warn("failed to delete notebook resource, will retry", "error", err)
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})
	return err
}

func (w *worker) DeletePod(podName string) error {
	namespace := w.notebook.Namespace
	logger := w.logger.With("operation", "DeletePod", "namespace", namespace, "name", podName)

	ctx, cancel := WithTimeoutContext(context.Background(), K8sDeletionTimeout)
	defer cancel()

	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		err := w.app.k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Delete(ctx, podName, deleteOptions)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return constants.RetryStop, nil
			}
			logger.Warn("failed to delete pod, will retry", "error", err)
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})
	return err
}
