package main

import (
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"log/slog"
	"os"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/s3"
	"sandbox-backend-service/pkg/utils"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found or error loading it", "error", err)
	}

	var config Env
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
	s3Client, err := s3.NewS3Client(config.S3_ACCESS_KEY, config.S3_SECRET_KEY, config.S3_REGION)
	if err != nil {
		utils.LogErrorAndExit("failed to get create s3 client", "error", err)
	}
	app := &application{
		k8sClient: k8sClient,
		pgPool:    pool,
		s3Client:  s3Client,
		env:       config,
	}
	app.spinner()

}
