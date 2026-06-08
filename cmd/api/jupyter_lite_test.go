package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func requestWithLoggerContext(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), "requestID", "test-request-id")
	return req.WithContext(ctx)
}

func requestWithUserContext(method, target string, body []byte) *http.Request {
	req := requestWithLoggerContext(method, target, body)
	ctx := context.WithValue(req.Context(), UserContextKey, UserInfo{
		Sub:   "00000000-0000-0000-0000-000000000001",
		Email: "test@example.com",
		Roles: []string{},
	})
	return req.WithContext(ctx)
}

func testJupyterLiteApp(staticDir string) *application {
	return &application{
		env: ApiEnv{
			JupyterLiteBaseURL:   "/jupyterlite",
			JupyterLiteStaticDir: staticDir,
			MaxBodySizeInMB:      5,
			TimeoutInSecs:        10,
			CORS_ORIGINS:         "*",
			NotebookConfig: NotebookConfig{
				SlotConfigProfile: "production",
			},
		},
		rateLimiter: NewIPRateLimiter(100, 60),
	}
}

func configureTestAuth(t *testing.T, app *application) string {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}

	app.env.KeycloakClientID = "sandbox-connect-test"
	app.env.KeycloakPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyDER}))

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, &JWTPayload{
		Exp:           time.Now().Add(time.Hour).Unix(),
		Iat:           time.Now().Unix(),
		Sub:           "00000000-0000-0000-0000-000000000001",
		Azp:           app.env.KeycloakClientID,
		EmailVerified: true,
		KycVerified:   false,
		Email:         "test.com",
	})
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return tokenString
}

func TestListGPUCategoriesIncludesJupyterLite(t *testing.T) {
	app := testJupyterLiteApp("")
	rec := httptest.NewRecorder()
	req := requestWithLoggerContext(http.MethodGet, "/v1/categories", nil)

	app.listGPUCategories(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp CategoriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	for _, category := range resp.Categories {
		if category.Name != "jupyter_lite" {
			continue
		}
		if category.ResourceType != "browser" {
			t.Fatalf("expected browser resource type, got %q", category.ResourceType)
		}
		if category.IsBookable {
			t.Fatal("expected jupyter_lite to be non-bookable")
		}
		if category.LaunchMode != "direct" {
			t.Fatalf("expected direct launch mode, got %q", category.LaunchMode)
		}
		if category.LaunchURL != "/jupyterlite/lab/index.html" {
			t.Fatalf("unexpected launch URL: %q", category.LaunchURL)
		}
		if category.PriceLabel != "Free" {
			t.Fatalf("expected Free price label, got %q", category.PriceLabel)
		}
		if category.Persistence != "browser_local" {
			t.Fatalf("expected browser_local persistence, got %q", category.Persistence)
		}
		return
	}
	t.Fatal("jupyter_lite category not found")
}

func TestCreateGPUBookingRejectsJupyterLite(t *testing.T) {
	app := testJupyterLiteApp("")
	body := []byte(`{
		"notebookName": "lite-demo",
		"category": "jupyter_lite",
		"slotDate": "2099-01-01",
		"slotKeys": ["jupyter_lite"]
	}`)
	rec := httptest.NewRecorder()
	req := requestWithUserContext(http.MethodPost, "/v1/bookings", body)

	app.createGPUBooking(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Category does not use bookings or slots")) {
		t.Fatalf("expected non-bookable error, got %s", rec.Body.String())
	}
}

func TestSlotEndpointsRejectJupyterLite(t *testing.T) {
	app := testJupyterLiteApp("")

	t.Run("available slots", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := requestWithUserContext(http.MethodGet, "/v1/slots/available?category=jupyter_lite&date=2099-01-01", nil)

		app.listGPUAvailableSlots(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("calendar", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := requestWithLoggerContext(http.MethodGet, "/v1/slots/calendar?category=jupyter_lite&month=2099-01", nil)

		app.listGPUCalendarSlots(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestJupyterLiteStaticRouteRequiresAuth(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "lab"), 0755); err != nil {
		t.Fatalf("failed to create lab dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "lab", "index.html"), []byte("<!doctype html><title>JupyterLite</title>"), 0644); err != nil {
		t.Fatalf("failed to write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "kernel.wasm"), []byte{0x00, 0x61, 0x73, 0x6d}, 0644); err != nil {
		t.Fatalf("failed to write wasm: %v", err)
	}

	app := testJupyterLiteApp(staticDir)
	app.env.KYCEnabled = true
	token := configureTestAuth(t, app)
	handler := app.router()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jupyterlite/lab/index.html", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated index status 401, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/jupyterlite/lab/index.html", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected authenticated index status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !bytes.Contains([]byte(contentType), []byte("text/html")) {
		t.Fatalf("expected HTML content type, got %q", contentType)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/jupyterlite/kernel.wasm", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected authenticated wasm status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/wasm" {
		t.Fatalf("expected application/wasm content type, got %q", contentType)
	}
}
