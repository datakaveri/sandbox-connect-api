package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/utils"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

	var config CronEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit("failed to parse environment variables", "error", err)
	}
	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit("failed to get pool of connection", "error", err)
	}
	defer pool.Pool.Close()
	if err := syncProfileCosts(pool.Pool, config); err != nil {
		utils.LogErrorAndExit(
			"failed to sync profile costs",
			"error", err)
	}
}

func syncProfileCosts(pgPool *pgxpool.Pool, config CronEnv) error {
	ctx := context.Background()
	slog.Info("starting profile credit sync")
	start := time.Now().AddDate(0, 0, -1).UTC().Format(time.RFC3339)
	end := time.Now().UTC().Format(time.RFC3339)

	slog.Info("fetching cost data from OpenCost",
		"start_time", start,
		"end_time", end,
		"url", config.OpenCostURL)

	openCostURL := fmt.Sprintf("%s/allocation?window=%s,%s&aggregate=namespace&format=json",
		config.OpenCostURL, start, end)

	client := &http.Client{Timeout: 1 * time.Minute}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, openCostURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	defer resp.Body.Close()

	slog.Info("received response from OpenCost", "status", resp.Status)

	var costData CostAllocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
		return fmt.Errorf("failed to decode cost data: %v", err)
	}

	var profiles []CostAllocation
	for _, dataMap := range costData.Data {
		for _, data := range dataMap {
			profileID := data.Properties.Namespace
			if _, err := uuid.Parse(profileID); err != nil {
				slog.Warn("skipping invalid profile ID - must be UUID",
					"profile_id", profileID,
					"error", err)
				continue
			}

			if profileID == "" {
				slog.Warn("skipping empty profile ID", "profile_id", profileID)
				continue
			}

			profiles = append(profiles, data)
		}
	}

	if len(profiles) == 0 {
		slog.Warn("no profile credit data received from OpenCost")
		return nil
	}

	slog.Info("processing profile credits in batches",
		"batch_size", config.BatchSize,
		"max_retries", config.MaxRetries,
		"total_profiles", len(profiles))

	for i := 0; i < len(profiles); i += config.BatchSize {
		end := i + config.BatchSize
		if end > len(profiles) {
			end = len(profiles)
		}

		batchData := profiles[i:end]
		if err := processBatch(ctx, pgPool, batchData, config.MaxRetries); err != nil {
			return fmt.Errorf("failed to process batch %d-%d: %v", i, end, err)
		}
	}
	slog.Info("completed namespace cost sync",
		"total_profiles", len(profiles),
		"total_batches", (len(profiles)+config.BatchSize-1)/config.BatchSize)

	return nil
}

func processBatch(ctx context.Context, pgPool *pgxpool.Pool, data []CostAllocation, maxRetries int) error {
	batch := &pgx.Batch{}

	validData := make([]CostAllocation, 0, len(data))
	for _, d := range data {
		if d.Properties.Namespace == "" {
			slog.Warn("skipping entry with empty profile ID",
				"name", d.Name,
				"cluster", d.Properties.Cluster,
				"cpu_cost", d.CPUCost,
				"total_cost", d.TotalCost)
			continue
		}
		validData = append(validData, d)
	}

	if len(validData) == 0 {
		slog.Warn("no valid profiles in batch")
		return nil
	}

	for _, d := range validData {
		profileUUID, err := uuid.Parse(d.Properties.Namespace)
		if err != nil {
			slog.Error("failed to parse profile ID as UUID",
				"profile_id", d.Properties.Namespace,
				"error", err)
			continue
		}

		batch.Queue(
			`INSERT INTO profile_costs(profile_id, gpu_total_cost, cpu_total_cost, memory_total_cost, total_cost, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (profile_id) DO UPDATE
			SET gpu_total_cost = $2,
				cpu_total_cost = $3,
				memory_total_cost = $4,
				total_cost = $5,
				updated_at = NOW()`,
			profileUUID, d.GPUCost, d.CPUCost, d.RAMCost, d.TotalCost,
		)
	}

	backoffConfig := utils.BackoffConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     10 * time.Second,
		MaxAttempts:  maxRetries,
		Factor:       2.0,
	}

	return utils.WithRetry(ctx, backoffConfig, func() error {
		br := pgPool.SendBatch(ctx, batch)
		defer br.Close()
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("failed to execute batch query: %v", err)
		}
		slog.Debug("batch processed successfully", "queries_executed", batch.Len())
		return nil
	})
}
