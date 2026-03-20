package main

import (
	"context"
	"fmt"
	"log/slog"

	"sandbox-backend-service/pkg/k8s"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// createStaticRegistrySecret creates (or updates) a kubernetes.io/dockerconfigjson Secret
// in the given namespace using static username/password credentials from RegistryConfig.
// Unlike the ECR path, there is no token rotation — CBR registry credentials are long-lived.
func (app *application) createStaticRegistrySecret(ctx context.Context, logger *slog.Logger, namespace string) error {
	cfg := app.registryConfig

	dockerConfigJSON, err := createDockerConfigJSONFromPassword(cfg.URL, cfg.Username, cfg.Password)
	if err != nil {
		return fmt.Errorf("failed to build docker config JSON: %w", err)
	}

	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	newSecret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      cfg.SecretName,
				"namespace": namespace,
			},
			"type": "kubernetes.io/dockerconfigjson",
			"data": map[string]interface{}{
				".dockerconfigjson": dockerConfigJSON,
			},
		},
	}

	existing, err := app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, cfg.SecretName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to check existing registry secret: %w", err)
	}

	if existing != nil && err == nil {
		// Update existing secret data in-place
		existing.Object["data"] = map[string]interface{}{
			".dockerconfigjson": dockerConfigJSON,
		}
		_, err = app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update registry secret: %w", err)
		}
		logger.Info("updated static registry secret", "namespace", namespace, "secret", cfg.SecretName)
		return nil
	}

	_, err = app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, newSecret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create registry secret: %w", err)
	}
	logger.Info("created static registry secret", "namespace", namespace, "secret", cfg.SecretName)
	return nil
}

// createStaticRegistrySecretForClient is a helper for use with an explicit k8sClient
// (useful if the caller has a different client reference).
func createStaticRegistrySecretForClient(ctx context.Context, logger *slog.Logger, k8sClient *k8s.K8sClient, cfg RegistryConfig, namespace string) error {
	dockerConfigJSON, err := createDockerConfigJSONFromPassword(cfg.URL, cfg.Username, cfg.Password)
	if err != nil {
		return fmt.Errorf("failed to build docker config JSON: %w", err)
	}

	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	newSecret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      cfg.SecretName,
				"namespace": namespace,
			},
			"type": "kubernetes.io/dockerconfigjson",
			"data": map[string]interface{}{
				".dockerconfigjson": dockerConfigJSON,
			},
		},
	}

	existing, err := k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, cfg.SecretName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to check existing registry secret: %w", err)
	}

	if existing != nil && err == nil {
		existing.Object["data"] = map[string]interface{}{
			".dockerconfigjson": dockerConfigJSON,
		}
		_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update registry secret: %w", err)
		}
		logger.Info("updated static registry secret", "namespace", namespace, "secret", cfg.SecretName)
		return nil
	}

	_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, newSecret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create registry secret: %w", err)
	}
	logger.Info("created static registry secret", "namespace", namespace, "secret", cfg.SecretName)
	return nil
}

// createDockerConfigJSONFromPassword builds a .dockerconfigjson payload from explicit
// username and password (static credentials, no auth-token rotation).
func createDockerConfigJSONFromPassword(registryURL, username, password string) ([]byte, error) {
	// Reuse the docker-config builder that already exists in ecr.go
	return createDockerConfigJSON(registryURL, password)
}
