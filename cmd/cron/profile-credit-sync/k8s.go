package main

import (
	"context"
	"fmt"
	"sandbox-backend-service/pkg/constants"
	"strings"
	"time"

	"github.com/google/uuid"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	notebookGVR = schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}

	profileGVR = schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}
)

// listNotebooksInNamespace lists all notebooks in the specified namespace
func (ps *profileSync) listNotebooksInNamespace(ctx context.Context, namespace string) ([]unstructured.Unstructured, error) {
	ps.logger.Info("listing notebooks in namespace", "namespace", namespace)

	var notebooks []unstructured.Unstructured
	err := WithK8sRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		list, err := ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				ps.logger.Warn("no notebooks found or namespace does not exist", "namespace", namespace, "error", err)
				return constants.RetryStop, nil
			}
			return constants.RetryContinue, err
		}
		notebooks = list.Items
		return constants.RetryStop, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list notebooks in namespace %s: %w", namespace, err)
	}

	ps.logger.Info("found notebooks in namespace", "namespace", namespace, "count", len(notebooks))
	return notebooks, nil
}

// getGPUResourceKeys returns a list of GPU resource keys from env/config, defaulting to ["nvidia.com/gpu"]
func getGPUResourceKeys(keys string) []string {
	if keys == "" {
		return []string{"nvidia.com/gpu"}
	}

	var out []string
	for _, k := range strings.Split(keys, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			out = append(out, k)
		}
	}

	if len(out) == 0 {
		return []string{"nvidia.com/gpu"}
	}
	return out
}

// hasGPUResource returns true if any container in the notebook spec has a GPU resource key in limits or requests
func hasGPUResource(notebook *unstructured.Unstructured, gpuKeys []string) bool {
	containers, found, err := unstructured.NestedSlice(notebook.Object, "spec", "template", "spec", "containers")
	if !found || err != nil {
		return false
	}

	for _, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		for _, resType := range []string{"limits", "requests"} {
			resources, found, _ := unstructured.NestedMap(container, "resources", resType)
			if !found {
				continue
			}

			for _, gpuKey := range gpuKeys {
				if _, ok := resources[gpuKey]; ok {
					return true
				}
			}
		}
	}
	return false
}

func isNotebookAlreadyStopped(notebook *unstructured.Unstructured) bool {
	annotations, found, err := unstructured.NestedMap(notebook.Object, "metadata", "annotations")
	if err != nil || !found {
		return false
	}

	if v, ok := annotations["kubeflow-resource-stopped"]; ok && v != "" {
		return true
	}
	return false
}

func (ps *profileSync) stopNotebook(ctx context.Context, notebook *unstructured.Unstructured, stopTime string) error {
	namespace := notebook.GetNamespace()
	notebookName := notebook.GetName()
	logger := ps.logger.With("namespace", namespace, "notebook", notebookName)

	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		// Check if already stopped
		if isNotebookAlreadyStopped(notebook) {
			logger.Debug("notebook already stopped, skipping", "notebook", notebookName)
			return constants.RetryStop, nil
		}

		// Get existing annotations or create new map
		annotations, found, err := unstructured.NestedMap(notebook.Object, "metadata", "annotations")
		if err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to get annotations: %w", err)
		}

		if !found {
			annotations = make(map[string]interface{})
		}

		// Add the stopped annotation
		annotations["kubeflow-resource-stopped"] = stopTime

		// Set the updated annotations
		if err := unstructured.SetNestedMap(notebook.Object, annotations, "metadata", "annotations"); err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to set annotations: %w", err)
		}

		// Update the notebook
		_, err = ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Update(ctx, notebook, metav1.UpdateOptions{})
		if err != nil {
			// If update fails due to resource version conflict, get latest and retry
			if k8serrors.IsConflict(err) {
				logger.Debug("resource version conflict, getting latest notebook", "notebook", notebookName)
				latestNotebook, getErr := ps.dynamicClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
				if getErr != nil {
					if k8serrors.IsNotFound(getErr) {
						logger.Debug("notebook not found during retry, skipping", "notebook", notebookName)
						return constants.RetryStop, nil
					}
					return constants.RetryContinue, fmt.Errorf("failed to get notebook during retry: %w", getErr)
				}

				// Update the notebook reference for the next retry
				*notebook = *latestNotebook
				return constants.RetryContinue, fmt.Errorf("resource version conflict, retrying: %w", err)
			}
			return constants.RetryContinue, fmt.Errorf("failed to update notebook: %w", err)
		}

		return constants.RetryStop, nil
	})
}

