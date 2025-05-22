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
	NotebookConfig NotebookConfig
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

type CheckStatusRequest struct {
	Id int64 `json:"id" validate:"required"`
}
type NotebookConfig struct {
	UserID        string `env:"DEFAULT_USER_ID"`
	Namespace     string `env:"DEFAULT_NAMESPACE,required"`
	StorageSize   string `env:"DEFAULT_STORAGE_SIZE,required"`
	CPURequest    string `env:"DEFAULT_CPU_REQUEST,required"`
	CPULimit      string `env:"DEFAULT_CPU_LIMIT,required"`
	MemoryRequest string `env:"DEFAULT_MEMORY_REQUEST,required"`
	MemoryLimit   string `env:"DEFAULT_MEMORY_LIMIT,required"`
	GPUType       string `env:"DEFAULT_GPU_TYPE,required"`
	GPULimit      string `env:"DEFAULT_GPU_LIMIT,required"`
	KubeFlowURL   string `env:"KUBEFLOW_URL,required"`
}

type NotebookRequest struct {
	Name string `json:"name" validate:"required"`
	Type string `json:"type" validate:"required"`
}

type NotebookDetails struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	StorageSize   string   `json:"storageSize"`
	PVCName       string   `json:"pvcName"`
	CPURequest    float64  `json:"cpuRequest"`
	CPULimit      float64  `json:"cpuLimit"`
	MemoryRequest string   `json:"memoryRequest"`
	MemoryLimit   string   `json:"memoryLimit"`
	GPUType       *string  `json:"gpuType,omitempty"`
	GPUCount      *int     `json:"gpuCount,omitempty"`
	TemplateName  *string  `json:"templateName,omitempty"`
	Events        []string `json:"events"`
	URL           string   `json:"notebookUrl,omitempty"`
}

type NotebookStatus struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	StorageSize   string   `json:"storageSize"`
	PVCName       string   `json:"pvcName"`
	CPURequest    float64  `json:"cpuRequest"`
	CPULimit      float64  `json:"cpuLimit"`
	MemoryRequest string   `json:"memoryRequest"`
	MemoryLimit   string   `json:"memoryLimit"`
	GPUType       *string  `json:"gpuType,omitempty"`
	GPUCount      *int     `json:"gpuCount,omitempty"`
	TemplateName  *string  `json:"templateName,omitempty"`
	Events        []string `json:"events"`
	Status        string   `json:"status"`
	URL           string   `json:"notebookUrl,omitempty"`
}

type StopNotebookRequest struct {
	Name string `json:"name" validate:"required"`
}
type StartNotebookRequest struct {
	Name string `json:"name" validate:"required"`
}
type DeleteNotebookRequest struct {
	Name string `json:"name" validate:"required"`
}

type NotebookState string

const (
	NotebookStatePending  NotebookState = "pending"
	NotebookStateRunning  NotebookState = "running"
	NotebookStateStopped  NotebookState = "stopped"
	NotebookStateFailed   NotebookState = "failed"
	NotebookStateOrphaned NotebookState = "orphaned"
)

type ListNotebooksResponse struct {
	Successful []NotebookDetails `json:"successful"`
	Stopped    []NotebookDetails `json:"stopped"`
	Pending    []NotebookDetails `json:"pending"`
	Failed     []NotebookDetails `json:"failed"`
	Orphaned   []NotebookDetails `json:"orphaned"`
}

type CreateProfileRequest struct {
	UserID string `json:"userId" validate:"required,uuid"`
	Email  string `json:"email" validate:"required,email"`
}
