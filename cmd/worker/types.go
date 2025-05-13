package main

import (
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/s3"
)

type application struct {
	env       Env
	k8sClient *k8s.K8sClient
	pgPool    *db.PgPool
	s3Client  *s3.S3Client
}

type Env struct {
	KubeConfigPath          string `env:"KUBE_CONFIG_PATH" envDefault:""`
	KubeConfigMode          string `env:"KUBE_CONFIG_MODE" envDefault:"cluster"`
	POSTGRES_URL            string `env:"POSTGRES_URL,required"`
	MAX_CONCURRENT_WORKER   int    `env:"MAX_CONCURRENT_WORKER,required"`
	S3_ENDPOINT             string `env:"S3_ENDPOINT,required"`
	S3_REGION               string `env:"S3_REGION,required"`
	S3_ACCESS_KEY           string `env:"S3_ACCESS_KEY,required"`
	S3_SECRET_KEY           string `env:"S3_SECRET_KEY,required"`
	S3_TEMPLATE_BUCKET_NAME string `env:"S3_TEMPLATE_BUCKET_NAME,required"`
}
type Notebook struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Namespace     string  `json:"namespace"`
	StorageSize   string  `json:"storage_size"`
	PVCname       string  `json:"pvc_name"`
	CPURequest    float64 `json:"cpu_request"`
	CPULimit      float64 `json:"cpu_limit"`
	MemoryRequest string  `json:"memory_request"`
	MemoryLimit   string  `json:"memory_limit"`
	GPUType       *string `json:"gpu_type"`
	GPUCount      *int    `json:"gpu_count"`
	TemplateName  *string `json:"template_name"`
}