// stopAllGPUNotebooksInNamespace stops all GPU notebooks in the specified namespace
func (ps *profileSync) stopAllGPUNotebooksInNamespace(ctx context.Context, profile *Profile) error {
	logger := ps.logger.With("namespace", profile.UserID, "user_id", profile.UserID)
	logger.Info("stopping all GPU notebooks in namespace")

	notebooks, err := ps.listNotebooksInNamespace(ctx, profile.UserID)
	if err != nil {
		return fmt.Errorf("failed to list notebooks: %w", err)
	}

	if len(notebooks) == 0 {
		logger.Info("no notebooks found to stop")
		return nil
	}

	stopTime := time.Now().UTC().Format(time.RFC3339)
	var stoppedNotebookNames []string
	var failedNotebookNames []string
	gpuKeys := getGPUResourceKeys(ps.config.GPUResourceKeys)

	for _, notebook := range notebooks {
		notebookName := notebook.GetName()

		// Skip non-GPU notebooks
		if !hasGPUResource(&notebook, gpuKeys) {
			logger.Debug("skipping non-GPU notebook", "notebook", notebookName)
			continue
		}

		// Stop the notebook
		if err := ps.stopNotebook(ctx, &notebook, stopTime); err != nil {
			logger.Error("failed to stop notebook", "notebook", notebookName, "error", err)
			failedNotebookNames = append(failedNotebookNames, notebookName)
		} else {
			logger.Info("stopped notebook", "notebook", notebookName)
			stoppedNotebookNames = append(stoppedNotebookNames, notebookName)
		}
	}

	logger.Info("notebook stopping complete (it will also count which are already stopped)",
		"total_notebooks", len(notebooks),
		"gpu_notebooks_stopped", len(stoppedNotebookNames),
		"failed_notebooks", len(failedNotebookNames),
		"stopped_notebooks", stoppedNotebookNames,
		"failed_notebooks_list", failedNotebookNames)

	return nil
}

// getAllProfilesFromK8s retrieves all Kubeflow profiles from Kubernetes
func (ps *profileSync) getAllProfilesFromK8s() ([]KubeflowProfile, error) {
	ps.logger.Info("retrieving all profiles from Kubernetes")

	var profileList *unstructured.UnstructuredList
	err := WithK8sRetry(ps.rootCtx, ps.logger, func() (constants.ShouldContinue, error) {
		var err error
		profileList, err = ps.dynamicClient.Dynamic.Resource(profileGVR).List(ps.rootCtx, metav1.ListOptions{})
		if err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to list profiles: %w", err)
		}
		return constants.RetryStop, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to retrieve profiles from K8s: %w", err)
	}

	profiles := make([]KubeflowProfile, 0, len(profileList.Items))

	for _, item := range profileList.Items {
		profile, err := ps.parseKubeflowProfile(item)
		if err != nil {
			ps.logger.Warn("skipping invalid profile", "profile_name", item.GetName(), "error", err)
			continue
		}
		profiles = append(profiles, profile)
	}

	ps.logger.Info("retrieved profiles from Kubernetes", "count", len(profiles))
	return profiles, nil
}

// parseKubeflowProfile parses a Kubernetes profile object into a KubeflowProfile struct
func (ps *profileSync) parseKubeflowProfile(item unstructured.Unstructured) (KubeflowProfile, error) {
	userId := item.GetName()

	// Validate that userId is a valid UUID
	if _, err := uuid.Parse(userId); err != nil {
		return KubeflowProfile{}, fmt.Errorf("invalid profile ID - must be UUID: %w", err)
	}

	// Extract owner email
	ownerEmail, found, err := unstructured.NestedString(item.Object, "spec", "owner", "name")
	if err != nil {
		return KubeflowProfile{}, fmt.Errorf("failed to extract owner email: %w", err)
	}
	if !found || ownerEmail == "" {
		return KubeflowProfile{}, fmt.Errorf("owner email not found or empty")
	}

	// Extract creation timestamp
	createdAt, err := getCreationTimestampFromUnstructured(item.Object)
	if err != nil {
		ps.logger.Warn("could not extract creationTimestamp from profile", "user_id", userId, "error", err)
		createdAt = time.Time{} // Use zero time as fallback
	}

	return KubeflowProfile{
		UserID:    userId,
		Email:     ownerEmail,
		CreatedAt: createdAt,
	}, nil
}
