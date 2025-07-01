package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"strings"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logLevel := slog.LevelInfo
	logLevelStr := os.Getenv("PROFILE_CREDIT_SYNC_LOG_LEVEL")
	if logLevelStr == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	logger = logger.With("hostname", os.Getenv("HOSTNAME"))
	if logLevelStr == "debug" {
		logger.Info("Setting log level to DEBUG")
	} else {
		logger.Info("Setting log level to INFO")
	}

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using environment variables from system", "info", err.Error())
	}

	var config CronEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(slog.Default(), "failed to parse environment variables", "error", err)
	}

	rootCtx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logger.Info("received shutdown signal", "signal", sig.String())
		cancel()
	}()

	pool, err := db.NewPool(config.POSTGRES_URL)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get pool of connection", "error", err)
	}
	defer pool.Close()
	k8sClient, err := k8s.NewK8sClient(config.K8S_CONFIG_MODE, config.K8S_CONFIG_PATH)
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get Kubernetes client", "error", err)
	}
	profileSync := &profileSync{
		logger:        logger,
		config:        config,
		pgPool:        pool.Pool,
		dynamicClient: k8sClient,
		rootCtx:       rootCtx,
	}

	k8sProfiles, err := profileSync.getAllProfilesFromK8s()

	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get profiles from Kubernetes", "error", err)
	}
	dbProfiles, err := profileSync.getAllProfileFromDb()
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to fetch profiles from DB", "error", err)
	}
	k8sOrphans, dbOrphans := profileSync.findOrphanProfiles(k8sProfiles, dbProfiles)

	if len(k8sOrphans) > 0 {
		for _, orphan := range k8sOrphans {
			if orphan.CreatedAt.IsZero() {
				utils.LogErrorAndExit(logger, "creationTimestamp not found for K8s orphan", "user_id", orphan.UserID)
			}
			dbProfile, err := profileSync.addProfileToDbFull(orphan, orphan.CreatedAt)
			if err != nil {
				utils.LogErrorAndExit(logger, "can't able to add profile to DB", "user_id", orphan.UserID, "email", orphan.Email, "error", err)
			} else {
				logger.Info("successfully added profile to DB", "user_id", orphan.UserID, "email", orphan.Email)
				dbProfiles = append(dbProfiles, dbProfile)
			}

		}

	}

	if len(dbOrphans) > 0 {
		for _, orphan := range dbOrphans {
			k8sProfile, err := profileSync.addProfileToK8s(orphan)
			if err != nil {
				utils.LogErrorAndExit(logger, "failed to add profile to K8s", "user_id", orphan.UserID, "error", err)
			} else {
				logger.Info("successfully added profile to K8s", "user_id", orphan.UserID)
				k8sProfiles = append(k8sProfiles, k8sProfile)
			}
		}
	}

	token, err := profileSync.getKeycloakToken()
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get Keycloak token", "error", err)
	}

	if err := profileSync.processProfiles(k8sProfiles, dbProfiles, token); err != nil {
		utils.LogErrorAndExit(logger, "failed to process profiles", "error", err)
	}
}

func (ps *profileSync) findOrphanProfiles(k8sProfiles []KubeflowProfile, dbProfiles []Profile) ([]KubeflowProfile, []Profile) {
	var k8sOrphans []KubeflowProfile
	for _, k8sProfile := range k8sProfiles {
		found := false
		for _, dbProfile := range dbProfiles {
			if dbProfile.UserID == k8sProfile.UserID {
				found = true
				break
			}
		}
		if !found {
			k8sOrphans = append(k8sOrphans, k8sProfile)
		}
	}
	var dbOrphans []Profile
	for _, dbProfile := range dbProfiles {
		found := false
		for _, k8sProfile := range k8sProfiles {
			if k8sProfile.UserID == dbProfile.UserID {
				found = true
				break
			}
		}
		if !found {
			dbOrphans = append(dbOrphans, dbProfile)
		}
	}

	return k8sOrphans, dbOrphans
}

