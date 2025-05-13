package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sandbox-backend-service/pkg/k8s"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

func CreatePVC(k8sClient *k8s.K8sClient, namespace, pvcName, storageSize string) error {
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
				"storageClassName": "eb2",
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
	_, err := k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Create(context.Background(), pvc, metav1.CreateOptions{})
	return err

}
func PVCWatcher(k8sClient *k8s.K8sClient, namespace, pvcName string) error {
	logger := slog.With("pvcName", pvcName,
		"pvcNamespace", namespace)
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	watcher, err := k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", pvcName),
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

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
			return errors.New("PVC was deleted before becoming bound.")
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("watch timed out waiting for PVC to become bound")
	} else if ctx.Err() == context.Canceled {
		return fmt.Errorf("watch was canceled")
	} else {
		return errors.New("watch ended unexpectedly")
	}
}
func CreateUploadFileToPVPod(k8sClient *k8s.K8sClient, namespace, pvcName, fileUploadPodName, fileDownloadURL string) error {
	logger := slog.With(
		"podName", fileUploadPodName,
		"podNamespace", namespace,
	)

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
	logger.Info("Creating Pod with main container logic")
	_, err := k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Create(context.Background(), podDefinition, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}
func CheckStatusOfUploadFilePod(k8sClient *k8s.K8sClient, namespace, notebookName, fileUploadPodName string) error {
	logger := slog.With("notebookName", notebookName,
		"namespace", namespace)

	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	watcher, err := k8sClient.Dynamic.Resource(podGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", fileUploadPodName),
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

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
		if status == "Succeeded" {
			logger.Info("pod successfully finished.")
			return nil
		}
		if status == "Failed" {
			return errors.New("pod got failed")
		}

		if event.Type == watch.Deleted {
			return errors.New("pod got deleted unexpectantly")
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("watch timed out waiting for Pod to succeed")
	} else if ctx.Err() == context.Canceled {
		return fmt.Errorf("watch was canceled")
	} else {
		return errors.New("watch ended unexpectedly")
	}
}
func CreateNotebook(k8sClient *k8s.K8sClient, nb Notebook, pvcName string) error {
	var limit map[string]any
	if nb.GPUType != nil && nb.GPUCount != nil && *nb.GPUCount != 0 {
		limit = map[string]any{
			"cpu":       nb.CPULimit,
			"memory":    nb.MemoryLimit,
			*nb.GPUType: nb.GPUCount,
		}
	} else {
		limit = map[string]any{
			"cpu":    nb.CPULimit,
			"memory": nb.MemoryLimit,
		}
	}

	notebookObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kubeflow.org/v1alpha1",
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
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  nb.Name,
								"image": "kubeflownotebookswg/jupyter-scipy:v1.9.2",
								"env":   []any{},
								"resources": map[string]any{
									"requests": map[string]any{
										"cpu":    nb.CPURequest,
										"memory": nb.MemoryRequest,
									},
									"limits": limit,
								},
								"volumeMounts": []any{
									map[string]any{
										"name":      "data-volume",
										"mountPath": "/home/jovyan/data",
									},
								},
							},
						},
						"volumes": []any{
							map[string]any{
								"name": "data-volume",
								"persistentVolumeClaim": map[string]any{
									"claimName": pvcName,
								},
							},
						},
						"serviceAccountName": "default-editor",
					},
				},
			},
		},
	}
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1alpha1",
		Resource: "notebooks",
	}
	_, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(nb.Namespace).Create(context.Background(), notebookObj, metav1.CreateOptions{})
	return err
}
