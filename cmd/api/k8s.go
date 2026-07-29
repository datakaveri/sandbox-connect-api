package main

import (
	"context"
	"fmt"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	sandboxk8s "sandbox-backend-service/pkg/k8s"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	profileWorkspacePVCName      = "workspace"
	profileWorkspaceStorageClass = "ceph-filesystem"
	profileWorkspaceStorageSize  = "50Gi"
	profileWorkspaceAccessMode   = "ReadWriteMany"
	profileWorkspaceVolumeMode   = "Filesystem"
)

var profileWorkspacePVCGVR = schema.GroupVersionResource{
	Group: "", Version: "v1", Resource: "persistentvolumeclaims",
}

// waitForNamespace polls until the given Kubernetes namespace exists.
// Kubeflow creates the namespace asynchronously after receiving a Profile CR,
// so we must wait before placing resources (e.g. Secrets) into it.
// Polls every 2 seconds; times out after 60 seconds.
func (app *application) waitForNamespace(ctx context.Context, logger *slog.Logger, namespace string) error {
	nsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}

	const (
		pollInterval = 2 * time.Second
		pollTimeout  = 60 * time.Second
	)

	deadline := time.Now().Add(pollTimeout)
	for {
		_, err := app.k8sClient.Dynamic.Resource(nsGVR).Get(ctx, namespace, metav1.GetOptions{})
		if err == nil {
			logger.Info("namespace is ready", "namespace", namespace)
			return nil
		}
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("error checking namespace %q: %w", namespace, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for namespace %q to be created by Kubeflow", pollTimeout, namespace)
		}
		logger.Info("waiting for namespace to be created by Kubeflow", "namespace", namespace)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// ensureProfileWorkspacePVC creates the profile-scoped CephFS claim used by
// the no-code sharing flow. The claim remains writable for the external copy
// pod; notebook templates mount it read-only.
func (app *application) ensureProfileWorkspacePVC(ctx context.Context, logger *slog.Logger, namespace string) error {
	if !app.env.WorkspaceEnabled {
		return nil
	}
	if err := app.waitForNamespace(ctx, logger, namespace); err != nil {
		return err
	}

	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      profileWorkspacePVCName,
			"namespace": namespace,
			"labels": map[string]any{
				"sandbox-connect.tgdex.io/profile-workspace": "true",
			},
		},
		"spec": map[string]any{
			"storageClassName": profileWorkspaceStorageClass,
			"accessModes":      []any{profileWorkspaceAccessMode},
			"volumeMode":       profileWorkspaceVolumeMode,
			"resources": map[string]any{
				"requests": map[string]any{
					"storage": profileWorkspaceStorageSize,
				},
			},
		},
	}}

	workspaceLogger := logger.With("operation", "ensureProfileWorkspacePVC", "namespace", namespace, "pvc", profileWorkspacePVCName)
	return WithK8sRetry(ctx, workspaceLogger, func() (constants.ShouldContinue, error) {
		_, err := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if err == nil {
			workspaceLogger.Info("profile workspace PVC created")
			return constants.RetryStop, nil
		}
		if !k8serrors.IsAlreadyExists(err) {
			return constants.RetryContinue, err
		}

		existing, getErr := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace(namespace).Get(ctx, profileWorkspacePVCName, metav1.GetOptions{})
		if getErr != nil {
			return constants.RetryContinue, getErr
		}
		if validateErr := validateProfileWorkspacePVC(existing); validateErr != nil {
			return constants.RetryStop, validateErr
		}
		workspaceLogger.Info("compatible profile workspace PVC already exists")
		return constants.RetryStop, nil
	})
}

func validateProfileWorkspacePVC(pvc *unstructured.Unstructured) error {
	storageClass, _, err := unstructured.NestedString(pvc.Object, "spec", "storageClassName")
	if err != nil || storageClass != profileWorkspaceStorageClass {
		return fmt.Errorf("workspace PVC storageClassName is %q, expected %q", storageClass, profileWorkspaceStorageClass)
	}
	accessModes, _, err := unstructured.NestedStringSlice(pvc.Object, "spec", "accessModes")
	if err != nil {
		return fmt.Errorf("read workspace PVC accessModes: %w", err)
	}
	hasRWX := false
	for _, mode := range accessModes {
		if mode == profileWorkspaceAccessMode {
			hasRWX = true
			break
		}
	}
	if !hasRWX {
		return fmt.Errorf("workspace PVC must include access mode %s", profileWorkspaceAccessMode)
	}
	volumeMode, found, err := unstructured.NestedString(pvc.Object, "spec", "volumeMode")
	if err != nil {
		return fmt.Errorf("read workspace PVC volumeMode: %w", err)
	}
	if !found || volumeMode == "" {
		volumeMode = "Filesystem"
	}
	if volumeMode != profileWorkspaceVolumeMode {
		return fmt.Errorf("workspace PVC volumeMode is %q, expected %q", volumeMode, profileWorkspaceVolumeMode)
	}
	storage, _, err := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	if err != nil {
		return fmt.Errorf("read workspace PVC storage request: %w", err)
	}
	actualSize, err := resource.ParseQuantity(storage)
	if err != nil {
		return fmt.Errorf("parse workspace PVC storage request %q: %w", storage, err)
	}
	requiredSize := resource.MustParse(profileWorkspaceStorageSize)
	if actualSize.Cmp(requiredSize) < 0 {
		return fmt.Errorf("workspace PVC storage request is %s, expected at least %s", actualSize.String(), requiredSize.String())
	}
	return nil
}
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

func (app *application) deleteManagedPVCsFromK8s(ctx context.Context, namespace, notebookName string) error {
	return sandboxk8s.DeleteManagedNotebookPVCs(ctx, app.k8sClient.Dynamic, namespace, notebookName)
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
func (app *application) getSuccessfullyCreatedNotebookCounts(ctx context.Context, dbNotebooks []DBNotebookInfo, namespace string) (int, int, error) {
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
func (app *application) CountEffectiveRunningNotebooks(ctx context.Context, dbNotebooks []DBNotebookInfo, namespace string) (int, int, error) {
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