func (ps *profileSync) processProfiles(k8sProfiles []KubeflowProfile, dbProfiles []Profile, token string) error {
	ps.logger.Info("processing profiles with single-phase approach", "count", len(k8sProfiles), "approach", "sequential cost calculation and immediate deduction")

	successfulDeductions := 0
	for _, profile := range k8sProfiles {
		select {
		case <-ps.rootCtx.Done():
			ps.logger.Warn("context cancelled during profile processing")
			return nil
		default:
			userID := profile.UserID
			logger := ps.logger.With("user_id", userID)
			err := utils.WithExponentialBackoff(ps.rootCtx, profileProcessingRetryConfig, logger, func() (constants.ShouldContinue, error) {
				err := ps.executeProfileProcessing(userID, token, logger)
				if err != nil {
					logger.Warn("profile processing attempt failed", "error", err)
					return constants.RetryContinue, err
				}

				logger.Info("successfully completed profile processing")
				return constants.RetryStop, nil
			})

			if err != nil {
				logger.Error("failed to process profile cost and deduction", "error", err)
			} else {
				successfulDeductions++
				logger.Info("successfully processed profile")
			}

			// 1ms break between processing each profile
			time.Sleep(1 * time.Millisecond)
		}
	}

	ps.logger.Info("profile sync completed successfully",
		"total_profiles", len(k8sProfiles),
		"successful_deductions", successfulDeductions)
	return nil
}

