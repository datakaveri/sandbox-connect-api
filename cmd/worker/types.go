package main

import (
	"log/slog"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/s3"
)

type application struct {
	env       Env
	k8sClient *k8s.K8sClient
	pgPool    *db.PgPool
	s3Client  *s3.S3Client
	logger    *slog.Logger
}

type Env struct {
	KubeConfigPath          string `env:"WORKER_KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode          string `env:"WORKER_KUBE_CONFIG_MODE" envDefault:"cluster"`
	POSTGRES_URL            string `env:"WORKER_POSTGRES_URL,required"`
	MAX_CONCURRENT_WORKER   int    `env:"WORKER_MAX_CONCURRENT_WORKER,required"`
	S3_ENDPOINT             string `env:"WORKER_S3_ENDPOINT,required"`
	S3_REGION               string `env:"WORKER_S3_REGION,required"`
	S3_ACCESS_KEY           string `env:"WORKER_S3_ACCESS_KEY,required"`
	S3_SECRET_KEY           string `env:"WORKER_S3_SECRET_KEY,required"`
	S3_TEMPLATE_BUCKET_NAME string `env:"WORKER_S3_TEMPLATE_BUCKET_NAME,required"`
	STORAGE_CLASS_NAME      string `env:"WORKER_STORAGE_CLASS_NAME,required"`
	CPU_NOTEBOOK_IMAGE      string `env:"WORKER_CPU_NOTEBOOK_IMAGE,required"`
	GPU_NOTEBOOK_IMAGE      string `env:"WORKER_GPU_NOTEBOOK_IMAGE,required"`
	INIT_CONTAINER_IMAGE    string `env:"WORKER_INIT_CONTAINER_IMAGE,required"`
	// GPU_NODE_INSTANCE_TYPE is kept for backward compatibility. Prefer WORKER_GPU_NODE_INSTANCE_TYPES.
	GPU_NODE_INSTANCE_TYPE string `env:"WORKER_GPU_NODE_INSTANCE_TYPE" envDefault:""`
	// GPU_NODE_INSTANCE_TYPES is a comma-separated list of allowed GPU node instance types.
	// Worker uses it only as a fallback when notebook.instance_type is not set in DB.
	GPU_NODE_INSTANCE_TYPES               string `env:"WORKER_GPU_NODE_INSTANCE_TYPES" envDefault:""`
	CPU_NODE_INSTANCE_TYPES               string `env:"WORKER_CPU_NODE_INSTANCE_TYPES,required"`
	IMAGE_PULL_ENABLED                    bool   `env:"WORKER_IMAGE_PULL_ENABLED" envDefault:"false"`
	ECR_SECRET_NAME                       string `env:"WORKER_ECR_SECRET_NAME"`
	RUNTIME_INJECTOR_IMAGE                string `env:"WORKER_RUNTIME_INJECTOR_IMAGE" envDefault:"alpine/git:2.45.2"`
	PLATFORM_TOKEN_SIDECAR_IMAGE          string `env:"WORKER_PLATFORM_TOKEN_SIDECAR_IMAGE" envDefault:""`
	PLATFORM_FILE_API_BASE_URL            string `env:"WORKER_PLATFORM_FILE_API_BASE_URL" envDefault:""`
	PLATFORM_KEYCLOAK_TOKEN_URL           string `env:"WORKER_PLATFORM_KEYCLOAK_TOKEN_URL" envDefault:""`
	PLATFORM_KEYCLOAK_CLIENT_ID           string `env:"WORKER_PLATFORM_KEYCLOAK_CLIENT_ID" envDefault:"sandbox-notebook"`
	PLATFORM_SANDBOX_CONNECT_API_BASE_URL string `env:"WORKER_PLATFORM_SANDBOX_CONNECT_API_BASE_URL" envDefault:""`
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
	app      *application
	notebook Notebook
	logger   *slog.Logger
}
