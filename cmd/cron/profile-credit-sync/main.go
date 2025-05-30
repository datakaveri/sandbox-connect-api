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
	"github.com/joho/godotenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	logger = logger.With("hostname", os.Getenv("HOSTNAME"))

	if err := godotenv.Load(); err != nil {
		logger.Info("no .env file found or error loading it", "error", err)
	}

	var config CronEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(logger, "failed to parse environment variables", "error", err)
	}
	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	defer pool.Pool.Close()
	profileSync := profileSync{
		logger: logger,
		config: config,
		pgPool: pool.Pool,
	}
	if err := profileSync.syncProfileCosts(); err != nil {
		utils.LogErrorAndExit(logger, "failed to sync profile costs", "error", err)
	}
}

func (ps *profileSync) syncProfileCosts() error {
	start := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	end := time.Now().UTC().Format(time.RFC3339)
	ps.logger.Info("fetching cost data from OpenCost",
		"start_time", start,
		"end_time", end,
		"url", ps.config.OpenCostURL,
		"max_retries", ps.config.MaxRetries)

	openCostURL := fmt.Sprintf("%s/allocation?window=%s,%s&aggregate=namespace&format=json",
		ps.config.OpenCostURL, start, end)

	client := &http.Client{Timeout: 1 * time.Minute}

	req, err := http.NewRequest(http.MethodGet, openCostURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	ctx := context.Background()
	backoffConfig := utils.BackoffConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     10 * time.Second,
		MaxAttempts:  ps.config.MaxRetries,
		Factor:       2.0,
	}

	var costData CostAllocationResponse
	err = utils.WithRetry(ctx, backoffConfig, func() error {
		resp, err := client.Do(req)
		if err != nil {
			ps.logger.Warn("OpenCost request failed, will retry", "error", err)
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ps.logger.Warn("OpenCost returned non-OK status code, will retry", "status_code", resp.StatusCode)
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		ps.logger.Info("received response from OpenCost", "status", resp.Status)

		if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
			ps.logger.Warn("failed to decode OpenCost response, will retry", "error", err)
			return fmt.Errorf("failed to decode cost data: %v", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to fetch data from OpenCost after %d retries: %v", ps.config.MaxRetries, err)
	}

	var profiles []CostAllocation
	for _, dataMap := range costData.Data {
		for _, data := range dataMap {
			profileID := data.Properties.Namespace
			if _, err := uuid.Parse(profileID); err != nil {
				ps.logger.Warn("skipping invalid profile ID - must be UUID",
					"profile_id", profileID,
					"error", err)
				continue
			}

			if profileID == "" {
				ps.logger.Warn("skipping empty profile ID", "profile_id", profileID)
				continue
			}

			profiles = append(profiles, data)
		}
	}

	if len(profiles) == 0 {
		ps.logger.Warn("no profile credit data received from OpenCost")
		return nil
	}

	ps.logger.Info("processing profile credits in batches",
		"batch_size", ps.config.BatchSize,
		"max_retries", ps.config.MaxRetries,
		"total_profiles", len(profiles))

	for i := 0; i < len(profiles); i += ps.config.BatchSize {
		end := i + ps.config.BatchSize
		if end > len(profiles) {
			end = len(profiles)
		}

		batchData := profiles[i:end]
		if err := ps.processBatch(context.Background(), batchData, ps.config.MaxRetries); err != nil {
			return fmt.Errorf("failed to process batch %d-%d: %v", i, end, err)
		}
	}
	ps.logger.Info("completed namespace cost sync",
		"total_profiles", len(profiles),
		"total_batches", (len(profiles)+ps.config.BatchSize-1)/ps.config.BatchSize)

	return nil
}

func (ps *profileSync) processBatch(ctx context.Context, data []CostAllocation, maxRetries int) error {
	batch := &pgx.Batch{}
	validData := make([]CostAllocation, 0, len(data))
	for _, d := range data {
		if d.Properties.Namespace == "" {
			ps.logger.Warn("skipping entry with empty profile ID",
				"name", d.Name,
				"cluster", d.Properties.Cluster,
				"cpu_cost", d.CPUCost,
				"total_cost", d.TotalCost)
			continue
		}
		validData = append(validData, d)
	}

	if len(validData) == 0 {
		ps.logger.Warn("no valid profiles in batch")
		return nil
	}

	for _, d := range validData {
		profileUUID, err := uuid.Parse(d.Properties.Namespace)
		if err != nil {
			ps.logger.Error("failed to parse profile ID as UUID",
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
		br := ps.pgPool.SendBatch(ctx, batch)
		defer br.Close()
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("failed to execute batch query: %v", err)
		}
		ps.logger.Debug("batch processed successfully", "queries_executed", batch.Len())
		return nil
	})
}
