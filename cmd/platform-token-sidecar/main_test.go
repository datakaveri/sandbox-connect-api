package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestTokenNeedsRefresh(t *testing.T) {
	fresh := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
	})
	freshString, err := fresh.SignedString([]byte("test"))
	if err != nil {
		t.Fatalf("failed to sign fresh token: %v", err)
	}
	if tokenNeedsRefresh(freshString, time.Minute) {
		t.Fatal("fresh token should not need refresh")
	}

	stale := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * time.Second)),
	})
	staleString, err := stale.SignedString([]byte("test"))
	if err != nil {
		t.Fatalf("failed to sign stale token: %v", err)
	}
	if !tokenNeedsRefresh(staleString, time.Minute) {
		t.Fatal("stale token should need refresh")
	}
	if !tokenNeedsRefresh("not-a-jwt", time.Minute) {
		t.Fatal("invalid token should need refresh")
	}
}

func TestAtomicWriteUsesRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := atomicWrite(path, "token\n", 0644); err != nil {
		t.Fatalf("atomicWrite failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("token file mode = %v, want 0644", got)
	}
}

func TestNextCheckIntervalAdaptsToTokenReadiness(t *testing.T) {
	dir := t.TempDir()
	cfg := config{
		RefreshTokenFile:     filepath.Join(dir, "refresh"),
		AccessTokenFile:      filepath.Join(dir, "access"),
		SecretWaitInterval:   time.Second,
		RefreshRetryInterval: 5 * time.Second,
		CheckInterval:        30 * time.Second,
	}

	if got := nextCheckInterval(cfg); got != time.Second {
		t.Fatalf("interval while waiting for secret = %v, want 1s", got)
	}
	if err := os.WriteFile(cfg.RefreshTokenFile, []byte("refresh-token\n"), 0600); err != nil {
		t.Fatalf("write refresh token: %v", err)
	}
	if got := nextCheckInterval(cfg); got != 5*time.Second {
		t.Fatalf("interval while retrying refresh = %v, want 5s", got)
	}
	if err := os.WriteFile(cfg.AccessTokenFile, []byte("access-token\n"), 0600); err != nil {
		t.Fatalf("write access token: %v", err)
	}
	if got := nextCheckInterval(cfg); got != 30*time.Second {
		t.Fatalf("interval with ready token = %v, want 30s", got)
	}
}

func TestRefreshIfNeededPublishesBootstrapTokenAndReadiness(t *testing.T) {
	userID := "00000000-0000-0000-0000-000000000001"
	clientID := "sandbox-notebook"
	access := jwt.NewWithClaims(jwt.SigningMethodHS256, accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
		AuthorizedParty: clientID,
	})
	accessToken, err := access.SignedString([]byte("test"))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}

	dir := t.TempDir()
	refreshFile := filepath.Join(dir, "refresh_token")
	bootstrapFile := filepath.Join(dir, "bootstrap.json")
	accessFile := filepath.Join(dir, "token")
	statusFile := filepath.Join(dir, "status.json")
	if err := os.WriteFile(refreshFile, []byte("refresh-token\n"), 0600); err != nil {
		t.Fatalf("write refresh token: %v", err)
	}
	bootstrapData, err := json.Marshal(tokenBootstrap{SessionID: "session-1", AccessToken: accessToken})
	if err != nil {
		t.Fatalf("encode bootstrap: %v", err)
	}
	if err := os.WriteFile(bootstrapFile, bootstrapData, 0600); err != nil {
		t.Fatalf("write bootstrap: %v", err)
	}

	cfg := config{
		RefreshTokenFile:   refreshFile,
		BootstrapTokenFile: bootstrapFile,
		AccessTokenFile:    accessFile,
		StatusFile:         statusFile,
		ExpectedUserID:     userID,
		ExpectedClientID:   clientID,
		RefreshSkew:        time.Minute,
	}
	currentRefreshToken, lastMountedRefreshToken := "", ""
	currentSessionID, lastMountedSessionID := "", ""
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := refreshIfNeeded(context.Background(), cfg, logger, &currentRefreshToken, &lastMountedRefreshToken, &currentSessionID, &lastMountedSessionID); err != nil {
		t.Fatalf("refreshIfNeeded failed: %v", err)
	}
	if got, err := readTrimmedFile(accessFile); err != nil || got != accessToken {
		t.Fatalf("published access token = %q, err=%v", got, err)
	}
	if currentSessionID != "session-1" || lastMountedSessionID != "session-1" {
		t.Fatalf("session IDs = %q/%q, want session-1", currentSessionID, lastMountedSessionID)
	}

	handler := readinessHandler(cfg)
	readyReq := httptest.NewRequest(http.MethodGet, "/readyz?sessionId=session-1", nil)
	readyRec := httptest.NewRecorder()
	handler.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("matching readiness status = %d, body=%s", readyRec.Code, readyRec.Body.String())
	}
	wrongReq := httptest.NewRequest(http.MethodGet, "/readyz?sessionId=session-2", nil)
	wrongRec := httptest.NewRecorder()
	handler.ServeHTTP(wrongRec, wrongReq)
	if wrongRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched readiness status = %d, want 503", wrongRec.Code)
	}
}

