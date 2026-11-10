package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type config struct {
	TokenURL             string
	ClientID             string
	ClientSecretFile     string
	RefreshTokenFile     string
	BootstrapTokenFile   string
	AccessTokenFile      string
	StatusFile           string
	ReadyAddress         string
	TokenSessionURL      string
	ExpectedUserID       string
	ExpectedClientID     string
	CheckInterval        time.Duration
	SecretWaitInterval   time.Duration
	RefreshRetryInterval time.Duration
	RefreshSkew          time.Duration
	RequestTimeout       time.Duration
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

type accessTokenClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string `json:"azp"`
}

type tokenBootstrap struct {
	SessionID   string `json:"sessionId"`
	AccessToken string `json:"accessToken"`
}

type tokenStatus struct {
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := loadConfig()
	if err := run(context.Background(), cfg, logger); err != nil {
		logger.Error("platform token sidecar stopped", "error", err)
		os.Exit(1)
	}
}

func loadConfig() config {
	checkInterval := durationFromEnv("CHECK_INTERVAL_SECONDS", 30) * time.Second
	secretWaitInterval := durationFromEnv("SECRET_WAIT_INTERVAL_SECONDS", 1) * time.Second
	refreshRetryInterval := durationFromEnv("REFRESH_RETRY_INTERVAL_SECONDS", 5) * time.Second
	refreshSkew := durationFromEnv("REFRESH_SKEW_SECONDS", 60) * time.Second
	requestTimeout := durationFromEnv("REQUEST_TIMEOUT_SECONDS", 10) * time.Second
	refreshTokenFile := strings.TrimSpace(os.Getenv("REFRESH_TOKEN_FILE"))
	bootstrapTokenFile := strings.TrimSpace(os.Getenv("BOOTSTRAP_TOKEN_FILE"))
	if bootstrapTokenFile == "" && refreshTokenFile != "" {
		bootstrapTokenFile = filepath.Join(filepath.Dir(refreshTokenFile), "bootstrap.json")
	}
	accessTokenFile := strings.TrimSpace(os.Getenv("ACCESS_TOKEN_FILE"))
	if accessTokenFile == "" {
		accessTokenFile = "/var/run/sandbox-connect/platform/token"
	}
	statusFile := strings.TrimSpace(os.Getenv("STATUS_FILE"))
	if statusFile == "" {
		statusFile = filepath.Join(filepath.Dir(accessTokenFile), "status.json")
	}
	readyAddress := strings.TrimSpace(os.Getenv("READY_ADDRESS"))
	if readyAddress == "" {
		readyAddress = "0.0.0.0:8081"
	}
	return config{
		TokenURL:             strings.TrimSpace(os.Getenv("KEYCLOAK_TOKEN_URL")),
		ClientID:             strings.TrimSpace(os.Getenv("KEYCLOAK_CLIENT_ID")),
		ClientSecretFile:     strings.TrimSpace(os.Getenv("KEYCLOAK_CLIENT_SECRET_FILE")),
		RefreshTokenFile:     refreshTokenFile,
		BootstrapTokenFile:   bootstrapTokenFile,
		AccessTokenFile:      accessTokenFile,
		StatusFile:           statusFile,
		ReadyAddress:         readyAddress,
		TokenSessionURL:      strings.TrimSpace(os.Getenv("TOKEN_SESSION_URL")),
		ExpectedUserID:       strings.TrimSpace(os.Getenv("EXPECTED_USER_ID")),
		ExpectedClientID:     strings.TrimSpace(os.Getenv("EXPECTED_CLIENT_ID")),
		CheckInterval:        checkInterval,
		SecretWaitInterval:   secretWaitInterval,
		RefreshRetryInterval: refreshRetryInterval,
		RefreshSkew:          refreshSkew,
		RequestTimeout:       requestTimeout,
	}
}