func (ps *profileSync) executeProfileProcessing(userID string, token string, logger *slog.Logger) error {
	// once we came inside this we should leave until we finish processing profile
	ctx := context.Background()
	tx, err := ps.pgPool.Begin(ctx)
	if err != nil {
		logger.Error("failed to begin transaction for profile lock", "error", err)
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		tx.Rollback(ctx)
	}()
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Acquire row lock with SELECT FOR UPDATE and get fresh profile data
	var freshProfile Profile
	err = tx.QueryRow(lockCtx, `
		SELECT id, user_id, email, total_paid_credit, last_sync_balance, 
		       can_create_gpu_notebook, aaa_and_opencost_synced_at, pending_deduction
		FROM profiles 
		WHERE user_id = $1 
		FOR UPDATE
	`, userID).Scan(
		&freshProfile.ProfileID,
		&freshProfile.UserID,
		&freshProfile.Email,
		&freshProfile.TotalPaidCredit,
		&freshProfile.LastSyncBalance,
		&freshProfile.CanCreateGpuNotebook,
		&freshProfile.AaaAndOpenCostSyncedAt,
		&freshProfile.PendingDeduction)

	if err != nil {
		logger.Error("failed to acquire profile lock and fetch fresh data", "error", err)
		return fmt.Errorf("failed to acquire profile lock: %w", err)
	}

	logger.Info("successfully acquired profile lock with fresh data",
		"locked_profile_id", freshProfile.ProfileID,
		"pending_deduction", freshProfile.PendingDeduction,
		"last_sync_balance", freshProfile.LastSyncBalance,
		"can_create_gpu_notebook", freshProfile.CanCreateGpuNotebook)

	currentTime := time.Now()
	timeSinceLastSync := currentTime.Sub(freshProfile.AaaAndOpenCostSyncedAt)

	var cost float64
	syncAtTime := time.Now()

	if timeSinceLastSync < 3*time.Minute {
		logger.Info("last sync was less than 3 minutes ago, setting OpenCost to 0 but checking pending deductions",
			"last_sync", freshProfile.AaaAndOpenCostSyncedAt,
			"time_since_last_sync", timeSinceLastSync.String(),
			"pending_deduction", freshProfile.PendingDeduction)
		cost = 0
	} else {
		logger.Info("processing profile - sufficient time since last sync",
			"last_sync", freshProfile.AaaAndOpenCostSyncedAt,
			"time_since_last_sync", timeSinceLastSync.String())

		start := freshProfile.AaaAndOpenCostSyncedAt

		logger.Info("calculating cost from OpenCost with fresh profile data", "start_time", start, "end_time", syncAtTime)
		var err error
		cost, err = ps.getProfileCostFromOpenCost(start, syncAtTime, &freshProfile)
		if err != nil {
			logger.Error("failed to get profile cost from OpenCost", "error", err)
			return fmt.Errorf("failed to get profile cost: %w", err)
		}
		logger.Info("cost calculated from OpenCost", "cost", cost)
	}

	canCreateGpuNotebook := freshProfile.CanCreateGpuNotebook
	pendingDeduction := freshProfile.PendingDeduction
	lastSyncBalance := freshProfile.LastSyncBalance
	totalPaidCredit := freshProfile.TotalPaidCredit
	totalDeduction := pendingDeduction + cost

	if totalDeduction > 0 {
		logger.Info("total deduction > 0, checking user balance first", "total_deduction", totalDeduction)

		// Get current user balance
		balance, balanceErr := ps.getUserBalance(ctx, userID, token)
		if balanceErr != nil {
			logger.Error("Failed to get user balance, keeping pending deduction", "error", balanceErr)
			pendingDeduction = totalDeduction
			canCreateGpuNotebook = false
		} else {
			logger.Info("Retrieved user balance", "balance", balance, "required", totalDeduction)

			if balance == 0 {
				// User has zero balance - don't do anything, keep full pending, remove GPU access
				logger.Info("User has zero balance, keeping full pending deduction and removing GPU access")
				pendingDeduction = totalDeduction
				canCreateGpuNotebook = false
				err := ps.stopAllGPUNotebooksInNamespace(ctx, &freshProfile)
				if err != nil {
					logger.Error("failed to stop notebooks after zero balance", "error", err)
				}
			} else if balance < totalDeduction {
				// User has some money but less than required - deduct what they have
				logger.Info("User has partial balance, deducting available amount",
					"balance", balance, "total_required", totalDeduction)
				canCreateGpuNotebook = false
				err := ps.stopAllGPUNotebooksInNamespace(ctx, &freshProfile)
				if err != nil {
					logger.Error("failed to stop notebooks after insufficient balance", "error", err)
				}

				res, statusCode, payload, requestTime, deductErr := ps.costDeductionRequest(ctx, userID, balance, token)
				if deductErr != nil {
					logger.Error("Failed to deduct partial balance", "error", deductErr, "status_code", statusCode)
					payloadBytes, marshalErr := json.Marshal(payload)
					if marshalErr != nil {
						logger.Error("failed to marshal payload for failed partial deduction", "error", marshalErr)
						payloadBytes = []byte{}
					}
					logErr := ps.failedAAARequest(ctx, userID, balance, requestTime, statusCode, deductErr.Error(), payloadBytes)
					if logErr != nil {
						logger.Error("Failed to log failed partial deduction request", "error", logErr)
					}
					pendingDeduction = totalDeduction
				} else {
					lastSyncBalance = res.Result.UpdatedBalance
					pendingDeduction = totalDeduction - balance
					totalPaidCredit += balance
					logger.Info("Successfully deducted partial balance",
						"deducted_amount", balance, "new_balance", res.Result.UpdatedBalance, "remaining_pending", pendingDeduction)
				}
			} else {
				// User has sufficient balance - deduct the full amount
				logger.Info("User has sufficient balance, deducting full amount",
					"balance", balance, "total_deduction", totalDeduction)

				res, statusCode, payload, requestTime, deductErr := ps.costDeductionRequest(ctx, userID, totalDeduction, token)
				if deductErr != nil {
					logger.Error("Failed to deduct full amount despite sufficient balance",
						"error", deductErr, "status_code", statusCode)
					payloadBytes, marshalErr := json.Marshal(payload)
					if marshalErr != nil {
						logger.Error("failed to marshal payload for failed full deduction", "error", marshalErr)
						payloadBytes = []byte{}
					}
					logErr := ps.failedAAARequest(ctx, userID, totalDeduction, requestTime, statusCode, deductErr.Error(), payloadBytes)
					if logErr != nil {
						logger.Error("Failed to log failed full deduction request", "error", logErr)
					}
					pendingDeduction = totalDeduction
					canCreateGpuNotebook = false
				} else {
					logger.Info("Successfully deducted full amount, enabling GPU access",
						"deducted_amount", totalDeduction, "new_balance", res.Result.UpdatedBalance)
					totalPaidCredit += totalDeduction
					pendingDeduction = 0
					lastSyncBalance = res.Result.UpdatedBalance
					canCreateGpuNotebook = true
				}
			}
		}
	}

	if freshProfile.CanCreateGpuNotebook != canCreateGpuNotebook {
		if !canCreateGpuNotebook {
			logger.Info("user got their permission removed for creating notebook", "old_value", freshProfile.CanCreateGpuNotebook, "new_value", canCreateGpuNotebook)
		} else {
			logger.Info("user got their permission to create notebook", "old_value", freshProfile.CanCreateGpuNotebook, "new_value", canCreateGpuNotebook)
		}
	}

	_, err = tx.Exec(ctx, `UPDATE profiles SET
			can_create_gpu_notebook = $2,
			pending_deduction = $3,
			aaa_and_opencost_synced_at = $4,
			last_sync_balance = $5,
			total_paid_credit = $6
			WHERE user_id = $1`,
		userID, canCreateGpuNotebook, pendingDeduction, syncAtTime, lastSyncBalance, totalPaidCredit)
	if err != nil {
		logger.Error("failed to update profile in transaction", "error", err)
		return fmt.Errorf("failed to update profile in transaction: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		logger.Error("failed to commit transaction", "error", err)
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (ps *profileSync) getProfileCostFromOpenCost(lastSyncAt time.Time, endTime time.Time, profile *Profile) (float64, error) {
	logger := ps.logger.With("user_id", profile.UserID)
	end := endTime.Format(time.RFC3339)
	start := lastSyncAt.Format(time.RFC3339)

	logger.Info("fetching cost data from OpenCost",
		"start_time", start,
		"end_time", end,
		"url", ps.config.OPENCOST_URL)

	openCostURL := fmt.Sprintf("%s/allocation?window=%s,%s&aggregate=namespace&format=json",
		ps.config.OPENCOST_URL, start, end)

	client := &http.Client{Timeout: opencostTimeout}

	req, err := http.NewRequest(http.MethodGet, openCostURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %v", err)
	}

	var costData CostAllocationResponse
	var cost float64 = 0

	err = WithExternalApiRetry(ps.rootCtx, logger, func() (constants.ShouldContinue, error) {
		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("OpenCost request failed, will retry", "error", err)
			return constants.RetryContinue, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			logger.Warn("OpenCost returned non-OK status code, will retry", "status_code", resp.StatusCode)
			return constants.RetryContinue, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
			logger.Warn("failed to decode OpenCost response, will retry", "error", err)
			return constants.RetryContinue, fmt.Errorf("failed to decode cost data: %v", err)
		}
		return constants.RetryStop, nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to fetch data from OpenCost after retries: %v", err)
	}
	for _, dataMap := range costData.Data {
		for _, data := range dataMap {
			if data.Properties.Namespace == profile.UserID {
				cost = data.GPUCost
				break
			}
		}
	}

	return cost, nil
}

func (ps *profileSync) getAllProfileFromDb() ([]Profile, error) {
	ps.logger.Info("Starting to fetch profiles from database")
	var profiles []Profile

	ctx, cancel := WithTimeoutContext(ps.rootCtx, 30*time.Second)
	defer cancel()

	err := WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		rows, err := ps.pgPool.Query(ctx, `
		SELECT id, user_id, email, total_paid_credit, last_sync_balance, can_create_gpu_notebook, aaa_and_opencost_synced_at, pending_deduction
		FROM profiles`,
		)
		if err != nil {
			ps.logger.Error("Query execution failed", "error", err)
			return constants.RetryContinue, fmt.Errorf("failed to fetch profiles from DB: %v", err)
		}
		defer rows.Close()

		ps.logger.Info("Query executed successfully, processing rows")
		rowCount := 0
		for rows.Next() {
			var profile Profile
			if err := rows.Scan(&profile.ProfileID, &profile.UserID, &profile.Email,
				&profile.TotalPaidCredit, &profile.LastSyncBalance, &profile.CanCreateGpuNotebook,
				&profile.AaaAndOpenCostSyncedAt, &profile.PendingDeduction); err != nil {
				ps.logger.Error("Failed to scan row", "error", err)
				return constants.RetryContinue, fmt.Errorf("failed to scan profile from DB: %v", err)
			}
			profiles = append(profiles, profile)
			rowCount++
		}

		if err := rows.Err(); err != nil {
			ps.logger.Error("Error during row iteration", "error", err)
			return constants.RetryContinue, fmt.Errorf("failed to iterate over DB rows: %v", err)
		}

		ps.logger.Info("Successfully processed all rows", "row_count", rowCount)
		return constants.RetryStop, nil
	})

	if err != nil {
		ps.logger.Error("Failed to get profiles from database", "error", err)
		return nil, err
	}

	ps.logger.Info("Successfully fetched profiles from database", "profile_count", len(profiles))
	return profiles, err
}

func (ps *profileSync) costDeductionRequest(ctx context.Context, profileID string, cost float64, token string) (AAAResponse, int, map[string]interface{}, string, error) {
	deductionURL := fmt.Sprintf("%s/auth/v1/admin/user/credit/deduct", ps.config.AAA_URL)
	logger := ps.logger.With("user_id", profileID)

	client := &http.Client{Timeout: creditDeductionTimeout}

	var response AAAResponse
	var responseBody []byte
	var statusCode int

	tokenUsed := token
	triedRefresh := false
	var requestTime string
	var payload map[string]interface{}

	err := WithExternalApiRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		// Generate fresh timestamp for each retry attempt
		requestTime = time.Now().Format("2006-01-02T15:04:05.000")

		// Format amount to preserve decimal places with 15 decimal precision
		amountStr := fmt.Sprintf("%.15f", cost)
		amountStr = strings.TrimRight(amountStr, "0")
		if strings.HasSuffix(amountStr, ".") {
			amountStr += "0"
		}

		payload = map[string]interface{}{
			"amount":       json.Number(amountStr),
			"user_id":      profileID,
			"requested_at": requestTime,
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return constants.RetryStop, fmt.Errorf("failed to marshal deduction payload: %v", err)
		}

		req, err := http.NewRequest("PUT", deductionURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return constants.RetryStop, fmt.Errorf("failed to create deduction request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenUsed)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("credit deduction request failed, will retry", "error", err)
			statusCode = 0
			return constants.RetryContinue, err
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		responseBody, err = io.ReadAll(resp.Body)
		if err != nil {
			logger.Warn("failed to read response body, will retry", "error", err)
			return constants.RetryContinue, err
		}

		logger.Debug("AAA API response",
			"status_code", statusCode,
			"response_body", string(responseBody),
			"user_id", profileID,
			"cost", cost,
			"requested_at", requestTime)

		if statusCode == http.StatusUnauthorized {
			ps.logger.Warn("received unauthorized response, token might be expired")
			if !triedRefresh {
				ps.logger.Info("Attempting to refresh Keycloak token due to 401 Unauthorized")
				newToken, tokenErr := ps.getKeycloakToken()
				if tokenErr != nil {
					logger.Error("Failed to refresh Keycloak token", "error", tokenErr)
					return constants.RetryStop, fmt.Errorf("failed to refresh Keycloak token: %v", tokenErr)
				}
				tokenUsed = newToken
				triedRefresh = true
				return constants.RetryContinue, fmt.Errorf("retrying with refreshed token")
			}
			return constants.RetryStop, fmt.Errorf("authentication failed even after token refresh: status code %d", statusCode)
		}

		if statusCode >= 400 && statusCode < 500 {

			if parseErr := json.Unmarshal(responseBody, &response); parseErr != nil {
				ps.logger.Warn("Failed to parse error response JSON", "error", parseErr, "response_body", string(responseBody))
			}
			return constants.RetryStop, fmt.Errorf("AAA API client error: status code %d, response: %s", statusCode, string(responseBody))
		}

		if statusCode >= 500 {
			ps.logger.Warn("server error from AAA API, will retry", "status_code", statusCode)
			return constants.RetryContinue, fmt.Errorf("server error from AAA API, will retry")
		}

		return constants.RetryStop, nil
	})

	if err != nil {
		ps.logger.Error("failed to complete credit deduction request", "error", err)
		return response, statusCode, payload, requestTime, fmt.Errorf("failed to complete credit deduction request: %v", err)
	}

	if statusCode < 200 || statusCode >= 300 {
		return response, statusCode, payload, requestTime, fmt.Errorf("AAA API returned non-success status code: %d, response: %s", statusCode, string(responseBody))
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return response, statusCode, payload, requestTime, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	markErr := ps.markResolvedAAARequests(ctx, profileID)
	if markErr != nil {
		ps.logger.Error("Failed to mark AAA requests as resolved", "error", markErr)
	}

	return response, statusCode, payload, requestTime, nil
}

func (ps *profileSync) getUserBalance(ctx context.Context, userID string, token string) (float64, error) {
	balanceURL := fmt.Sprintf("%s/auth/v1/admin/user/credit/balance/%s", ps.config.AAA_URL, userID)
	logger := ps.logger.With("user_id", userID)

	client := &http.Client{Timeout: creditDeductionTimeout}

	var response BalanceResponse
	var responseBody []byte
	var statusCode int

	tokenUsed := token
	triedRefresh := false

	err := WithExternalApiRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		req, err := http.NewRequest("GET", balanceURL, nil)
		if err != nil {
			return constants.RetryStop, fmt.Errorf("failed to create balance request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenUsed)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("balance check request failed, will retry", "error", err)
			statusCode = 0
			return constants.RetryContinue, err
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		responseBody, err = io.ReadAll(resp.Body)
		if err != nil {
			logger.Warn("failed to read balance response body, will retry", "error", err)
			return constants.RetryContinue, err
		}

		logger.Info("Balance API response",
			"status_code", statusCode,
			"response_body", string(responseBody))

		if statusCode == http.StatusUnauthorized {
			ps.logger.Warn("received unauthorized response for balance check, token might be expired")
			if !triedRefresh {
				ps.logger.Info("Attempting to refresh Keycloak token for balance check due to 401 Unauthorized")
				newToken, tokenErr := ps.getKeycloakToken()
				if tokenErr != nil {
					logger.Error("Failed to refresh Keycloak token for balance check", "error", tokenErr)
					return constants.RetryStop, fmt.Errorf("failed to refresh Keycloak token: %v", tokenErr)
				}
				tokenUsed = newToken
				triedRefresh = true
				return constants.RetryContinue, fmt.Errorf("retrying balance check with refreshed token")
			}
			return constants.RetryStop, fmt.Errorf("balance check authentication failed even after token refresh: status code %d", statusCode)
		}

		if statusCode >= 400 && statusCode < 500 {
			logger.Warn("Balance API client error", "status_code", statusCode, "response_body", string(responseBody))
			return constants.RetryStop, fmt.Errorf("balance API client error: status code %d, response: %s", statusCode, string(responseBody))
		}

		if statusCode >= 500 {
			ps.logger.Warn("server error from Balance API, will retry", "status_code", statusCode)
			return constants.RetryContinue, fmt.Errorf("server error from Balance API, will retry")
		}

		return constants.RetryStop, nil
	})

	if err != nil {
		ps.logger.Error("failed to complete balance check request", "error", err)
		return 0, fmt.Errorf("failed to complete balance check request: %v", err)
	}

	if statusCode < 200 || statusCode >= 300 {
		return 0, fmt.Errorf("balance API returned non-success status code: %d, response: %s", statusCode, string(responseBody))
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal balance response: %v", err)
	}

	logger.Info("successfully retrieved user balance", "balance", response.Result.Balance)
	return response.Result.Balance, nil
}

func (ps *profileSync) deductAllRemainingCredits(ctx context.Context, userID string, balance float64, token string) (float64, error) {
	logger := ps.logger.With("user_id", userID)

	if balance <= 0 {
		logger.Info("User has zero balance, skipping credit deduction", "balance", balance)
		return 0, nil
	}

	logger.Info("Attempting to deduct all remaining credits", "balance", balance)

	res, statusCode, payload, requestTime, err := ps.costDeductionRequest(ctx, userID, balance, token)
	if err != nil {
		logger.Error("Failed to deduct all remaining credits",
			"balance", balance,
			"error", err,
			"status_code", statusCode)

		payloadBytes, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			logger.Error("failed to marshal payload for failed deduction log", "error", marshalErr)
			payloadBytes = []byte{}
		}

		logErr := ps.failedAAARequest(
			ctx,
			userID,
			balance,
			requestTime,
			statusCode,
			err.Error(),
			payloadBytes,
		)
		if logErr != nil {
			logger.Error("Failed to log failed balance deduction request", "error", logErr)
		}

		return 0, fmt.Errorf("failed to deduct all remaining credits: %v", err)
	} else {
		logger.Info("Successfully deducted all remaining credits", "deducted_amount", balance, "new_balance", res.Result.UpdatedBalance)
		return res.Result.UpdatedBalance, nil
	}
}

func (ps *profileSync) getKeycloakToken() (string, error) {
	ps.logger.Info("getting Keycloak token")

	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", ps.config.KEYCLOAK_CLIENT_ID)
	data.Set("username", ps.config.KEYCLOAK_USERNAME)
	data.Set("password", ps.config.KEYCLOAK_PASSWORD)

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		ps.config.KEYCLOAK_URL, ps.config.KEYCLOAK_REALM)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: keycloakTokenTimeout}

	var tokenResponse KeycloakTokenResponse
	var statusCode int

	err = WithExternalApiRetry(ps.rootCtx, ps.logger, func() (constants.ShouldContinue, error) {
		resp, err := client.Do(req)
		if err != nil {
			ps.logger.Warn("Keycloak token request failed, will retry", "error", err)
			return constants.RetryContinue, err
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		if statusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ps.logger.Warn("Keycloak returned non-OK status code, will retry",
				"status_code", statusCode,
				"body", string(body))
			return constants.RetryContinue, fmt.Errorf("unexpected status code: %d", statusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
			ps.logger.Warn("failed to decode token response, will retry", "error", err)
			return constants.RetryContinue, err
		}

		return constants.RetryStop, nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to get Keycloak token after retries: %v", err)
	}

	if tokenResponse.AccessToken == "" {
		return "", fmt.Errorf("empty access token received from Keycloak")
	}

	ps.logger.Info("successfully obtained Keycloak token")
	return tokenResponse.AccessToken, nil
}
