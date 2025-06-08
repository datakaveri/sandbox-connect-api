package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/s3"
	"sandbox-backend-service/pkg/utils"
	"sync"
	"syscall"

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

	sigChan := make(chan os.Signal, 2)

	signal.Notify(sigChan, os.Interrupt)
	signal.Notify(sigChan, syscall.SIGTERM)
	k8sClient, err := k8s.NewK8sClient(config.KubeConfigMode, config.KubeConfigPath)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to create kubernetes client", "error", err)
	}

	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	defer pool.Pool.Close()

	s3Client, err := s3.NewS3Client(config.S3_ACCESS_KEY, config.S3_SECRET_KEY, config.S3_REGION)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get create s3 client", "error", err)
	}

	app := &application{
		k8sClient: k8sClient,
		pgPool:    pool,
		s3Client:  s3Client,
		env:       config,
		logger:    logger,
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{}, config.MAX_CONCURRENT_WORKER)

	wg := sync.WaitGroup{}
	defer func() {
		app.logger.Info("Waiting for all workers to stop gracefully.")
		wg.Wait()
		app.logger.Info("All workers have stopped gracefully.")
	}()
	for {
		select {
		case ch <- struct{}{}:
			wg.Add(1)
			go func() {
				defer func() {
					wg.Done()
					<-ch
				}()
				app.worker(ctx)
			}()
		case <-sigChan:
			cancel()
			return
		}
	}

}
