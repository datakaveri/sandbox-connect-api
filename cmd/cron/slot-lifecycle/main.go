package main

import (
	"log/slog"
	"os"
	"os/signal"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logLevel := slog.LevelInfo
	if os.Getenv("SLOT_LIFECYCLE_LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using environment variables from system", "info", err.Error())
	}

	var config CronEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(slog.Default(), "failed to parse environment variables", "error", err)
	}

	// Backward compatibility: if SLOT_CONFIG_PROFILE isn't set, fall back to legacy GPU_SLOT_CONFIG_PROFILE.
	if config.SlotConfigProfile == "" {
		config.SlotConfigProfile = config.LegacyGPUSlotConfigProfile
	}

	if err := gpuconfig.ValidateGPUSlotConfigProfile(config.SlotConfigProfile); err != nil {
		utils.LogErrorAndExit(slog.Default(), "invalid GPU slot config profile", "error", err)
	}

	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	defer pool.Close()

	k8sClient, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create kubernetes client", "error", err)
	}

	// Handle signals so the cron job can exit gracefully.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		<-sigChan
		close(done)
	}()

	if err := runSlotLifecycle(pool, k8sClient, config); err != nil {
		logger.Error("slot lifecycle run failed", "error", err)
		os.Exit(1)
	}

	select {
	case <-done:
	default:
	}
}
