package main

import (
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
)

type ApiEnv struct {
	Address          string `env:"API_ADDRESS,required"`
	KubeConfigPath   string `env:"API_KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode   string `env:"API_KUBE_CONFIG_MODE" envDefault:"cluster"`
	POSTGRES_URL     string `env:"API_POSTGRES_URL,required"`
	KeycloakURL      string `env:"API_KEYCLOAK_URL,required"`
	KeycloakRealm    string `env:"API_KEYCLOAK_REALM,required"`
	KeycloakClientID string `env:"API_KEYCLOAK_CLIENT_ID,required"`
	CORS_ORIGINS     string `env:"API_CORS_ORIGINS" envDefault:""`
	RateLimit        int    `env:"API_RATE_LIMIT" envDefault:"60"`
	RateBurst        int    `env:"API_RATE_BURST" envDefault:"10"`
	NotebookConfig   NotebookConfig
}

type application struct {
	env         ApiEnv
	k8sClient   *k8s.K8sClient
	pgPool      *db.PgPool
	rateLimiter *IPRateLimiter
}
type Resource struct {
	Request float64 `json:"request" validate:"required,gt=0.1"`
	Limit   float64 `json:"limit" validate:"required,gt=0.1"`
}

type GPUResource struct {
	Type  string `json:"type" validate:"omitempty"`
	Limit int    `json:"limit" validate:"omitempty"`
}

type CheckStatusRequest struct {
	Id int64 `json:"id" validate:"required"`
}
type NotebookConfig struct {
	StorageSize              string `env:"API_DEFAULT_STORAGE_SIZE,required"`
	CPURequest               string `env:"API_DEFAULT_CPU_REQUEST,required"`
	CPULimit                 string `env:"API_DEFAULT_CPU_LIMIT,required"`
	MemoryRequest            string `env:"API_DEFAULT_MEMORY_REQUEST,required"`
	MemoryLimit              string `env:"API_DEFAULT_MEMORY_LIMIT,required"`
	GPUType                  string `env:"API_DEFAULT_GPU_TYPE,required"`
	GPULimit                 string `env:"API_DEFAULT_GPU_LIMIT,required"`
	KubeFlowURL              string `env:"API_KUBEFLOW_URL,required"`
	DefaultNotebookListLimit int    `env:"API_NOTEBOOK_LIST_LIMIT" envDefault:"10"`
}

type NotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
	Type string `json:"type" validate:"required"`
}

type NotebookStatus struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Namespace     string             `json:"namespace"`
	StorageSize   string             `json:"storageSize"`
	PVCName       string             `json:"pvcName"`
	CPURequest    float64            `json:"cpuRequest"`
	CPULimit      float64            `json:"cpuLimit"`
	MemoryRequest string             `json:"memoryRequest"`
	MemoryLimit   string             `json:"memoryLimit"`
	GPUType       *string            `json:"gpuType,omitempty"`
	GPUCount      *int               `json:"gpuCount,omitempty"`
	TemplateName  *string            `json:"templateName,omitempty"`
	Events        []constants.Events `json:"events"`
	Status        NotebookState      `json:"status"`
	URL           string             `json:"notebookUrl,omitempty"`
}

type StopNotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
}
type StartNotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
}
type DeleteNotebookRequest struct {
	Name string `json:"name" validate:"gt=3,required"`
}

type NotebookState string

const (
	NotebookStatePending  NotebookState = "pending"
	NotebookStateRunning  NotebookState = "running"
	NotebookStateStopped  NotebookState = "stopped"
	NotebookStateFailed   NotebookState = "failed"
	NotebookStateOrphaned NotebookState = "orphaned"
)

type CreateProfileRequest struct {
	UserID string `json:"userId" validate:"required,uuid"`
	Email  string `json:"email" validate:"required,email"`
}
