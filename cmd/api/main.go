// @title           Sandbox Connect API
// @version         1.0
// @description     API for managing notebooks and profiles
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
	k8sClient, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create kubernetes client", "error", err)
	}
	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	rateLimiter := NewIPRateLimiter(config.RateLimit, config.RateWindowSecs)

	ecrClient, err := NewECRClient(config.ECRConfig)
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
		pgPool:       pool,
		k8sClient:    k8sClient,
		env:          config,
		rateLimiter:  rateLimiter,
		ecrClient:    ecrClient,
		auditService: auditService,
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
