package main

import (
	"context"
	"fmt"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (w *worker) CreatePVC() error {
	namespace := w.notebook.Namespace
	pvcName := w.notebook.PVCname
	storageSize := w.notebook.StorageSize
	logger := w.logger.With("operation", "CreatePVC")

	ctx, cancel := WithTimeoutContext(context.Background(), K8sCreationTimeout)
	defer cancel()

	pvc := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata": map[string]any{
				"name":      pvcName,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"accessModes": []any{
					"ReadWriteOnce",
				},
				"storageClassName": w.app.env.STORAGE_CLASS_NAME,
				"resources": map[string]any{
					"requests": map[string]any{
						"storage": storageSize,
					},
				},
			},
		},
	}
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := w.app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		//it can happen that our notebook is in deletion phase and it is still showing it exists so we better throw error
		if k8serrors.IsAlreadyExists(err) {
			logger.Warn("PVC already exists, will not retry creation", "error", err)
			return constants.RetryStop, err
		}
		return constants.RetryContinue, err
	})

	return err
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

	ctx, cancel := WithTimeoutContext(context.Background(), NotebookCreationTimeout)
	defer cancel()

	var limit map[string]any
	var imageName string
	var request map[string]any
	if utils.CheckGPUResource(nb.GPUType, nb.GPURequest, nb.GPULimit) {
		imageName = w.app.env.GPU_NOTEBOOK_IMAGE
		limit = map[string]any{
			"cpu":       fmt.Sprintf("%.6f", nb.CPULimit),
			"memory":    nb.MemoryLimit,
			*nb.GPUType: *nb.GPULimit,
		}
		request = map[string]any{
			"cpu":       fmt.Sprintf("%.6f", nb.CPURequest),
			"memory":    nb.MemoryRequest,
			*nb.GPUType: *nb.GPURequest,
		}
	} else {
		imageName = w.app.env.CPU_NOTEBOOK_IMAGE
		limit = map[string]any{
			"cpu":    fmt.Sprintf("%.6f", nb.CPULimit),
			"memory": nb.MemoryLimit,
		}
		request = map[string]any{
			"cpu":    fmt.Sprintf("%.6f", nb.CPURequest),
			"memory": nb.MemoryRequest,
		}
	}

	// Build the notebook spec
	specTemplateSpec := map[string]any{
		"initContainers": []any{
			map[string]any{
				"name":  "init-demo-ipynb",
				"image": w.app.env.INIT_CONTAINER_IMAGE,
				"command": []any{"/bin/sh", "-c", `
if [ -f /home/jovyan/demo.ipynb ]; then
  echo '[init] /home/jovyan/demo.ipynb already exists, skipping copy.'
else
  echo '[init] /home/jovyan/demo.ipynb not found, attempting to move from /tmp/demo.ipynb...'
  if mv /tmp/demo.ipynb /home/jovyan/demo.ipynb; then
    echo '[init] Successfully moved /tmp/demo.ipynb to /home/jovyan/demo.ipynb.'
    chown 1000:1000 /home/jovyan/demo.ipynb
    chmod 644 /home/jovyan/demo.ipynb
  else
    echo '[init] Failed to move /tmp/demo.ipynb to /home/jovyan/demo.ipynb.'
    exit 1
  fi
fi

if [ -f /home/jovyan/requirements.txt ]; then
  echo '[init] /home/jovyan/requirements.txt already exists, skipping copy.'
else
  echo '[init] /home/jovyan/requirements.txt not found, attempting to move from /tmp/requirements.txt...'
  if mv /tmp/requirements.txt /home/jovyan/requirements.txt; then
    echo '[init] Successfully moved /tmp/requirements.txt to /home/jovyan/requirements.txt.'
    chown 1000:1000 /home/jovyan/requirements.txt
    chmod 644 /home/jovyan/requirements.txt
  else
    echo '[init] Failed to move /tmp/requirements.txt to /home/jovyan/requirements.txt.'
    exit 1
  fi
fi

if [ -f /home/jovyan/Python_Packages_Installation_Demo.ipynb ]; then
  echo '[init] /home/jovyan/Python_Packages_Installation_Demo.ipynb already exists, skipping copy.'
else
  echo '[init] /home/jovyan/Python_Packages_Installation_Demo.ipynb not found, attempting to move from /tmp/Python_Packages_Installation_Demo.ipynb...'
  if mv /tmp/Python_Packages_Installation_Demo.ipynb /home/jovyan/Python_Packages_Installation_Demo.ipynb; then
    echo '[init] Successfully moved /tmp/Python_Packages_Installation_Demo.ipynb to /home/jovyan/Python_Packages_Installation_Demo.ipynb.'
    chown 1000:1000 /home/jovyan/Python_Packages_Installation_Demo.ipynb
    chmod 644 /home/jovyan/Python_Packages_Installation_Demo.ipynb
  else
    echo '[init] Failed to move /tmp/Python_Packages_Installation_Demo.ipynb to /home/jovyan/Python_Packages_Installation_Demo.ipynb.'
    exit 1
  fi
fi
`},
				"volumeMounts": []any{
					map[string]any{
						"name":      "data-volume",
						"mountPath": "/home/jovyan",
					},
				},
			},
		},
		"containers": []any{
			map[string]any{
				"name":  nb.Name,
				"image": imageName,
				"securityContext": map[string]any{
					"privileged":               false,
					"procMount":                "Default",
					"allowPrivilegeEscalation": false,
				},
				"resources": map[string]any{
					"requests": request,
					"limits":   limit,
				},
				"volumeMounts": []any{
					map[string]any{
						"name":      "data-volume",
						"mountPath": "/home/jovyan",
					},
				},
			},
		},
		"volumes": []any{
			map[string]any{
				"name": "data-volume",
				"persistentVolumeClaim": map[string]any{
					"claimName": nb.PVCname,
				},
			},
		},
		"serviceAccountName": "default-editor",
	}

	// Add nodeSelector for CPU or GPU notebooks
	if utils.CheckGPUResource(nb.GPUType, nb.GPURequest, nb.GPULimit) {
		specTemplateSpec["nodeSelector"] = map[string]any{
			"node.kubernetes.io/instance-type": "g4dn.xlarge",
		}
	} else {
		specTemplateSpec["affinity"] = map[string]any{
			"nodeAffinity": map[string]any{
				"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
					"nodeSelectorTerms": []any{
						map[string]any{
							"matchExpressions": []any{
								map[string]any{
									"key":      "node.kubernetes.io/instance-type",
									"operator": "In",
									"values":   []any{"t3a.2xlarge", "c5a.4xlarge"},
								},
							},
						},
					},
				},
			},
		}
	}

	notebookObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kubeflow.org/v1beta1",
			"kind":       "Notebook",
			"metadata": map[string]any{
				"name":      nb.Name,
				"namespace": nb.Namespace,
				"labels": map[string]any{
					"app": nb.Name,
				},
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": specTemplateSpec,
				},
			},
		},
	}
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
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

func (w *worker) DeletePVC() error {
	namespace := w.notebook.Namespace
	pvcName := w.notebook.PVCname
	logger := w.logger.With("operation", "DeletePVC", "namespace", namespace, "name", pvcName)

	ctx, cancel := WithTimeoutContext(context.Background(), K8sDeletionTimeout)
	defer cancel()

	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		err := w.app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(ctx, pvcName, deleteOptions)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return constants.RetryStop, nil
			}
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})
	return err
}
