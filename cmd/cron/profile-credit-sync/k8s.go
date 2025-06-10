package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var notebookGVR = schema.GroupVersionResource{
	Group:    "kubeflow.org",
	Version:  "v1beta1",
	Resource: "notebooks",
}

func (ps *profileSync) listNotebooksInNamespace(ctx context.Context, namespace string) ([]unstructured.Unstructured, error) {
	ps.logger.Info("listing notebooks in namespace", "namespace", namespace)

	var notebooks []unstructured.Unstructured
	err := WithK8sRetry(ctx, func() error {
		list, err := ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				ps.logger.Warn("no notebooks found or namespace does not exist", "namespace", namespace, "error", err)
				return nil
			}
			return err
		}
		notebooks = list.Items
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list notebooks in namespace %s: %w", namespace, err)
	}

	ps.logger.Info("found notebooks in namespace", "namespace", namespace, "count", len(notebooks))
	return notebooks, nil
}

func (ps *profileSync) stopAllNotebooksInNamespace(ctx context.Context, namespace string) error {
	ps.logger.Info("stopping all notebooks in namespace", "namespace", namespace)

	notebooks, err := ps.listNotebooksInNamespace(ctx, namespace)
	if err != nil {
		return err
	}

	if len(notebooks) == 0 {
		ps.logger.Info("no notebooks found to stop", "namespace", namespace)
		return nil
	}
	stopTime := time.Now().UTC().Format(time.RFC3339)
	var stopedNotebookName []string
	var failedNotebookName []string

	for _, notebook := range notebooks {
		notebookName := notebook.GetName()
		err := WithK8sRetry(ctx, func() error {
			latestNotebook, err := ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			annotations, found, err := unstructured.NestedMap(latestNotebook.Object, "metadata", "annotations")
			if err != nil {
				return err
			}

			if !found {
				annotations = make(map[string]interface{})
			}

			annotations["kubeflow-resource-stopped"] = stopTime

			if err := unstructured.SetNestedMap(latestNotebook.Object, annotations, "metadata", "annotations"); err != nil {
				return err
			}

			_, err = ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(ctx, latestNotebook, metav1.UpdateOptions{})
			return err
		})

		if err != nil {
			ps.logger.Error("failed to stop notebook", "namespace", namespace, "notebook", notebookName, "error", err)
			failedNotebookName = append(failedNotebookName, notebookName)
		} else {
			ps.logger.Info("stopped notebook", "namespace", namespace, "notebook", notebookName)
			stopedNotebookName = append(stopedNotebookName, notebookName)
		}
	}

	ps.logger.Info("notebook stopping complete",
		"namespace", namespace,
		"total", len(notebooks),
		"stopped", stopedNotebookName,
		"failed", failedNotebookName)

	return nil
}
func (ps *profileSync) getAllProfilesFromK8s() ([]KubeflowProfile, error) {
	profileGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}
	profileList := &unstructured.UnstructuredList{}
	err := WithK8sRetry(ps.rootCtx, func() error {
		var err error
		profileList, err = ps.dynamicClient.Dynamic.Resource(profileGVR).List(ps.rootCtx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("failed to list profiles: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	profiles := make([]KubeflowProfile, 0, len(profileList.Items))

	for _, item := range profileList.Items {
		userId := item.GetName()

		if _, err := uuid.Parse(userId); err != nil {
			ps.logger.Warn("skipping invalid profile ID - must be UUID",
				"profile_id", userId,
				"error", err)
			continue
		}

		ownerEmail, found, err := unstructured.NestedString(item.Object, "spec", "owner", "name")
		if err != nil || !found {
			ps.logger.Warn("could not extract owner email from profile",
				"user_id", userId,
				"error", err)
			continue
		}

		profiles = append(profiles, KubeflowProfile{
			UserID: userId,
			Email:  ownerEmail,
		})
	}

	ps.logger.Info("found profiles in Kubernetes", "count", len(profiles))
	return profiles, nil
}