func TestRefreshIfNeededPersistsRotationBeforePublishingAccessToken(t *testing.T) {
	userID := "00000000-0000-0000-0000-000000000001"
	clientID := "sandbox-notebook"
	access := jwt.NewWithClaims(jwt.SigningMethodHS256, accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
		AuthorizedParty: clientID,
	})
	accessToken, err := access.SignedString([]byte("test"))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "sandbox-notebook" || clientSecret != "client-secret" {
			t.Error("refresh request missing confidential client authentication")
		}
		if r.Form.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh token = %q, want refresh-1", r.Form.Get("refresh_token"))
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:      accessToken,
			RefreshToken:     "refresh-2",
			ExpiresIn:        300,
			RefreshExpiresIn: 14400,
		})
	}))
	defer tokenServer.Close()

	persisted := false
	sessionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+accessToken {
			t.Error("rotation request missing delegated bearer token")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode rotation body: %v", err)
		}
		if body["refreshToken"] != "refresh-2" {
			t.Errorf("persisted refresh token = %q, want refresh-2", body["refreshToken"])
		}
		persisted = true
		w.WriteHeader(http.StatusOK)
	}))
	defer sessionServer.Close()

	dir := t.TempDir()
	refreshFile := filepath.Join(dir, "refresh")
	clientSecretFile := filepath.Join(dir, "client-secret")
	accessFile := filepath.Join(dir, "token")
	statusFile := filepath.Join(dir, "status.json")
	if err := os.WriteFile(refreshFile, []byte("refresh-1"), 0600); err != nil {
		t.Fatalf("write mounted refresh token: %v", err)
	}
	if err := os.WriteFile(clientSecretFile, []byte("client-secret"), 0600); err != nil {
		t.Fatalf("write client secret: %v", err)
	}
	cfg := config{
		TokenURL:         tokenServer.URL,
		ClientID:         clientID,
		ClientSecretFile: clientSecretFile,
		RefreshTokenFile: refreshFile,
		AccessTokenFile:  accessFile,
		StatusFile:       statusFile,
		TokenSessionURL:  sessionServer.URL,
		ExpectedUserID:   userID,
		ExpectedClientID: clientID,
		RefreshSkew:      time.Minute,
		RequestTimeout:   time.Second,
	}
	currentRefreshToken := ""
	lastMountedRefreshToken := ""
	currentSessionID := ""
	lastMountedSessionID := ""
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := refreshIfNeeded(context.Background(), cfg, logger, &currentRefreshToken, &lastMountedRefreshToken, &currentSessionID, &lastMountedSessionID); err != nil {
		t.Fatalf("refreshIfNeeded failed: %v", err)
	}
	if !persisted {
		t.Fatal("rotated refresh token was not persisted")
	}
	if currentRefreshToken != "refresh-2" {
		t.Fatalf("current refresh token = %q, want refresh-2", currentRefreshToken)
	}
	gotAccess, err := readTrimmedFile(accessFile)
	if err != nil {
		t.Fatalf("read published access token: %v", err)
	}
	if gotAccess != accessToken {
		t.Fatal("published access token does not match refreshed token")
	}
}
