package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

const livenessFile = "/tmp/slot-lifecycle-alive"

func main() {
	logLevel := slog.LevelInfo
	if os.Getenv("SLOT_LIFECYCLE_LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}

	handlerOpts := &slog.HandlerOptions{Level: logLevel}
	var handler slog.Handler
	if os.Getenv("SLOT_LIFECYCLE_LOG_FORMAT") == "text" {
		handler = slog.NewTextHandler(os.Stdout, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, handlerOpts)
	}
	logger := slog.New(handler)
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	tickInterval := time.Duration(config.TickIntervalSecs) * time.Second
	runTimeout := time.Duration(config.RunTimeoutSecs) * time.Second

	logger.Info("slot lifecycle worker started", "tick_interval", tickInterval, "run_timeout", runTimeout)

	for {
		// Touch liveness file so the K8s liveness probe can verify the loop is running.
		_ = os.WriteFile(livenessFile, []byte(time.Now().Format(time.RFC3339)), 0644)

		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		if err := runSlotLifecycle(ctx, pool, k8sClient, config); err != nil {
			logger.Error("slot lifecycle run failed", "error", err)
		}
		cancel()
		fmt.Println("****")

		select {
		case <-sigChan:
			logger.Info("shutting down slot lifecycle worker")
			return
		case <-time.After(tickInterval):
		}
	}
}
