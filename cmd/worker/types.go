package main

import (
	"fmt"
	"log/slog"
	pathpkg "path"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/s3"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

type application struct {
	env               Env
	notebookTemplates map[string]*SandboxNotebookTemplate
	k8sClient         *k8s.K8sClient
	pgPool            *db.PgPool
	s3Client          *s3.S3Client
	logger            *slog.Logger
}

type Env struct {
	KubeConfigPath               string `env:"WORKER_KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode               string `env:"WORKER_KUBE_CONFIG_MODE" envDefault:"cluster"`
	CPUNotebookTemplatePath      string `env:"WORKER_CPU_NOTEBOOK_TEMPLATE_PATH,required"`
	GPUNotebookTemplatePath      string `env:"WORKER_GPU_NOTEBOOK_TEMPLATE_PATH,required"`
	POSTGRES_URL                 string `env:"WORKER_POSTGRES_URL,required"`
	MAX_CONCURRENT_WORKER        int    `env:"WORKER_MAX_CONCURRENT_WORKER,required"`
	WorkspaceEnabled             bool   `env:"WORKSPACE_ENABLED" envDefault:"false"`
	EvaluationWorkspaceEnabled   bool   `env:"WORKER_EVALUATION_WORKSPACE_ENABLED" envDefault:"false"`
	EvaluationWorkspacePVCName   string `env:"WORKER_EVALUATION_WORKSPACE_PVC_NAME" envDefault:"evaluation-workspace"`
	EvaluationWorkspaceMountPath string `env:"WORKER_EVALUATION_WORKSPACE_MOUNT_PATH" envDefault:"/home/jovyan/evaluation-workspace"`
	S3_ENDPOINT                  string `env:"WORKER_S3_ENDPOINT,required"`
	S3_REGION                    string `env:"WORKER_S3_REGION,required"`
	S3_ACCESS_KEY                string `env:"WORKER_S3_ACCESS_KEY,required"`
	S3_SECRET_KEY                string `env:"WORKER_S3_SECRET_KEY,required"`
	S3_TEMPLATE_BUCKET_NAME      string `env:"WORKER_S3_TEMPLATE_BUCKET_NAME,required"`
}

func (e Env) ValidateEvaluationWorkspace() error {
	if !e.EvaluationWorkspaceEnabled {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(e.EvaluationWorkspacePVCName); len(errs) > 0 {
		return fmt.Errorf("evaluation workspace PVC name is invalid: %s", strings.Join(errs, ", "))
	}
	if !pathpkg.IsAbs(e.EvaluationWorkspaceMountPath) || pathpkg.Clean(e.EvaluationWorkspaceMountPath) != e.EvaluationWorkspaceMountPath {
		return fmt.Errorf("evaluation workspace mount path must be a clean absolute path")
	}
	return nil
}

type Notebook struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Namespace          string  `json:"namespace"`
	StorageSize        string  `json:"storage_size"`
	PVCname            string  `json:"pvc_name"`
	CPURequest         float64 `json:"cpu_request"`
	CPULimit           float64 `json:"cpu_limit"`
	MemoryRequest      string  `json:"memory_request"`
	MemoryLimit        string  `json:"memory_limit"`
	GPUType            *string `json:"gpu_type"`
	GPURequest         *int    `json:"gpu_request"`
	GPULimit           *int    `json:"gpu_limit"`
	InstanceType       *string `json:"instance_type"`
	TemplateName       *string `json:"template_name"`
	ImageName          *string `json:"image_name"`
	FileURL            *string `json:"file_url"`
	GitURL             *string `json:"git_url"`
	GitTokenSecretName *string `json:"git_token_secret_name"`
	BookingID          *int64  `json:"booking_id"`
}
type worker struct {
	app               *application
	notebook          Notebook
	logger            *slog.Logger
	template          *SandboxNotebookTemplate
	omittedVolumes    map[string]struct{}
	resolvedPVCMounts []ResolvedPVCMount
}
