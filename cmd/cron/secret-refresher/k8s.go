package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sandbox-backend-service/pkg/k8s"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DockerConfig struct {
	Auths map[string]Auth `json:"auths"`
}

type Auth struct {
	Auth string `json:"auth"`
}

func (sr *secretRefresher) getECRAuthToken(ctx context.Context) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(sr.env.ECRRegion),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(sr.env.AWSAccessKeyID, sr.env.AWSSecretKey, "")),
	)
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	ecrClient := ecr.NewFromConfig(cfg)

	authToken, err := ecrClient.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", fmt.Errorf("failed to get ECR authorization token: %w", err)
	}

	if len(authToken.AuthorizationData) == 0 {
		return "", fmt.Errorf("no authorization data returned from ECR")
	}

	return *authToken.AuthorizationData[0].AuthorizationToken, nil
}

func (sr *secretRefresher) createSecret(ctx context.Context, k8sClient *k8s.K8sClient) error {
	ecrAuthToken, err := sr.getECRAuthToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get ECR auth token: %w", err)
	}

	decodedToken, err := base64.StdEncoding.DecodeString(ecrAuthToken)
	if err != nil {
		return fmt.Errorf("failed to decode ECR auth token: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString(decodedToken)

	dockerConfig := DockerConfig{
		Auths: map[string]Auth{
			sr.env.ECRRegistryURL: {
				Auth: auth,
			},
		},
	}

	dockerConfigJSON, err := json.Marshal(dockerConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal docker config: %w", err)
	}

	secretData := map[string][]byte{
		".dockerconfigjson": dockerConfigJSON,
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sr.env.SecretName,
			Namespace: sr.env.SecretNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: secretData,
	}

	existingSecret, err := k8sClient.Clientset.CoreV1().Secrets(sr.env.SecretNamespace).Get(ctx, sr.env.SecretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			sr.logger.Info("Creating new secret", "secret_name", sr.env.SecretName, "ecr_registry", sr.env.ECRRegistryURL)
			_, createErr := k8sClient.Clientset.CoreV1().Secrets(sr.env.SecretNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("failed to create secret: %w", createErr)
			}
			sr.logger.Info("Secret created successfully", "secret_name", sr.env.SecretName)
			return nil
		}
		return fmt.Errorf("failed to get secret: %w", err)
	}

	sr.logger.Info("Updating existing secret", "secret_name", sr.env.SecretName, "ecr_registry", sr.env.ECRRegistryURL)
	existingSecret.Data = secretData
	_, updateErr := k8sClient.Clientset.CoreV1().Secrets(sr.env.SecretNamespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
	if updateErr != nil {
		return fmt.Errorf("failed to update secret: %w", updateErr)
	}
	sr.logger.Info("Secret updated successfully", "secret_name", sr.env.SecretName)

	return nil
}
