package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
	"sandbox-backend-service/internal/outputargo"
	"sandbox-backend-service/internal/outputprovision"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"

	"github.com/caarlos0/env/v11"
)

type serviceConfig struct {
	PostgresURL    string `env:"OUTPUT_POSTGRES_URL,required"`
	KubeConfigMode string `env:"OUTPUT_KUBE_CONFIG_MODE" envDefault:"cluster"`
	KubeConfigPath string `env:"OUTPUT_KUBE_CONFIG_PATH" envDefault:""`
	WorkerID       string `env:"OUTPUT_WORKER_ID" envDefault:""`

	PollIntervalSeconds   int   `env:"OUTPUT_POLL_INTERVAL_SECONDS" envDefault:"5"`
	PVCWaitTimeoutSeconds int   `env:"OUTPUT_PVC_WAIT_TIMEOUT_SECONDS" envDefault:"300"`
	ClaimLeaseSeconds     int   `env:"OUTPUT_CLAIM_LEASE_SECONDS" envDefault:"60"`
	MaxManifestBytes      int   `env:"OUTPUT_MAX_MANIFEST_BYTES" envDefault:"262144"`
	MaxManifestFiles      int   `env:"OUTPUT_MAX_MANIFEST_FILES" envDefault:"1000"`
	MaxFileBytes          int64 `env:"OUTPUT_MAX_FILE_BYTES" envDefault:"268435456"`
	MaxOutputBytes        int64 `env:"OUTPUT_MAX_BYTES" envDefault:"1073741824"`

	RunnerImage                    string `env:"OUTPUT_RUNNER_IMAGE,required"`
	RunnerMaxLogBytes              int64  `env:"OUTPUT_RUNNER_MAX_LOG_BYTES" envDefault:"5242880"`
	UploaderImage                  string `env:"OUTPUT_UPLOADER_IMAGE,required"`
	WorkflowServiceAccount         string `env:"OUTPUT_WORKFLOW_SERVICE_ACCOUNT" envDefault:"output-runner"`
	ScratchStorageClass            string `env:"OUTPUT_SCRATCH_STORAGE_CLASS,required"`
	ScratchStorageSize             string `env:"OUTPUT_SCRATCH_STORAGE_SIZE" envDefault:"5Gi"`
	ReplacementConfigMapName       string `env:"OUTPUT_REPLACEMENT_CONFIG_MAP" envDefault:"output-replacements-v1"`
	ProductionEnvSecretName        string `env:"OUTPUT_PRODUCTION_ENV_SECRET" envDefault:"output-production-env"`
	FileServiceSecretName          string `env:"OUTPUT_FILE_SERVICE_SECRET" envDefault:"output-file-service"`
	DataAccessEnabled              bool   `env:"OUTPUT_DATA_ACCESS_ENABLED" envDefault:"false"`
	DataAccessBaseURL              string `env:"OUTPUT_DATA_ACCESS_BASE_URL" envDefault:""`
	DataAccessMTLSSecretName       string `env:"OUTPUT_DATA_ACCESS_MTLS_SECRET" envDefault:""`
	PlatformTokenSidecarImage      string `env:"OUTPUT_PLATFORM_TOKEN_SIDECAR_IMAGE" envDefault:""`
	PlatformTokenURL               string `env:"OUTPUT_PLATFORM_TOKEN_URL" envDefault:""`
	PlatformTokenClientID          string `env:"OUTPUT_PLATFORM_TOKEN_CLIENT_ID" envDefault:""`
	PlatformTokenSessionAPIBaseURL string `env:"OUTPUT_PLATFORM_TOKEN_SESSION_API_BASE_URL" envDefault:""`
	FilesConnectBaseURL            string `env:"OUTPUT_FILES_CONNECT_BASE_URL,required"`
	FilesConnectServiceToken       string `env:"OUTPUT_FILES_CONNECT_SERVICE_TOKEN,required"`
	ReviewDatabankID               string `env:"OUTPUT_REVIEW_DATABANK_ID,required"`
	WorkspaceDatabankID            string `env:"OUTPUT_WORKSPACE_DATABANK_ID,required"`
	FilesConnectTimeoutSecs        int    `env:"OUTPUT_FILES_CONNECT_TIMEOUT_SECONDS" envDefault:"30"`
	WorkflowDeadlineSeconds        int64  `env:"OUTPUT_WORKFLOW_DEADLINE_SECONDS" envDefault:"1800"`
	WorkflowTTLSeconds             int64  `env:"OUTPUT_WORKFLOW_TTL_SECONDS" envDefault:"3600"`

	// Automatic namespace provisioning: off (default), dry-run, or apply.
	ProvisionMode                  string   `env:"OUTPUT_PROVISION_MODE" envDefault:"off"`
	ProvisionIntervalSeconds       int      `env:"OUTPUT_PROVISION_INTERVAL_SECONDS" envDefault:"600"`
	ProvisionProfileLabel          string   `env:"OUTPUT_PROVISION_PROFILE_LABEL" envDefault:"app.kubernetes.io/part-of=kubeflow-profile"`
	ProvisionAllowedNamespaces     []string `env:"OUTPUT_PROVISION_ALLOWED_NAMESPACES" envSeparator:","`
	ProvisionRunnerClusterRole     string   `env:"OUTPUT_PROVISION_RUNNER_CLUSTER_ROLE" envDefault:"output-runner-executor"`
	ProvisionRegistrySecretName    string   `env:"OUTPUT_PROVISION_REGISTRY_SECRET_NAME" envDefault:"nha-registry-cred"`
	ProvisionRegistrySecretDir     string   `env:"OUTPUT_PROVISION_REGISTRY_SECRET_DIR" envDefault:"/var/run/output-provision/registry"`
	ProvisionFileServiceSecretDir  string   `env:"OUTPUT_PROVISION_FILE_SERVICE_SECRET_DIR" envDefault:"/var/run/output-provision/file-service"`
	ProvisionMTLSSourceNamespace   string   `env:"OUTPUT_PROVISION_MTLS_SOURCE_NAMESPACE" envDefault:"omop-auth"`
	ProvisionMTLSSourceSecret      string   `env:"OUTPUT_PROVISION_MTLS_SOURCE_SECRET" envDefault:"output-data-access-mtls-issued"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "output-argo-service")
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
		logger.Error("connect to output queue", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	kube, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		logger.Error("create Kubernetes client", "error", err)
		os.Exit(1)
	}
	filesClient, err := filesconnect.NewClient(filesconnect.Config{
		Limits:  output.Limits{MaxManifestBytes: config.MaxManifestBytes, MaxManifestFiles: config.MaxManifestFiles, MaxFileBytes: config.MaxFileBytes, MaxOutputBytes: config.MaxOutputBytes},
		BaseURL: config.FilesConnectBaseURL, ServiceToken: config.FilesConnectServiceToken,
		ReviewDatabankID: config.ReviewDatabankID, WorkspaceDatabankID: config.WorkspaceDatabankID,
		Timeout: time.Duration(config.FilesConnectTimeoutSecs) * time.Second,
	})
	if err != nil {
		logger.Error("initialize Files Connect client", "error", err)
		os.Exit(1)
	}
	controller, err := outputargo.NewController(
		output.NewStore(pool.Pool), filesClient, kube.Dynamic,
		outputargo.ControllerConfig{
			WorkerID:         config.WorkerID,
			PollInterval:     time.Duration(config.PollIntervalSeconds) * time.Second,
			PVCWaitTimeout:   time.Duration(config.PVCWaitTimeoutSeconds) * time.Second,
			ClaimLease:       time.Duration(config.ClaimLeaseSeconds) * time.Second,
			MaxManifestBytes: config.MaxManifestBytes,
			MaxManifestFiles: config.MaxManifestFiles,
			MaxFileBytes:     config.MaxFileBytes,
			MaxOutputBytes:   config.MaxOutputBytes,
			Workflow: outputargo.WorkflowConfig{
				RunnerImage: config.RunnerImage, RunnerMaxLogBytes: config.RunnerMaxLogBytes, UploaderImage: config.UploaderImage,
				ServiceAccountName:             config.WorkflowServiceAccount,
				ScratchStorageClass:            config.ScratchStorageClass,
				ScratchStorageSize:             config.ScratchStorageSize,
				ReplacementConfigMapName:       config.ReplacementConfigMapName,
				ProductionEnvSecretName:        config.ProductionEnvSecretName,
				FileServiceSecretName:          config.FileServiceSecretName,
				DataAccessEnabled:              config.DataAccessEnabled,
				DataAccessBaseURL:              config.DataAccessBaseURL,
				DataAccessMTLSSecretName:       config.DataAccessMTLSSecretName,
				PlatformTokenSidecarImage:      config.PlatformTokenSidecarImage,
				PlatformTokenURL:               config.PlatformTokenURL,
				PlatformTokenClientID:          config.PlatformTokenClientID,
				PlatformTokenSessionAPIBaseURL: config.PlatformTokenSessionAPIBaseURL,
				ActiveDeadlineSeconds:          config.WorkflowDeadlineSeconds,
				TTLSecondsAfterFinished:        config.WorkflowTTLSeconds,
			},
		}, logger)
	if err != nil {
		logger.Error("initialize controller", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	provisioner, err := newProvisioner(config, kube, logger)
	if err != nil {
		logger.Error("initialize namespace provisioner", "error", err)
		os.Exit(1)
	}
	if provisioner != nil {
		controller.SetProvisioner(outputprovision.Workflow{P: provisioner})
		go provisioner.Run(ctx, time.Duration(config.ProvisionIntervalSeconds)*time.Second)
		logger.Info("namespace provisioner started", "mode", string(provisioner.Mode()))
	}
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

func newProvisioner(config serviceConfig, kube *k8s.K8sClient, logger *slog.Logger) (*outputprovision.Provisioner, error) {
	labelKey, labelValue, ok := strings.Cut(config.ProvisionProfileLabel, "=")
	if !ok {
		return nil, fmt.Errorf("OUTPUT_PROVISION_PROFILE_LABEL must be key=value")
	}
	if config.ProvisionMode != string(outputprovision.ModeOff) && config.ProvisionIntervalSeconds < 60 {
		return nil, fmt.Errorf("OUTPUT_PROVISION_INTERVAL_SECONDS must be at least 60")
	}
	var allowed []string
	for _, name := range config.ProvisionAllowedNamespaces {
		if name = strings.TrimSpace(name); name != "" {
			allowed = append(allowed, name)
		}
	}
	return outputprovision.New(kube.Dynamic, outputprovision.Config{
		Mode:                     outputprovision.Mode(config.ProvisionMode),
		ProfileLabelKey:          labelKey,
		ProfileLabelValue:        labelValue,
		AllowedNamespaces:        allowed,
		RunnerServiceAccount:     config.WorkflowServiceAccount,
		RunnerClusterRole:        config.ProvisionRunnerClusterRole,
		RegistrySecretName:       config.ProvisionRegistrySecretName,
		RegistrySecretDir:        config.ProvisionRegistrySecretDir,
		FileServiceSecretName:    config.FileServiceSecretName,
		FileServiceSecretDir:     config.ProvisionFileServiceSecretDir,
		ReplacementConfigMapName: config.ReplacementConfigMapName,
		ProductionEnvSecretName:  config.ProductionEnvSecretName,
		MTLSEnabled:              config.DataAccessEnabled,
		MTLSSecretName:           config.DataAccessMTLSSecretName,
		MTLSSourceNamespace:      config.ProvisionMTLSSourceNamespace,
		MTLSSourceSecret:         config.ProvisionMTLSSourceSecret,
	}, logger)
}
