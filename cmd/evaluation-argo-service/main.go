package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sandbox-backend-service/internal/evaluation"
	"sandbox-backend-service/internal/evaluationargo"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"

	"github.com/caarlos0/env/v11"
)

type serviceConfig struct {
	PostgresURL    string `env:"EVALUATION_POSTGRES_URL,required"`
	KubeConfigMode string `env:"EVALUATION_KUBE_CONFIG_MODE" envDefault:"cluster"`
	KubeConfigPath string `env:"EVALUATION_KUBE_CONFIG_PATH" envDefault:""`
	WorkerID       string `env:"EVALUATION_WORKER_ID" envDefault:""`

	PollIntervalSeconds   int `env:"EVALUATION_POLL_INTERVAL_SECONDS" envDefault:"5"`
	PVCWaitTimeoutSeconds int `env:"EVALUATION_PVC_WAIT_TIMEOUT_SECONDS" envDefault:"300"`
	ClaimLeaseSeconds     int `env:"EVALUATION_CLAIM_LEASE_SECONDS" envDefault:"60"`
	MaxManifestBytes      int `env:"EVALUATION_MAX_MANIFEST_BYTES" envDefault:"262144"`
	MaxManifestFiles      int `env:"EVALUATION_MAX_MANIFEST_FILES" envDefault:"1000"`

	RunnerImage              string `env:"EVALUATION_RUNNER_IMAGE,required"`
	UploaderImage            string `env:"EVALUATION_UPLOADER_IMAGE,required"`
	CopierImage              string `env:"EVALUATION_COPIER_IMAGE,required"`
	WorkflowServiceAccount   string `env:"EVALUATION_WORKFLOW_SERVICE_ACCOUNT" envDefault:"evaluation-runner"`
	CopyServiceAccount       string `env:"EVALUATION_COPY_SERVICE_ACCOUNT" envDefault:"evaluation-runner"`
	ScratchStorageClass      string `env:"EVALUATION_SCRATCH_STORAGE_CLASS,required"`
	ScratchStorageSize       string `env:"EVALUATION_SCRATCH_STORAGE_SIZE" envDefault:"5Gi"`
	ReplacementConfigMapName string `env:"EVALUATION_REPLACEMENT_CONFIG_MAP" envDefault:"evaluation-replacements-v1"`
	ProductionEnvSecretName  string `env:"EVALUATION_PRODUCTION_ENV_SECRET" envDefault:"evaluation-production-env"`
	FileServiceSecretName    string `env:"EVALUATION_FILE_SERVICE_SECRET" envDefault:"evaluation-file-service"`
	WorkspacePVCName         string `env:"EVALUATION_WORKSPACE_PVC" envDefault:"evaluation-workspace"`
	UserWorkspacePath        string `env:"EVALUATION_USER_WORKSPACE_PATH" envDefault:"/home/jovyan/evaluation-workspace"`
	WorkspaceMountPath       string `env:"EVALUATION_WORKSPACE_MOUNT_PATH" envDefault:"/workspace"`
	WorkflowDeadlineSeconds  int64  `env:"EVALUATION_WORKFLOW_DEADLINE_SECONDS" envDefault:"1800"`
	WorkflowTTLSeconds       int64  `env:"EVALUATION_WORKFLOW_TTL_SECONDS" envDefault:"3600"`
	CopyDeadlineSeconds      int64  `env:"EVALUATION_COPY_DEADLINE_SECONDS" envDefault:"900"`
	CopyTTLSeconds           int64  `env:"EVALUATION_COPY_TTL_SECONDS" envDefault:"3600"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "evaluation-argo-service")
	slog.SetDefault(logger)
	var config serviceConfig
	if err := env.Parse(&config); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if config.WorkerID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			logger.Error("resolve worker identity", "error", err)
			os.Exit(1)
		}
		config.WorkerID = hostname
	}

	pool, err := db.NewPool(config.PostgresURL)
	if err != nil {
		logger.Error("connect to evaluation queue", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	kube, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		logger.Error("create Kubernetes client", "error", err)
		os.Exit(1)
	}
	controller, err := evaluationargo.NewController(
		evaluation.NewStore(pool.Pool), kube.Dynamic,
		evaluationargo.ControllerConfig{
			WorkerID:         config.WorkerID,
			PollInterval:     time.Duration(config.PollIntervalSeconds) * time.Second,
			PVCWaitTimeout:   time.Duration(config.PVCWaitTimeoutSeconds) * time.Second,
			ClaimLease:       time.Duration(config.ClaimLeaseSeconds) * time.Second,
			MaxManifestBytes: config.MaxManifestBytes,
			MaxManifestFiles: config.MaxManifestFiles,
			Workflow: evaluationargo.WorkflowConfig{
				RunnerImage: config.RunnerImage, UploaderImage: config.UploaderImage,
				ServiceAccountName:       config.WorkflowServiceAccount,
				ScratchStorageClass:      config.ScratchStorageClass,
				ScratchStorageSize:       config.ScratchStorageSize,
				ReplacementConfigMapName: config.ReplacementConfigMapName,
				ProductionEnvSecretName:  config.ProductionEnvSecretName,
				FileServiceSecretName:    config.FileServiceSecretName,
				ActiveDeadlineSeconds:    config.WorkflowDeadlineSeconds,
				TTLSecondsAfterFinished:  config.WorkflowTTLSeconds,
			},
			CopyJob: evaluationargo.CopyJobConfig{
				CopierImage: config.CopierImage, ServiceAccountName: config.CopyServiceAccount,
				WorkspacePVCName:      config.WorkspacePVCName,
				UserWorkspacePath:     config.UserWorkspacePath,
				WorkspaceMountPath:    config.WorkspaceMountPath,
				FileServiceSecretName: config.FileServiceSecretName,
				ActiveDeadlineSeconds: config.CopyDeadlineSeconds,
				TTLSecondsAfterFinish: config.CopyTTLSeconds,
			},
		}, logger)
	if err != nil {
		logger.Error("initialize controller", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	logger.Info("controller started", "worker_id", config.WorkerID)
	for ctx.Err() == nil {
		if err := controller.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logger.Error("reconciliation failed", "error", err)
		}
		timer := time.NewTimer(time.Duration(config.PollIntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	logger.Info("controller stopped")
}
