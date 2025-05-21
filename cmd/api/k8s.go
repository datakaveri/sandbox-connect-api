package main

import (
	"context"
	"sandbox-backend-service/pkg/k8s"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func addStoppedAnnotationToNotebook(k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	notebook, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
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

	_, err = k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(context.Background(), notebook, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

func removeStoppedAnnotationFromNotebook(k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	notebook, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
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

	_, err = k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(context.Background(), notebook, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

func deleteNotebookFromK8s(k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	_, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Delete(context.Background(), notebookName, deleteOptions)
	if err != nil {
		return err
	}

	return nil
}

func getNotebooksJSON(k8sClient *k8s.K8sClient, namespace string) (map[string]any, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	list, err := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	notebooks := make(map[string]any)

	for _, item := range list.Items {
		notebooks[item.GetName()] = item.Object
	}

	return notebooks, nil
}
func deletePVCFromK8s(k8sClient *k8s.K8sClient, namespace, pvcName string) error {
	pvcGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}
	_, err := k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	deletePolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	err = k8sClient.Dynamic.Resource(pvcGVR).Namespace(namespace).Delete(context.Background(), pvcName, deleteOptions)
	if err != nil {
		return err
	}

	return nil
}
func getNotebookJSON(k8sClient *k8s.K8sClient, namespace, notebookName string) (*unstructured.Unstructured, error) {
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	spec, errr := k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(context.Background(), notebookName, metav1.GetOptions{})
	return spec, errr
}
