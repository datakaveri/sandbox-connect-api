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
		Version:  "v1alpha1",
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
