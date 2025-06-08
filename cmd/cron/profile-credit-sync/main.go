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
	"sandbox-backend-service/pkg/db"
	"sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
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

	if rootCtx.Err() != nil {
		logger.Info("shutting down before processing started")
		return
	}

	profiles, err := profileSync.getAllProfiles()
	if err != nil {
		utils.LogErrorAndExit(logger, "failed to get profiles from Kubernetes", "error", err)
	}

	logger.Info("successfully retrieved profiles from Kubernetes", "count", len(profiles))

	if rootCtx.Err() != nil {
		logger.Info("shutting down after retrieving profiles")
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
	if err := profileSync.processProfiles(profiles, token); err != nil {
		utils.LogErrorAndExit(logger, "failed to process profiles", "error", err)
	}

	logger.Info("profile credit sync completed successfully")
}

func (ps *profileSync) getAllProfiles() ([]KubeflowProfile, error) {

	profileGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}
	profileList := &unstructured.UnstructuredList{}
	WithK8sRetry(ps.rootCtx, func() error {
		var err error
		profileList, err = ps.dynamicClient.Dynamic.Resource(profileGVR).List(ps.rootCtx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("failed to list profiles: %v", err)
		}
		return nil
	})
	profiles := make([]KubeflowProfile, 0, len(profileList.Items))

	for _, item := range profileList.Items {
		userId := item.GetName()

		if _, err := uuid.Parse(userId); err != nil {
			ps.logger.Warn("skipping invalid profile ID - must be UUID",
				"profile_id", userId,
				"error", err)
			continue
		}

		ownerEmail, found, err := unstructured.NestedString(item.Object, "spec", "owner", "name")
		if err != nil || !found {
			ps.logger.Warn("could not extract owner email from profile",
				"user_id", userId,
				"error", err)
			continue
		}

		profiles = append(profiles, KubeflowProfile{
			UserID: userId,
			Email:  ownerEmail,
		})
	}

	ps.logger.Info("found profiles in Kubernetes", "count", len(profiles))
	return profiles, nil
}

func (ps *profileSync) processProfiles(profiles []KubeflowProfile, token string) error {
	ps.logger.Info("processing profiles with concurrency", "count", len(profiles), "concurrency_limit", ps.config.MaxProfileCanSyncAtOnce)

	if ps.rootCtx.Err() != nil {
		ps.logger.Info("shutdown requested before starting profile processing")
		return nil
	}

	semaphore := make(chan struct{}, ps.config.MaxProfileCanSyncAtOnce)
	wg := sync.WaitGroup{}
	getAllProfileFromDb, err := ps.getAllProfileFromDb()
	if err != nil {
		ps.logger.Error("failed to fetch profiles from DB", "error", err)
		return err
	}

	for _, profile := range profiles {
		select {
		case <-ps.rootCtx.Done():
			ps.logger.Warn("context cancelled during profile processing")
			return nil
		case semaphore <- struct{}{}:
			profileFromDb := findProfile(getAllProfileFromDb, profile.UserID)
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
				if err != nil {
					logger.Error("failed to deduct cost", "error", err)
					WithDBRetry(ctx, func() error {
						_, err = ps.pgPool.Exec(ctx, `UPDATE profile 
						SET pending_deduction = pending_deduction + $2, 
						aaa_and_opencost_synced_at = $3,
						can_create_notebook = false 
						WHERE user_id = $1`, profile.UserID, cost, now)
						if err != nil {
							logger.Error("failed to update pending deduction", "error", err)
							return err
						}
						if profileFromDb.CanCreateNotebook {
							ps.stopAllNotebooksInNamespace(ctx, profileFromDb.UserID)
						}
						return nil
					})
					return
				}
				err = WithDBRetry(ctx, func() error {
					_, err = ps.pgPool.Exec(ctx, `UPDATE profile SET 
					can_create_notebook = true, 
					pending_deduction = 0, 
					aaa_and_opencost_synced_at = $2, 
					last_sync_balance = $3 
					total_credit = total_credit + $4
					WHERE user_id = $1`, profile.UserID, now, res.Result.UpdatedBalance, cost)
					if err != nil {
						logger.Error("failed to update can_create_notebook flag after deduction", "error", err)
						return err
					}
					return nil
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

func (ps *profileSync) getProfileCostFromOpenCost(last_sync_at time.Time, start_time time.Time, profile *Profile) (float64, error) {
	logger := ps.logger.With("profile_id", profile.UserID)
	end := last_sync_at.Format(time.RFC3339)
	start := start_time.Format(time.RFC3339)

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

	err = WithExternalApiRetry(ps.rootCtx, func() error {
		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("OpenCost request failed, will retry", "error", err)
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			logger.Warn("OpenCost returned non-OK status code, will retry", "status_code", resp.StatusCode)
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
			logger.Warn("failed to decode OpenCost response, will retry", "error", err)
			return fmt.Errorf("failed to decode cost data: %v", err)
		}
		return nil
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
	var profiles []Profile
	WithDBRetry(ps.rootCtx, func() error {
		err := ps.pgPool.QueryRow(ps.rootCtx, `
		SELECT profile_id, user_id, aaa_and_opencost_synced_at, total_credit, last_sync_balance, can_create_notebook, pending_deduction
		FROM profile`,
		).Scan(&profiles)
		if err != nil {
			return err
		}
		return nil
	})
	return profiles, nil
}

func (ps *profileSync) costDeductionRequest(ctx context.Context, profileID string, cost float64, token string) (*AAAResponse, error) {
	requestTime := time.Now().UTC().Format(time.RFC3339)
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

	err = WithExternalApiRetry(ctx, func() error {
		resp, err := client.Do(req)
		if err != nil {
			ps.logger.Warn("credit deduction request failed, will retry", "error", err)
			return err
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		responseBody, err = io.ReadAll(resp.Body)
		if err != nil {
			ps.logger.Warn("failed to read response body, will retry", "error", err)
			return err
		}

		if statusCode == http.StatusUnauthorized {
			ps.logger.Warn("received unauthorized response, token might be expired")
			return nil
		}

		if statusCode >= 400 && statusCode < 500 {
			return nil
		}

		if statusCode >= 500 {
			ps.logger.Warn("server error from AAA API, will retry", "status_code", statusCode)
			return fmt.Errorf("server error: %d", statusCode)
		}

		return nil
	})

	if err != nil {
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

		if statusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("still unauthorized after token refresh")
		}
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
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

	client := &http.Client{Timeout: 10 * time.Second}

	var tokenResponse KeycloakTokenResponse
	var statusCode int

	err = WithExternalApiRetry(context.Background(), func() error {
		resp, err := client.Do(req)
		if err != nil {
			ps.logger.Warn("Keycloak token request failed, will retry", "error", err)
			return err
		}
		defer resp.Body.Close()

		statusCode = resp.StatusCode
		if statusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ps.logger.Warn("Keycloak returned non-OK status code, will retry",
				"status_code", statusCode,
				"body", string(body))
			return fmt.Errorf("unexpected status code: %d", statusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
			ps.logger.Warn("failed to decode token response, will retry", "error", err)
			return err
		}

		return nil
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
