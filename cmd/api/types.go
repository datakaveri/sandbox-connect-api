package main

import (
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
)

type ApiEnv struct {
	Address        string `env:"ADDRESS,required"`
	POSTGRES_URL   string `env:"POSTGRES_URL,required"`
	KubeConfigPath string `env:"KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode string `env:"KUBE_CONFIG_MODE" envDefault:"cluster"`
}

type application struct {
	env       ApiEnv
	k8sClient *k8s.K8sClient
	pgPool    *db.PgPool
}
type Resource struct {
	Request float64 `json:"request" validate:"required,gt=0.1"`
	Limit   float64 `json:"limit" validate:"required,gt=0.1"`
}

type GPUResource struct {
	Type  string `json:"type" validate:"omitempty"`
	Limit int    `json:"limit" validate:"omitempty"`
}
type NotebookRequest struct {
	Name            string      `json:"name" validate:"required"`
	Namespace       string      `json:"namespace" validate:"required"`
	StorageSizeInGi float64     `json:"storageSizeInGi" validate:"required"`
	PVCName         string      `json:"PVCName" validate:"required"`
	CPU             Resource    `json:"cpu" validate:"required"`
	MemoryInGi      Resource    `json:"memoryInGi" validate:"required"`
	GPU             GPUResource `json:"gpu" validate:"omitempty"`
	TemplateName    string      `json:"templateName" validate:"omitempty"`
}
