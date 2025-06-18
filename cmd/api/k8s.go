package main

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (app *application) addStoppedAnnotationToNotebook(ctx context.Context, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	notebook, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	annotations, found, err := unstructured.NestedMap(notebook.Object, "metadata", "annotations")
	if err != nil {
		return err
	}

	if !found {
		annotations = make(map[string]interface{})
	}

	stopTime := time.Now().UTC().Format(time.RFC3339)
	annotations["kubeflow-resource-stopped"] = stopTime

	if err := unstructured.SetNestedMap(notebook.Object, annotations, "metadata", "annotations"); err != nil {
		return err
	}

	_, err = app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(ctx, notebook, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

func (app *application) removeStoppedAnnotationFromNotebook(ctx context.Context, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	notebook, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	annotations, found, err := unstructured.NestedMap(notebook.Object, "metadata", "annotations")
	if err != nil {
		return err
	}

	if !found {
		return nil
	}

	_, exists := annotations["kubeflow-resource-stopped"]
	if !exists {
		return nil
	}

	delete(annotations, "kubeflow-resource-stopped")

	if err := unstructured.SetNestedMap(notebook.Object, annotations, "metadata", "annotations"); err != nil {
		return err
	}

	_, err = app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(ctx, notebook, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

func (app *application) deleteNotebookFromK8s(ctx context.Context, logger *slog.Logger, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(ctx, notebookName, deleteOptions)
		if err != nil && k8serrors.IsNotFound(err) {
			return constants.RetryStop, err
		}
		return constants.RetryContinue, err
	})
}

/*
	func (app *application) getNotebooksJSON(ctx context.Context, namespace string) (map[string]any, error) {
		notebookGVR := schema.GroupVersionResource{
			Group:    "kubeflow.org",
			Version:  "v1beta1",
			Resource: "notebooks",
		}

		list, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		notebooks := make(map[string]any)

		for _, item := range list.Items {
			notebooks[item.GetName()] = item.Object
		}

		slog.Info("notebooks", "notebooks", notebooks)
		slog.Info("list", "list", list)

		return notebooks, nil
	}
*/
func (app *application) deletePVCFromK8s(ctx context.Context, logger *slog.Logger, namespace, pvcName string) error {
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		err := app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(ctx, pvcName, deleteOptions)
		if err != nil && k8serrors.IsNotFound(err) {
			return constants.RetryStop, err
		}
		return constants.RetryContinue, err
	})
}

func (app *application) getNotebookJSON(ctx context.Context, logger *slog.Logger, namespace, notebookName string) (*unstructured.Unstructured, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	var spec *unstructured.Unstructured
	err := WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		var err error
		spec, err = app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
		return constants.RetryContinue, err
	})

	return spec, err
}

func (app *application) createKubeflowProfile(ctx context.Context, logger *slog.Logger, userId, email string) error {
	profile := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kubeflow.org/v1",
			"kind":       "Profile",
			"metadata": map[string]any{
				"name": userId,
			},
			"spec": map[string]any{
				"owner": map[string]any{
					"kind": "User",
					"name": email,
				},
				"plugins":           []any{},
				"resourceQuotaSpec": map[string]any{},
			},
		},
	}

	profileGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}

	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := app.k8sClient.Dynamic.Resource(profileGVR).Create(ctx, profile, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			logger.Info("profile already exists", "profileName", userId)
			return constants.RetryStop, err
		}
		return constants.RetryContinue, err
	})
}

// CountRunningNotebooks returns the number of running CPU and GPU notebooks in the given namespace.
func (app *application) CountRunningNotebooks(ctx context.Context, namespace string, gpuTypeKey string) (int, int, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	list, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}
	runningCPU, runningGPU := 0, 0
	for _, item := range list.Items {
		annotations, found, err := unstructured.NestedMap(item.Object, "metadata", "annotations")
		if err != nil {
			return 0, 0, err
		}
		if !found {
			annotations = map[string]any{}
		}
		if IsNotebookRunningFromAnnotations(annotations) {
			gpuType, _, _ := unstructured.NestedString(item.Object, "spec", "template", "spec", "containers", "0", "resources", "limits", gpuTypeKey)
			if gpuType == "" {
				runningCPU++
			} else {
				runningGPU++
			}
		}
	}
	return runningCPU, runningGPU, nil
}

// DBNotebookInfo holds minimal info about a notebook from the DB
// for running state calculation
// Type: "cpu" or "gpu"
type DBNotebookInfo struct {
	Name        string
	Type        string // "cpu" or "gpu"
	LatestEvent constants.Events
}

// getSuccessfullyCreatedNotebookCounts returns the total number of successfully created CPU and GPU notebooks
// in the given namespace, ignoring stopped ones. It does not check for "applied" state, just existence in k8s.
func (app *application) getSuccessfullyCreatedNotebookCounts(ctx context.Context, dbNotebooks []DBNotebookInfo, namespace, gpuTypeKey string) (int, int, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	list, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}
	k8sNotebookNames := make(map[string]struct{}, len(list.Items))
	for _, item := range list.Items {
		k8sNotebookNames[item.GetName()] = struct{}{}
	}

	totalCPU, totalGPU := 0, 0
	for _, nb := range dbNotebooks {
		if checkNotebookFailed(nb.LatestEvent) {
			continue
		}
		_, exists := k8sNotebookNames[nb.Name]
		//orphaned notebook
		if !exists && nb.LatestEvent == constants.StatusNotebookApplied {
			continue
		}
		if nb.Type == "cpu" {
			totalCPU++
		} else {
			totalGPU++
		}
	}
	return totalCPU, totalGPU, nil
}

// CountEffectiveRunningNotebooks returns the number of running CPU and GPU notebooks
// considering both DB and k8s state.
// we are not checking notebook applied status because our worker can be inbetewen state of creating it
func (app *application) CountEffectiveRunningNotebooks(ctx context.Context, namespace string, gpuTypeKey string, dbNotebooks []DBNotebookInfo) (int, int, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	list, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}
	k8sMap := make(map[string]*unstructured.Unstructured)
	for _, item := range list.Items {
		k8sMap[item.GetName()] = &item
	}

	runningCPU, runningGPU := 0, 0
	for _, nb := range dbNotebooks {
		if checkNotebookFailed(nb.LatestEvent) {
			continue
		}

		k8sNotebook, exists := k8sMap[nb.Name]
		isRunning := false
		// if the notebook is applied and not in k8s, it means it's not running
		// orphaned notebook
		if nb.LatestEvent == constants.StatusNotebookApplied && !exists {
			continue
		}

		if exists {
			annotations, found, err := unstructured.NestedMap(k8sNotebook.Object, "metadata", "annotations")
			if err != nil {
				return 0, 0, err
			}
			if !found {
				annotations = map[string]any{}
			}
			isRunning = IsNotebookRunningFromAnnotations(annotations)
		} else {
			isRunning = true
		}
		if isRunning {
			if nb.Type == "cpu" {
				runningCPU++
			} else {
				runningGPU++
			}
		}
	}
	return runningCPU, runningGPU, nil
}
