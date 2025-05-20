package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sandbox-backend-service/pkg/db"
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

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found or error loading it", "error", err)
	}

	var config ApiEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit("failed to parse environment variables", "error", err)
	}
	k8sClient, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		utils.LogErrorAndExit("failed to create kubernetes client", "error", err)
	}
	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit("failed to get pool of connection", "error", err)
	}
	app := application{
		pgPool:    pool,
		k8sClient: k8sClient,
		env:       config,
	}

	slog.Info(fmt.Sprintf("server is serving from : http://%s", config.Address))
	server := http.Server{
		Addr:    config.Address,
		Handler: loggingMiddleware(app.enableCORS(app.router())),
	}
	if err := server.ListenAndServe(); err != nil {
		utils.LogErrorAndExit("Can't start server", "address", config.Address, "error", err)
	}
}
