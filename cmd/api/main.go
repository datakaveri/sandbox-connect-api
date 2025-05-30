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

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

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

	app := application{
		pgPool:      pool,
		k8sClient:   k8sClient,
		env:         config,
		rateLimiter: rateLimiter,
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

		slog.Info("shutdown complete")
	}
}
