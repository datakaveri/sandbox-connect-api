package main

import (
	"log/slog"
)

type Env struct {
	SecretName      string `env:"SECRET_REFRESHER_SECRET_NAME,required"`
	SecretNamespace string `env:"SECRET_REFRESHER_SECRET_NAMESPACE" envDefault:"default"`
	ECRRegion       string `env:"SECRET_REFRESHER_ECR_REGION,required"`
	ECRRegistryURL  string `env:"SECRET_REFRESHER_ECR_REGISTRY_URL,required"`
	AWSAccessKeyID  string `env:"SECRET_REFRESHER_AWS_ACCESS_KEY_ID,required"`
	AWSSecretKey    string `env:"SECRET_REFRESHER_AWS_SECRET_KEY,required"`
	K8sConfigMode   string `env:"SECRET_REFRESHER_K8S_CONFIG_MODE" envDefault:"cluster"`
	K8sConfigPath   string `env:"SECRET_REFRESHER_K8S_CONFIG_PATH" envDefault:""`
}

type secretRefresher struct {
	env    Env
	logger *slog.Logger
}