func durationFromEnv(name string, fallback int) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return time.Duration(fallback)
	}
	return time.Duration(value)
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	if cfg.TokenURL == "" {
		return errors.New("KEYCLOAK_TOKEN_URL is required")
	}
	if cfg.ClientID == "" {
		return errors.New("KEYCLOAK_CLIENT_ID is required")
	}
	if cfg.ClientSecretFile == "" {
		return errors.New("KEYCLOAK_CLIENT_SECRET_FILE is required")
	}
	if cfg.RefreshTokenFile == "" {
		return errors.New("REFRESH_TOKEN_FILE is required")
	}
	if cfg.BootstrapTokenFile == "" {
		return errors.New("BOOTSTRAP_TOKEN_FILE is required")
	}
	if cfg.ReadyAddress == "" {
		return errors.New("READY_ADDRESS is required")
	}
	if cfg.TokenSessionURL == "" {
		return errors.New("TOKEN_SESSION_URL is required")
	}
	if cfg.ExpectedUserID == "" {
		return errors.New("EXPECTED_USER_ID is required")
	}
	if cfg.ExpectedClientID == "" {
		return errors.New("EXPECTED_CLIENT_ID is required")
	}

	readyServer := &http.Server{
		Addr:              cfg.ReadyAddress,
		Handler:           readinessHandler(cfg),
		ReadHeaderTimeout: 2 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if err := readyServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = readyServer.Shutdown(shutdownCtx)
	}()

	var currentRefreshToken string
	var lastMountedRefreshToken string
	var currentSessionID string
	var lastMountedSessionID string
	for {
		if err := refreshIfNeeded(ctx, cfg, logger, &currentRefreshToken, &lastMountedRefreshToken, &currentSessionID, &lastMountedSessionID); err != nil {
			logger.Warn("token refresh iteration failed", "error", err)
		}

		timer := time.NewTimer(nextCheckInterval(cfg))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case err := <-serverErrors:
			timer.Stop()
			return fmt.Errorf("readiness server stopped: %w", err)
		case <-timer.C:
		}
	}
}

func nextCheckInterval(cfg config) time.Duration {
	refreshToken, _ := readTrimmedFile(cfg.RefreshTokenFile)
	if refreshToken == "" {
		return cfg.SecretWaitInterval
	}
	accessToken, _ := readTrimmedFile(cfg.AccessTokenFile)
	if accessToken == "" {
		return cfg.RefreshRetryInterval
	}
	return cfg.CheckInterval
}

func refreshIfNeeded(ctx context.Context, cfg config, logger *slog.Logger, currentRefreshToken, lastMountedRefreshToken, currentSessionID, lastMountedSessionID *string) error {
	mountedRefreshToken, err := readTrimmedFile(cfg.RefreshTokenFile)
	if err != nil || mountedRefreshToken == "" {
		removeIfExists(cfg.AccessTokenFile)
		_ = writeStatus(cfg.StatusFile, "waiting_for_refresh_token", "")
		*currentRefreshToken = ""
		*lastMountedRefreshToken = ""
		*currentSessionID = ""
		*lastMountedSessionID = ""
		return nil
	}
	if *currentRefreshToken == "" || mountedRefreshToken != *lastMountedRefreshToken {
		*currentRefreshToken = mountedRefreshToken
		*lastMountedRefreshToken = mountedRefreshToken
	}

	bootstrap, bootstrapErr := readTokenBootstrap(cfg.BootstrapTokenFile)
	mountedSessionID := ""
	if bootstrapErr == nil {
		mountedSessionID = strings.TrimSpace(bootstrap.SessionID)
	}
	if mountedSessionID != "" && mountedSessionID != *lastMountedSessionID {
		*currentSessionID = mountedSessionID
		*lastMountedSessionID = mountedSessionID
		removeIfExists(cfg.AccessTokenFile)
		_ = writeStatus(cfg.StatusFile, "publishing_bootstrap_token", mountedSessionID)

		bootstrapAccessToken := strings.TrimSpace(bootstrap.AccessToken)
		if validateAccessTokenIdentity(bootstrapAccessToken, cfg.ExpectedUserID, cfg.ExpectedClientID) == nil &&
			!tokenNeedsRefresh(bootstrapAccessToken, cfg.RefreshSkew) {
			if err := atomicWrite(cfg.AccessTokenFile, bootstrapAccessToken+"\n", 0644); err != nil {
				return err
			}
			_ = writeStatus(cfg.StatusFile, "ready", mountedSessionID)
			logger.Info("published bootstrap platform access token", "session_id", mountedSessionID)
			return nil
		}
		logger.Warn("bootstrap access token is not usable; refreshing it", "session_id", mountedSessionID)
	}

	accessToken, _ := readTrimmedFile(cfg.AccessTokenFile)
	if accessToken != "" &&
		validateAccessTokenIdentity(accessToken, cfg.ExpectedUserID, cfg.ExpectedClientID) == nil &&
		!tokenNeedsRefresh(accessToken, cfg.RefreshSkew) {
		_ = writeStatus(cfg.StatusFile, "ready", *currentSessionID)
		return nil
	}

	resp, err := requestAccessToken(ctx, cfg, *currentRefreshToken)
	if err != nil {
		removeIfExists(cfg.AccessTokenFile)
		_ = writeStatus(cfg.StatusFile, "refresh_failed", *currentSessionID)
		return err
	}
	accessToken = strings.TrimSpace(resp.AccessToken)
	if accessToken == "" {
		removeIfExists(cfg.AccessTokenFile)
		_ = writeStatus(cfg.StatusFile, "refresh_failed", *currentSessionID)
		return errors.New("token endpoint returned empty access token")
	}
	if err := validateAccessTokenIdentity(accessToken, cfg.ExpectedUserID, cfg.ExpectedClientID); err != nil {
		removeIfExists(cfg.AccessTokenFile)
		_ = writeStatus(cfg.StatusFile, "identity_validation_failed", *currentSessionID)
		return err
	}

	rotatedRefreshToken := strings.TrimSpace(resp.RefreshToken)
	if rotatedRefreshToken != "" && rotatedRefreshToken != *currentRefreshToken {
		*currentRefreshToken = rotatedRefreshToken
		if err := persistRefreshToken(ctx, cfg, accessToken, rotatedRefreshToken); err != nil {
			removeIfExists(cfg.AccessTokenFile)
			_ = writeStatus(cfg.StatusFile, "refresh_persist_failed", *currentSessionID)
			return err
		}
	}
	if err := atomicWrite(cfg.AccessTokenFile, accessToken+"\n", 0644); err != nil {
		return err
	}
	_ = writeStatus(cfg.StatusFile, "ready", *currentSessionID)
	logger.Info("refreshed platform access token", "expires_in", resp.ExpiresIn, "refresh_expires_in", resp.RefreshExpiresIn)
	return nil
}

