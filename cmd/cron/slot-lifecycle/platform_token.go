package main

import (
	"context"
	"strings"

	"sandbox-backend-service/pkg/k8s"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const platformTokenSecretNameSuffix = "-plt-token"

var platformTokenSecretGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "secrets",
}

func platformTokenSecretName(notebookName string) string {
	return strings.TrimSpace(notebookName) + platformTokenSecretNameSuffix
}

func deletePlatformTokenSecret(ctx context.Context, k8sClient *k8s.K8sClient, namespace, notebookName string) error {
	if k8sClient == nil || namespace == "" || notebookName == "" {
		return nil
	}
	err := k8sClient.Dynamic.Resource(platformTokenSecretGVR).Namespace(namespace).Delete(ctx, platformTokenSecretName(notebookName), metav1.DeleteOptions{})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return err
}
