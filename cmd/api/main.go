// @title           Sandbox Connect API
// @version         1.0
// @description     API for sandbox notebooks (lifecycle via bookings) and profiles. Create CPU/GPU notebooks with POST /v1/bookings; the slot lifecycle service provisions and cleans up resources at the booked slot times.
// @description
// @description     Booking lifecycle states:
// @description     - `scheduled`: booking is accepted and counts against slot capacity, active-booking limits, and weekly quota. No notebook resource is linked yet. Users can cancel scheduled bookings, or reset them to cancelled if cleanup is needed before provisioning.
// @description     - `ready`: slot start has arrived and a notebook metadata row is linked. The worker/lifecycle path is waiting for the Kubeflow Notebook resource to report ready replicas. Users can terminate ready bookings, or reset stuck ready bookings to expired.
// @description     - `active`: the notebook is running and usable. `GET /v1/bookings` returns `notebookUrl` only for active bookings whose notebook resource has been applied. Active bookings can be extended to the next contiguous slot when capacity, category limits, and the one-extension rule allow.
// @description     - `shutting_down`: the booking is inside the category pre-shutdown warning window before `slotEnd`. It remains an active-capacity state and may still be terminated; the lifecycle service will mark it completed at slot end.
// @description     - `completed`: the session ended normally, either at `slotEnd` or through terminate. Completed bookings remain in history; automatic cleanup deletes notebook/PVC resources after the category shutdown grace period unless terminate already cleaned them.
// @description     - `cancelled`: terminal state for scheduled bookings cancelled by the user, reset before resources were ready, or cancelled by lifecycle because the category configuration was invalid. Cancelled bookings do not consume active capacity.
// @description     - `expired`: terminal state for ready bookings that never became active before the category no-show grace period, or ready bookings reset during cleanup. Scheduled bookings reset before resources are ready become cancelled, not expired.
// @description
// @description     Active-capacity checks treat `scheduled`, `ready`, `active`, and `shutting_down` as active booking states. Weekly quota counts the same active booking states plus `completed`; `cancelled` and `expired` do not count toward active capacity, and `expired` is excluded from weekly quota.
// @BasePath        /
// @schemes         http https
// @produce         json
// @consumes        json
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Bearer token authentication. Example: "Bearer {token}"

// Security is defined at the operation level

// @x-extension-info-ratelimit "100 requests per minute"
// @x-extension-info-cors "Configurable CORS origins"

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/gpuconfig"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"syscall"
	"time"

	_ "sandbox-backend-service/docs" // swaggo docs

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logLevel := slog.LevelInfo
	logLevelStr := os.Getenv("API_LOG_LEVEL")
	if logLevelStr == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	if logLevelStr == "debug" {
		logger.Info("Setting log level to DEBUG")
	} else {
		logger.Info("Setting log level to INFO")
	}

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found or error loading it", "error", err)
	}

	var config ApiEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(logger, "failed to parse environment variables", "error", err)
	}

	// Backward compatibility: if SLOT_CONFIG_PROFILE isn't set, fall back to API_GPU_SLOT_CONFIG_PROFILE.
	if config.NotebookConfig.SlotConfigProfile == "" {
		config.NotebookConfig.SlotConfigProfile = config.NotebookConfig.LegacyGPUSlotConfigProfile
	}
	if config.NotebookConfig.SlotConfigProfile == "" {
		config.NotebookConfig.SlotConfigProfile = "production"
	}

	if err := gpuconfig.ValidateGPUSlotConfigProfile(config.NotebookConfig.SlotConfigProfile); err != nil {
		utils.LogErrorAndExit(logger, "invalid GPU slot config profile", "error", err)
	}
	k8sClient, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create kubernetes client", "error", err)
	}
	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	rateLimiter := NewIPRateLimiter(config.RateLimit, config.RateWindowSecs)

	ecrClient, err := NewECRClient(config.RegistrySecretConfig)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create ECR client", "error", err)
	}

	// Initialize audit services (RabbitMQ + Audit)
	// Auditing is optional — if RabbitMQ is not configured, auditing is silently disabled.
	var auditService *AuditService
	rabbitmqService, err := NewRabbitMQService(config.RabbitMQConfig)
	if err != nil {
		slog.Warn("Audit system disabled: RabbitMQ not configured", "error", err)
	} else {
		auditService = NewAuditService(rabbitmqService)
		slog.Info("Audit system initialized successfully")
	}

	app := application{
		pgPool:         pool,
		k8sClient:      k8sClient,
		env:            config,
		rateLimiter:    rateLimiter,
		ecrClient:      ecrClient,
		registrySecret: config.RegistrySecretConfig,
		auditService:   auditService,
	}

	server := http.Server{
		Addr:         config.Address,
		Handler:      app.router(),
		ReadTimeout:  time.Duration(config.ReadTimeoutSecs) * time.Second,
		WriteTimeout: time.Duration(config.WriteTimeoutSecs) * time.Second,
		IdleTimeout:  time.Duration(config.IdleTimeoutSecs) * time.Second,
	}

	serverErrors := make(chan error, 1)

	go func() {
		slog.Info(fmt.Sprintf("server is serving from : http://%s", config.Address))
		serverErrors <- server.ListenAndServe()
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			utils.LogErrorAndExit(logger, "server error", "error", err)
		}
	case sig := <-shutdown:
		slog.Info("starting shutdown", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			server.Close()
			utils.LogErrorAndExit(logger, "could not stop server gracefully", "error", err)
		}

		app.pgPool.Pool.Close()

		// Gracefully close audit/RabbitMQ connection
		if app.auditService != nil {
			if err := app.auditService.Close(); err != nil {
				slog.Warn("Error closing audit service", "error", err)
			}
		}

		slog.Info("shutdown complete")
	}
}