func readTokenBootstrap(path string) (*tokenBootstrap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bootstrap tokenBootstrap
	if err := json.Unmarshal(data, &bootstrap); err != nil {
		return nil, err
	}
	bootstrap.SessionID = strings.TrimSpace(bootstrap.SessionID)
	bootstrap.AccessToken = strings.TrimSpace(bootstrap.AccessToken)
	if bootstrap.SessionID == "" || bootstrap.AccessToken == "" {
		return nil, errors.New("bootstrap token file is incomplete")
	}
	return &bootstrap, nil
}

func requestAccessToken(ctx context.Context, cfg config, refreshToken string) (*tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	clientSecret, err := readTrimmedFile(cfg.ClientSecretFile)
	if err != nil || clientSecret == "" {
		return nil, errors.New("read Keycloak client secret")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", cfg.ClientID)
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(cfg.ClientID, clientSecret)

	client := &http.Client{
		Timeout: cfg.RequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var parsed tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func validateAccessTokenIdentity(tokenString, expectedUserID, expectedClientID string) error {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	claims := &accessTokenClaims{}
	_, _, err := parser.ParseUnverified(tokenString, claims)
	if err != nil {
		return fmt.Errorf("parse refreshed access token: %w", err)
	}
	if claims.Subject != expectedUserID {
		return errors.New("refreshed access token subject does not match notebook owner")
	}
	if claims.AuthorizedParty != expectedClientID {
		return errors.New("refreshed access token client does not match notebook client")
	}
	if claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) <= 0 {
		return errors.New("refreshed access token is expired or missing expiry")
	}
	return nil
}

func persistRefreshToken(ctx context.Context, cfg config, accessToken, refreshToken string) error {
	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPut, cfg.TokenSessionURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: cfg.RequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("persist rotated refresh token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("persist rotated refresh token returned status %d", resp.StatusCode)
	}
	return nil
}

func tokenNeedsRefresh(tokenString string, skew time.Duration) bool {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	claims := &accessTokenClaims{}
	_, _, err := parser.ParseUnverified(tokenString, claims)
	if err != nil || claims.ExpiresAt == nil {
		return true
	}
	return time.Until(claims.ExpiresAt.Time) <= skew
}

func readinessHandler(cfg config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		expectedSessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		if expectedSessionID == "" {
			http.Error(w, "sessionId is required", http.StatusBadRequest)
			return
		}

		statusData, err := os.ReadFile(cfg.StatusFile)
		if err != nil {
			http.Error(w, "token is not ready", http.StatusServiceUnavailable)
			return
		}
		var status tokenStatus
		if err := json.Unmarshal(statusData, &status); err != nil || status.Status != "ready" || status.SessionID != expectedSessionID {
			http.Error(w, "token is not ready", http.StatusServiceUnavailable)
			return
		}
		accessToken, err := readTrimmedFile(cfg.AccessTokenFile)
		if err != nil || validateAccessTokenIdentity(accessToken, cfg.ExpectedUserID, cfg.ExpectedClientID) != nil || tokenNeedsRefresh(accessToken, cfg.RefreshSkew) {
			http.Error(w, "token is not ready", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenStatus{Status: "ready", SessionID: status.SessionID, UpdatedAt: status.UpdatedAt})
	})
	return mux
}

func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func atomicWrite(path, value string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeStatus(path, status, sessionID string) error {
	data, err := json.Marshal(tokenStatus{
		Status:    status,
		SessionID: strings.TrimSpace(sessionID),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return atomicWrite(path, string(data)+"\n", 0644)
}

func removeIfExists(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove access token file", "error", err)
	}
}
