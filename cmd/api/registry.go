package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"sandbox-backend-service/pkg/k8s"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ensureRegistrySecret is the single dispatch point for all registry-secret types.
// It reads app.registrySecret.SecretType and calls the appropriate implementation:
//
//	"ecr"              → ECR rotating-token flow (app.ecrClient.CreateOrUpdateSecret)
//	"private-registry" → Static username/password flow
//	"none" / anything else → no-op
func (app *application) ensureRegistrySecret(ctx context.Context, logger *slog.Logger, namespace string) error {
	switch app.registrySecret.SecretType {
	case "ecr":
		if app.ecrClient == nil {
			return fmt.Errorf("ECR client is nil but SECRET_TYPE is \"ecr\"")
		}
		return app.ecrClient.CreateOrUpdateSecret(ctx, logger, app.k8sClient, namespace)

	case "private-registry":
		return app.createStaticRegistrySecret(ctx, logger, namespace)

	default:
		// "none" or unset — do nothing
		return nil
	}
}

// createStaticRegistrySecret creates (or updates) a kubernetes.io/dockerconfigjson Secret
// in the given namespace using static username/password credentials from RegistrySecretConfig.
// Unlike the ECR path, there is no token rotation — credentials are long-lived.
func (app *application) createStaticRegistrySecret(ctx context.Context, logger *slog.Logger, namespace string) error {
	cfg := app.registrySecret
	return createStaticRegistrySecretForClient(ctx, logger, app.k8sClient, cfg, namespace)
}

// createStaticRegistrySecretForClient is a helper that takes an explicit k8sClient.
func createStaticRegistrySecretForClient(ctx context.Context, logger *slog.Logger, k8sClient *k8s.K8sClient, cfg RegistrySecretConfig, namespace string) error {
	dockerConfigJSON, err := createDockerConfigJSONWithUsernamePassword(cfg.URL, cfg.Username, cfg.Password)
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
		// Update existing secret data in-place
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

// createDockerConfigJSONWithUsernamePassword builds a .dockerconfigjson payload using an
// explicit username and password (static credentials, no auth-token rotation).
// Unlike the ECR helper (which hardcodes "AWS" as the username), this is fully generic.
func createDockerConfigJSONWithUsernamePassword(registryURL, username, password string) ([]byte, error) {
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			registryURL: map[string]string{
				"username": username,
				"password": password,
				"auth":     base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
			},
		},
	}
	configJSON, err := json.Marshal(dockerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal docker config: %w", err)
	}
	return configJSON, nil
}
