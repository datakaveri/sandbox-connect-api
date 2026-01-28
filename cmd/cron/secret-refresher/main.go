package main

import (
	"context"
	"log/slog"
	"os"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	logger = logger.With("pod_name", os.Getenv("HOSTNAME"))

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found or error loading it", "error", err)
	}

	var config Env
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(logger, "failed to parse environment variables", "error", err)
	}

	k8sClient, err := k8s.NewK8sClient(config.K8sConfigMode, config.K8sConfigPath)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create kubernetes client", "error", err)
	}

	refresher := &secretRefresher{
		env:    config,
		logger: logger,
	}

	ctx := context.Background()
	if err := refresher.createSecret(ctx, k8sClient); err != nil {
		utils.LogErrorAndExit(logger, "failed to create/update secret", "error", err)
	}

	logger.Info("Secret operation completed successfully")
}
