package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sandbox-backend-service/pkg/k8s"
)

type DockerConfig struct {
	Auths map[string]DockerAuthEntry `json:"auths"`
}

type DockerAuthEntry struct {
	Auth string `json:"auth"`
}

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

	authToken := string(authTokenBytes)
	proxyEndpoint := *authData.ProxyEndpoint

	return authToken, proxyEndpoint, nil
}

func (e *ECRClient) CreateOrUpdateSecret(ctx context.Context, logger *slog.Logger, k8sClient *k8s.K8sClient, namespace string) error {
	authToken, proxyEndpoint, err := e.GetAuthorizationToken(ctx)
	if err != nil {
		logger.Error("failed to get ECR authorization token", "error", err)
		return err
	}

	registryURL := proxyEndpoint
	if e.Config.ECRRegistryURL != "" {
		registryURL = e.Config.ECRRegistryURL
	}

	dockerConfig := DockerConfig{
		Auths: map[string]DockerAuthEntry{
			registryURL: {
				Auth: authToken,
			},
		},
	}

	dockerConfigJSON, err := json.Marshal(dockerConfig)
	if err != nil {
		logger.Error("failed to marshal docker config", "error", err)
		return fmt.Errorf("failed to marshal docker config: %w", err)
	}

	dockerConfigJSONBase64 := base64.StdEncoding.EncodeToString(dockerConfigJSON)

	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	secret := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      e.Config.SecretName,
				"namespace": namespace,
			},
			"type": "kubernetes.io/dockerconfigjson",
			"data": map[string]any{
				".dockerconfigjson": dockerConfigJSONBase64,
			},
		},
	}

	existingSecret, err := k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, e.Config.SecretName, metav1.GetOptions{})
	if err == nil {
		existingSecret.Object["data"] = map[string]any{
			".dockerconfigjson": dockerConfigJSONBase64,
		}
		_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
		if err != nil {
			logger.Error("failed to update ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
			return fmt.Errorf("failed to update ECR secret: %w", err)
		}
		logger.Info("successfully updated ECR secret", "namespace", namespace, "secret_name", e.Config.SecretName)
	} else {
		_, err = k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			logger.Error("failed to create ECR secret", "error", err, "namespace", namespace, "secret_name", e.Config.SecretName)
			return fmt.Errorf("failed to create ECR secret: %w", err)
		}
		logger.Info("successfully created ECR secret", "namespace", namespace, "secret_name", e.Config.SecretName)
	}

	return nil
}
