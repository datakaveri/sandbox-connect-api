package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"sandbox-backend-service/pkg/k8s"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func (e *ECRClient) GetAuthorizationToken(ctx context.Context) (string, string, error) {
	input := &ecr.GetAuthorizationTokenInput{}

	output, err := e.ECRClient.GetAuthorizationToken(ctx, input)
	if err != nil {
		return "", "", fmt.Errorf("failed to get ECR authorization token: %w", err)
	}

	if len(output.AuthorizationData) == 0 {
		return "", "", fmt.Errorf("no authorization data returned from ECR")
	}

	authData := output.AuthorizationData[0]
	authTokenBytes, err := base64.StdEncoding.DecodeString(*authData.AuthorizationToken)
	if err != nil {
		return "", "", fmt.Errorf("failed to decode ECR auth token: %w", err)
	}

	// The decoded token is in format "AWS:password"
	// We need to split it to extract just the password
	authToken := string(authTokenBytes)
	parts := strings.SplitN(authToken, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid auth token format, expected 'username:password'")
	}

	password := parts[1]
	proxyEndpoint := *authData.ProxyEndpoint

	return password, proxyEndpoint, nil
}

func (e *ECRClient) CreateOrUpdateSecret(ctx context.Context, logger *slog.Logger, k8sClient *k8s.K8sClient, namespace string) error {
	ecrAuthToken, proxyEndpoint, err := e.GetAuthorizationToken(ctx)
	if err != nil {
		logger.Error("failed to get ECR authorization token", "error", err)
		return err
	}

	registryURL := proxyEndpoint
	if e.Config.ECRRegistryURL != "" {
		registryURL = e.Config.ECRRegistryURL
	}

	logger.Debug("Creating ECR docker-registry secret", "registry_url", registryURL, "auth_token_length", len(ecrAuthToken))

	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	dockerConfigJSON := createDockerConfigJSON(registryURL, ecrAuthToken)

	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      e.Config.SecretName,
				"namespace": namespace,
			},
			"type": "kubernetes.io/dockerconfigjson",
			"data": map[string]interface{}{
				".dockerconfigjson": dockerConfigJSON,
			},
		},
	}

	existingSecret, err := k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, e.Config.SecretName, metav1.GetOptions{})
	if err == nil {
		existingSecret.Object["data"] = map[string]interface{}{
			".dockerconfigjson": dockerConfigJSON,
		}
		_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
		if err != nil {
			logger.Error("failed to update ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
			return fmt.Errorf("failed to update ECR secret: %w", err)
		}
		logger.Info("successfully updated ECR secret", "namespace", namespace, "secret_name", e.Config.SecretName)
		return nil
	}

	_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		logger.Error("failed to create ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
		return fmt.Errorf("failed to create ECR secret: %w", err)
	}

	logger.Info("successfully created ECR secret", "namespace", namespace, "secret_name", e.Config.SecretName)
	return nil
}

func createDockerConfigJSON(registryURL, password string) []byte {
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			registryURL: map[string]string{
				"username": "AWS",
				"password": password,
				"auth":     base64.StdEncoding.EncodeToString([]byte("AWS:" + password)),
			},
		},
	}
	configJSON, _ := json.Marshal(dockerConfig)
	return configJSON
}
