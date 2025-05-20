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
	CORS_ORIGINS   string `env:"CORS_ORIGINS" envDefault:""`
	API_KEY        string `env:"API_KEY,required"`
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
type CheckExistsRequest struct {
	Name      string `json:"name" validate:"required"`
	Namespace string `json:"namespace" validate:"required"`
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

type NotebookDetails struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	StorageSize   string   `json:"storage_size"`
	PVCName       string   `json:"pvc_name"`
	CPURequest    float64  `json:"cpu_request"`
	CPULimit      float64  `json:"cpu_limit"`
	MemoryRequest string   `json:"memory_request"`
	MemoryLimit   string   `json:"memory_limit"`
	GPUType       *string  `json:"gpu_type,omitempty"`
	GPUCount      *int     `json:"gpu_count,omitempty"`
	TemplateName  *string  `json:"template_name,omitempty"`
	Events        []string `json:"events"`
}

type StopNotebookRequest struct {
	Name      string `json:"name" validate:"required"`
	Namespace string `json:"namespace" validate:"required"`
}
type StartNotebookRequest struct {
	Name      string `json:"name" validate:"required"`
	Namespace string `json:"namespace" validate:"required"`
}
type DeleteNotebookRequest struct {
	Name      string `json:"name" validate:"required"`
	Namespace string `json:"namespace" validate:"required"`
}

type ListNotebooksRequest struct {
	Namespace string `json:"namespace" validate:"required"`
}

type ListNotebooksResponse struct {
	Successful []NotebookDetails `json:"successful"`
	Stopped    []NotebookDetails `json:"stopped"`
	Pending    []NotebookDetails `json:"pending"`
	Failed     []NotebookDetails `json:"failed"`
	Orphaned   []NotebookDetails `json:"orphaned"`
}
