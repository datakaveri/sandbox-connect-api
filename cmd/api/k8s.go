package main

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func (app *application) deleteNotebookFromK8s(ctx context.Context, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(ctx, notebookName, deleteOptions)
	if err != nil {
		return err
	}

	return nil
}

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

	return notebooks, nil
}

func (app *application) deletePVCFromK8s(ctx context.Context, namespace, pvcName string) error {
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}

	_, err := app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = app.k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(ctx, pvcName, deleteOptions)
	if err != nil {
		return err
	}

	return nil
}

func (app *application) getNotebookJSON(ctx context.Context, namespace, notebookName string) (*unstructured.Unstructured, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	spec, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
	return spec, err
}

func (app *application) createKubeflowProfile(ctx context.Context, userId, email string) error {
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

	_, err := app.k8sClient.Dynamic.Resource(profileGVR).Create(ctx, profile, metav1.CreateOptions{})
	return err
}
