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
	"sync"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

func main() {
	logLevel := slog.LevelInfo
	if err := godotenv.Load(); err != nil {
		utils.LogErrorAndExit(slog.Default(), "failed to load environment variables", "error", err)
	}

	var config CronEnv
	if err := env.Parse(&config); err != nil {
		utils.LogErrorAndExit(slog.Default(), "failed to parse environment variables", "error", err)
	}

	logLevelStr := os.Getenv("PROFILE_CREDIT_SYNC_LOG_LEVEL")
	if logLevelStr == "debug" {
		logLevel = slog.LevelDebug
		slog.Info("Setting log level to DEBUG")
	} else {
		slog.Info("Setting log level to INFO")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	logger = logger.With("hostname", os.Getenv("HOSTNAME"))

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
		logger.Info("adding missing profiles to DB", "count", len(k8sOrphans))
		for _, orphan := range k8sOrphans {
			err := profileSync.addProfileToDb(orphan)
			if err != nil {
				logger.Error("failed to add profile to DB", "user_id", orphan.UserID, "email", orphan.Email, "error", err)
			} else {
				logger.Info("successfully added profile to DB", "user_id", orphan.UserID, "email", orphan.Email)
			}
		}
	}

	// Add missing profiles to K8s
	if len(dbOrphans) > 0 {
		logger.Info("adding missing profiles to K8s", "count", len(dbOrphans))
		for _, orphan := range dbOrphans {
			err := profileSync.addProfileToK8s(orphan)
			if err != nil {
				logger.Error("failed to add profile to K8s", "user_id", orphan.UserID, "error", err)
				// Update orphan_profiles table to track profiles missing from K8s
				err = profileSync.updateOrphanProfile(orphan, true)
				if err != nil {
					logger.Error("failed to update orphan profile", "user_id", orphan.UserID, "error", err)
				}
			} else {
				logger.Info("successfully added profile to K8s", "user_id", orphan.UserID)
				// Update orphan_profiles table to mark profile as no longer missing from K8s
				err = profileSync.updateOrphanProfile(orphan, false)
				if err != nil {
					logger.Error("failed to update orphan profile", "user_id", orphan.UserID, "error", err)
				}
			}
		}
	}

	if len(k8sOrphans) > 0 {
		logger.Warn("found orphan profiles in K8s", "k8s_orphans_count", len(k8sOrphans))

		for _, orphan := range k8sOrphans {
			logger.Error("orphaned K8s profile",
				"user_id", orphan.UserID,
				"email", orphan.Email)
		}

		filteredK8sProfiles := make([]KubeflowProfile, 0, len(k8sProfiles)-len(k8sOrphans))
		for _, profile := range k8sProfiles {
			isOrphan := false
			for _, orphan := range k8sOrphans {
				if profile.UserID == orphan.UserID {
					isOrphan = true
					break
				}
			}
			if !isOrphan {
				filteredK8sProfiles = append(filteredK8sProfiles, profile)
			}
		}
		k8sProfiles = filteredK8sProfiles
		logger.Info("filtered out orphaned K8s profiles", "remaining_profiles", len(k8sProfiles))
	}

	if len(dbOrphans) > 0 {
		logger.Warn("found orphan profiles in DB", "db_orphans_count", len(dbOrphans))

		for _, orphan := range dbOrphans {
			logger.Error("orphaned DB profile",
				"user_id", orphan.UserID,
				"profile_id", orphan.ProfileID)
		}
	}

	if len(k8sOrphans) == 0 && len(dbOrphans) == 0 {
		logger.Info("no orphan profiles found, counts match", "profile_count", len(dbProfiles))
	}
	if len(dbProfiles) == 0 {
		logger.Info("no profiles found in DB")
		return
	}

	token, err := profileSync.getKeycloakToken()
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get Keycloak token", "error", err)
	}

	logger.Info("successfully obtained Keycloak token")

	if rootCtx.Err() != nil {
		logger.Info("shutting down after obtaining Keycloak token")
		return
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
	ps.logger.Info("processing profiles with concurrency", "count", len(dbProfiles), "concurrency_limit", ps.config.MaxProfileCanSyncAtOnce)

	if ps.rootCtx.Err() != nil {
		ps.logger.Info("shutdown requested before starting profile processing")
		return nil
	}

	semaphore := make(chan struct{}, ps.config.MaxProfileCanSyncAtOnce)
	wg := sync.WaitGroup{}

	for _, profile := range k8sProfiles {
		select {
		case <-ps.rootCtx.Done():
			ps.logger.Warn("context cancelled during profile processing")
			return nil
		case semaphore <- struct{}{}:
			profileFromDb := findProfile(dbProfiles, profile.UserID)
			if profileFromDb == nil {
				ps.logger.Error("profile not found in database", "user_id", profile.UserID)
				<-semaphore
				continue
			}
			wg.Add(1)
			go func(profile KubeflowProfile, profileFromDb *Profile) {
				defer func() {
					wg.Done()
					<-semaphore
				}()
				logger := ps.logger.With("user_id", profile.UserID)
				now := time.Now()
				cost, err := ps.getProfileCostFromOpenCost(profileFromDb.AaaAndOpenCostSyncedAt, now, profileFromDb)
				if err != nil {
					logger.Error("failed to get profile cost", "error", err)
					return
				}
				ctx := context.Background()
				res, err := ps.costDeductionRequest(ctx, profile.UserID, cost, token)
				logger.Info("cost deduction request response", "response", res)
				if err != nil {
					requestTime := time.Now().UTC().Format("2006-01-02T15:04:05")
					payload, payloadErr := getRequestPayload(cost, profile.UserID, requestTime)
					if payloadErr != nil {
						logger.Error("failed to create payload for failed AAA request", "error", payloadErr)
					}

					errorMessage := err.Error()
					statusCode := 0

					if serverErr, ok := err.(*ServerError); ok {
						statusCode = serverErr.StatusCode
						errorMessage = serverErr.Message
						logger.Error("server error from AAA API, logging to failed_aaa_requests",
							"error", serverErr,
							"status_code", serverErr.StatusCode)
					} else {
						logger.Error("failed to deduct cost, logging to failed_aaa_requests", "error", err)
					}
					logErr := ps.failedAAARequest(
						ctx,
						profile.UserID,
						cost,
						requestTime,
						statusCode,
						errorMessage,
						payload,
					)
					if logErr != nil {
						logger.Error("Failed to log failed AAA request", "error", logErr)
					}
					err := WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
						_, err = ps.pgPool.Exec(ctx, `UPDATE profiles 
						SET aaa_and_opencost_synced_at = $2
						WHERE user_id = $1`, profile.UserID, now)
						if err != nil {
							logger.Error("failed to update sync timestamp", "error", err)
							return constants.RetryContinue, err
						}
						return constants.RetryStop, nil
					})

					if err != nil {
						logger.Error("failed to update sync timestamp", "error", err)
					}
					if profileFromDb.CanCreateGpuNotebook {
						err := ps.stopAllNotebooksInNamespace(ctx, profileFromDb.UserID)
						if err != nil {
							logger.Error("failed to stop notebooks", "error", err)
						}
					}
					return
				}
				err = WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
					_, err = ps.pgPool.Exec(ctx, `UPDATE profiles SET 
					can_create_gpu_notebook = true, 
					pending_deduction = 0, 
					aaa_and_opencost_synced_at = $2, 
					last_sync_balance = $3, 
					total_credit = total_credit + $4
					WHERE user_id = $1`, profile.UserID, now, res.Result.UpdatedBalance, cost)
					if err != nil {
						logger.Error("failed to update can_create_gpu_notebook flag after deduction", "error", err)
						return constants.RetryContinue, err
					}
					return constants.RetryStop, nil
				})
				if err != nil {
					logger.Error("error updating DB after successful deduction", "error", err)
					return
				}
			}(profile, profileFromDb)
		}
	}

	wg.Wait()

	ps.logger.Info("profile sync completed successfully")
	return nil
}

