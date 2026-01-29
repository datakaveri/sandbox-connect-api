package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"sandbox-backend-service/pkg/k8s"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ECRTokenMetadata represents the metadata stored in the secret annotation
type ECRTokenMetadata struct {
	Expiration int64  `json:"expiration"`
	Version    string `json:"version"`
}

const (
	// Refresh token 1 hour before expiration
	RefreshBeforeExpiry = 1 * time.Hour
	// Annotation key for storing token metadata
	TokenMetadataAnnotation = "ecr-token-metadata"
)

func NewECRClient(ecrConfig ECRConfig) (*ECRClient, error) {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(ecrConfig.ECRRegion),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(ecrConfig.AWSAccessKeyID, ecrConfig.AWSSecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := ecr.NewFromConfig(cfg)

	return &ECRClient{
		ECRClient: client,
		Config:    ecrConfig,
	}, nil
}

func (e *ECRClient) GetAuthorizationToken(ctx context.Context) (string, string, int64, error) {
	input := &ecr.GetAuthorizationTokenInput{}

	output, err := e.ECRClient.GetAuthorizationToken(ctx, input)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to get ECR authorization token: %w", err)
	}

	if len(output.AuthorizationData) == 0 {
		return "", "", 0, fmt.Errorf("no authorization data returned from ECR")
	}

	authData := output.AuthorizationData[0]
	authTokenBytes, err := base64.StdEncoding.DecodeString(*authData.AuthorizationToken)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to decode ECR auth token: %w", err)
	}

	authToken := string(authTokenBytes)
	parts := strings.SplitN(authToken, ":", 2)
	if len(parts) != 2 {
		return "", "", 0, fmt.Errorf("invalid auth token format, expected 'username:password'")
	}

	password := parts[1]
	if authData.ProxyEndpoint == nil {
		return "", "", 0, fmt.Errorf("proxy endpoint is nil")
	}
	proxyEndpoint := *authData.ProxyEndpoint

	var expiresAt int64
	if authData.ExpiresAt != nil {
		expiresAt = authData.ExpiresAt.Unix()
	}

	return password, proxyEndpoint, expiresAt, nil
}

// shouldRefreshToken checks if the token needs to be refreshed
func shouldRefreshToken(expiresAt int64) bool {
	if expiresAt == 0 {
		return true
	}

	expirationTime := time.Unix(expiresAt, 0)
	refreshTime := expirationTime.Add(-RefreshBeforeExpiry)

	return time.Now().After(refreshTime)
}

// getTokenMetadataFromSecret extracts token metadata from secret annotations
func getTokenMetadataFromSecret(secret *unstructured.Unstructured) (*ECRTokenMetadata, error) {
	annotations, found, err := unstructured.NestedStringMap(secret.Object, "metadata", "annotations")
	if err != nil || !found {
		return nil, fmt.Errorf("no annotations found")
	}

	metadataStr, exists := annotations[TokenMetadataAnnotation]
	if !exists {
		return nil, fmt.Errorf("no token metadata annotation found")
	}

	var metadata ECRTokenMetadata
	if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token metadata: %w", err)
	}

	return &metadata, nil
}

func (e *ECRClient) CreateOrUpdateSecret(ctx context.Context, logger *slog.Logger, k8sClient *k8s.K8sClient, namespace string) error {
	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	existingSecret, err := k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, e.Config.SecretName, metav1.GetOptions{})
	if err == nil {
		metadata, err := getTokenMetadataFromSecret(existingSecret)
		if err == nil {
			if !shouldRefreshToken(metadata.Expiration) {
				expirationTime := time.Unix(metadata.Expiration, 0)
				timeUntilRefresh := expirationTime.Add(-RefreshBeforeExpiry).Sub(time.Now())
				logger.Debug("ECR token is still valid, skipping refresh",
					"namespace", namespace,
					"secret_name", e.Config.SecretName,
					"expires_at", expirationTime.Format(time.RFC3339),
					"time_until_refresh", timeUntilRefresh.Round(time.Minute).String(),
				)
				return nil
			}
			logger.Debug("ECR token needs refresh",
				"namespace", namespace,
				"secret_name", e.Config.SecretName,
				"current_expiration", time.Unix(metadata.Expiration, 0).Format(time.RFC3339),
			)
		} else {
			logger.Warn("Could not read token metadata, will refresh token",
				"namespace", namespace,
				"secret_name", e.Config.SecretName,
				"error", err,
			)
		}
	}

	ecrAuthToken, proxyEndpoint, expiresAt, err := e.GetAuthorizationToken(ctx)
	if err != nil {
		logger.Error("failed to get ECR authorization token", "error", err)
		return err
	}

	registryURL := proxyEndpoint
	if e.Config.ECRRegistryURL != "" {
		registryURL = e.Config.ECRRegistryURL
	}

	logger.Debug("Creating/Updating ECR docker-registry secret",
		"registry_url", registryURL,
		"auth_token_length", len(ecrAuthToken),
		"expires_at", time.Unix(expiresAt, 0).Format(time.RFC3339),
	)

	dockerConfigJSON, err := createDockerConfigJSON(registryURL, ecrAuthToken)
	if err != nil {
		return fmt.Errorf("failed to create docker config JSON: %w", err)
	}

	tokenMetadata := ECRTokenMetadata{
		Expiration: expiresAt,
		Version:    "1",
	}
	metadataJSON, err := json.Marshal(tokenMetadata)
	if err != nil {
		return fmt.Errorf("failed to marshal token metadata: %w", err)
	}

	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      e.Config.SecretName,
				"namespace": namespace,
				"annotations": map[string]string{
					TokenMetadataAnnotation: string(metadataJSON),
				},
			},
			"type": "kubernetes.io/dockerconfigjson",
			"data": map[string]interface{}{
				".dockerconfigjson": dockerConfigJSON,
			},
		},
	}

	if existingSecret != nil {
		existingSecret.Object["data"] = map[string]interface{}{
			".dockerconfigjson": dockerConfigJSON,
		}

		annotations, found, err := unstructured.NestedStringMap(existingSecret.Object, "metadata", "annotations")
		if err != nil || !found {
			annotations = make(map[string]string)
		}
		annotations[TokenMetadataAnnotation] = string(metadataJSON)
		unstructured.SetNestedStringMap(existingSecret.Object, annotations, "metadata", "annotations")

		_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
		if err != nil {
			logger.Error("failed to update ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
			return fmt.Errorf("failed to update ECR secret: %w", err)
		}
		logger.Debug("successfully updated ECR secret",
			"namespace", namespace,
			"secret_name", e.Config.SecretName,
			"expires_at", time.Unix(expiresAt, 0).Format(time.RFC3339),
		)
		return nil
	}

	_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		logger.Error("failed to create ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
		return fmt.Errorf("failed to create ECR secret: %w", err)
	}

	logger.Debug("successfully created ECR secret",
		"namespace", namespace,
		"secret_name", e.Config.SecretName,
		"expires_at", time.Unix(expiresAt, 0).Format(time.RFC3339),
	)
	return nil
}

func createDockerConfigJSON(registryURL, password string) ([]byte, error) {
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			registryURL: map[string]string{
				"username": "AWS",
				"password": password,
				"auth":     base64.StdEncoding.EncodeToString([]byte("AWS:" + password)),
			},
		},
	}
	configJSON, err := json.Marshal(dockerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal docker config: %w", err)
	}
	return configJSON, nil
}