func (ps *profileSync) getProfileCostFromOpenCost(last_sync_at time.Time, endTime time.Time, profile *Profile) (float64, error) {
	logger := ps.logger.With("user_id", profile.UserID)
	end := endTime.Format(time.RFC3339)
	start := last_sync_at.Format(time.RFC3339)

	logger.Info("fetched cost data from OpenCost",
		"start_time", start,
		"end_time", end,
		"url", ps.config.OpenCostURL)

	openCostURL := fmt.Sprintf("%s/allocation?window=%s,%s&aggregate=namespace&format=json",
		ps.config.OpenCostURL, start, end)

	client := &http.Client{Timeout: opencostTimeout}

	req, err := http.NewRequest(http.MethodGet, openCostURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %v", err)
	}

	var costData CostAllocationResponse
	var cost float64 = 0

	err = WithExternalApiRetry(ps.rootCtx, ps.logger, func() (constants.ShouldContinue, error) {
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
				cost = data.TotalCost
				break
			}
		}
	}

	return cost, nil
}

func findProfile(profiles []Profile, userId string) *Profile {
	for _, profile := range profiles {
		if profile.UserID == userId {
			return &profile
		}
	}
	return nil
}

func (ps *profileSync) getAllProfileFromDb() ([]Profile, error) {
	ps.logger.Info("Starting to fetch profiles from database")
	var profiles []Profile

	ctx, cancel := WithTimeoutContext(ps.rootCtx, 30*time.Second)
	defer cancel()

	err := WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		rows, err := ps.pgPool.Query(ctx, `
		SELECT id, user_id, aaa_and_opencost_synced_at, total_credit, last_sync_balance, can_create_gpu_notebook, pending_deduction
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
			if err := rows.Scan(&profile.ProfileID, &profile.UserID, &profile.AaaAndOpenCostSyncedAt,
				&profile.TotalCredit, &profile.LastSyncBalance, &profile.CanCreateGpuNotebook, &profile.PendingDeduction); err != nil {
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

func (ps *profileSync) costDeductionRequest(ctx context.Context, profileID string, cost float64, token string) (*AAAResponse, error) {
	requestTime := time.Now().UTC().Format("2006-01-02T15:04:05")
	deductionURL := fmt.Sprintf("%s/auth/v1/admin/user/credit/deduct", ps.config.AAA_URL)
	payload := map[string]interface{}{
		"amount":       cost,
		"user_id":      profileID,
		"requested_at": requestTime,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal deduction payload: %v", err)
	}

	req, err := http.NewRequest("PUT", deductionURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create deduction request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: creditDeductionTimeout}

	var response AAAResponse
	var responseBody []byte
	var statusCode int

	err = WithExternalApiRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		resp, err := client.Do(req)
		if err != nil {
			ps.logger.Warn("credit deduction request failed, will retry", "error", err)
			return constants.RetryContinue, &ServerError{
				StatusCode: 0,
				Message:    err.Error(),
			}
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		responseBody, err = io.ReadAll(resp.Body)
		if err != nil {
			ps.logger.Warn("failed to read response body, will retry", "error", err)
			return constants.RetryContinue, err
		}

		ps.logger.Debug("AAA API response",
			"status_code", statusCode,
			"response_body", string(responseBody),
			"user_id", profileID,
			"cost", cost,
			"requested_at", requestTime)

		if statusCode == http.StatusUnauthorized {
			ps.logger.Warn("received unauthorized response, token might be expired")
			return constants.RetryStop, nil
		}

		if statusCode >= 400 && statusCode < 500 {
			ps.logger.Error("AAA API client error",
				"status_code", statusCode,
				"response_body", string(responseBody),
				"user_id", profileID,
				"cost", cost,
				"requested_at", requestTime,
				"payload", string(payloadBytes))
			return constants.RetryStop, nil
		}

		if statusCode >= 500 {
			ps.logger.Warn("server error from AAA API, will retry", "status_code", statusCode)
			return constants.RetryContinue, &ServerError{
				StatusCode: statusCode,
				Message:    string(responseBody),
			}
		}

		return constants.RetryStop, nil
	})

	if err != nil {
		if serverErr, ok := err.(*ServerError); ok {
			logErr := ps.failedAAARequest(
				ctx,
				profileID,
				cost,
				requestTime,
				serverErr.StatusCode,
				serverErr.Message,
				payloadBytes,
			)
			if logErr != nil {
				ps.logger.Error("Failed to log failed AAA request", "error", logErr)
			}
		}
		return nil, fmt.Errorf("failed to complete credit deduction request after retries: %v", err)
	}

	if statusCode == http.StatusUnauthorized {
		ps.logger.Info("unauthorized response received, refreshing token and retrying")

		newToken, err := ps.getKeycloakToken()
		if err != nil {
			return nil, fmt.Errorf("failed to refresh token: %v", err)
		}

		req, err = http.NewRequest("PUT", deductionURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request with new token: %v", err)
		}

		req.Header.Set("Authorization", "Bearer "+newToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request with new token failed: %v", err)
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		responseBody, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body with new token: %v", err)
		}

		// Log response details after token refresh
		ps.logger.Debug("AAA API response after token refresh",
			"status_code", statusCode,
			"response_body", string(responseBody),
			"user_id", profileID,
			"cost", cost,
			"requested_at", requestTime)

		if statusCode >= 400 {
			ps.logger.Error("AAA API error after token refresh",
				"status_code", statusCode,
				"response_body", string(responseBody),
				"user_id", profileID,
				"cost", cost,
				"requested_at", requestTime,
				"payload", string(payloadBytes))
		}

		if statusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("still unauthorized after token refresh")
		}
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	if statusCode >= 200 && statusCode < 300 {
		markErr := ps.markResolvedAAARequests(ctx, profileID)
		if markErr != nil {
			ps.logger.Error("Failed to mark AAA requests as resolved", "error", markErr)
		}
	}

	return &response, nil
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
