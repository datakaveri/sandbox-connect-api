package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	env "github.com/caarlos0/env/v11"
)

func TestApiEnvBookingsEnabledDefaultsTrue(t *testing.T) {
	required := map[string]string{
		"API_ADDRESS":                    "127.0.0.1:0",
		"API_POSTGRES_URL":               "postgres://example",
		"API_KEYCLOAK_URL":               "https://idp.example.test/auth",
		"API_KEYCLOAK_REALM":             "sandbox",
		"API_KEYCLOAK_CLIENT_ID":         "sandbox-connect-test",
		"API_KEYCLOAK_PUBLIC_KEY":        "test-public-key",
		"API_KYC_ENABLED":                "false",
		"API_VERSION":                    "test",
		"API_DEFAULT_CPU_STORAGE_SIZE":   "10Gi",
		"API_DEFAULT_GPU_STORAGE_SIZE":   "50Gi",
		"API_DEFAULT_CPU_REQUEST":        "1",
		"API_DEFAULT_CPU_LIMIT":          "1",
		"API_DEFAULT_MEMORY_REQUEST":     "1Gi",
		"API_DEFAULT_MEMORY_LIMIT":       "1Gi",
		"API_DEFAULT_GPU_TYPE":           "nvidia.com/gpu",
		"API_DEFAULT_GPU_REQUEST":        "1",
		"API_DEFAULT_GPU_LIMIT":          "1",
		"API_DEFAULT_GPU_MEMORY_REQUEST": "1Gi",
		"API_DEFAULT_GPU_MEMORY_LIMIT":   "1Gi",
		"API_DEFAULT_GPU_CPU_REQUEST":    "1",
		"API_DEFAULT_GPU_CPU_LIMIT":      "1",
		"API_GPU_NODE_INSTANCE_TYPES":    "g4dn.xlarge",
		"API_KUBEFLOW_URL":               "https://kubeflow.example.test",
	}
	for key, value := range required {
		t.Setenv(key, value)
	}

	previousValue, hadPreviousValue := os.LookupEnv("API_BOOKINGS_ENABLED")
	if err := os.Unsetenv("API_BOOKINGS_ENABLED"); err != nil {
		t.Fatalf("unset API_BOOKINGS_ENABLED: %v", err)
	}
	t.Cleanup(func() {
		if hadPreviousValue {
			_ = os.Setenv("API_BOOKINGS_ENABLED", previousValue)
		} else {
			_ = os.Unsetenv("API_BOOKINGS_ENABLED")
		}
	})

	var cfg ApiEnv
	if err := env.Parse(&cfg); err != nil {
		t.Fatalf("parse ApiEnv: %v", err)
	}
	if !cfg.BookingsEnabled {
		t.Fatal("expected API_BOOKINGS_ENABLED to default to true")
	}
}

func testBookingModeApp(bookingsEnabled bool) *application {
	app := testJupyterLiteApp("")
	app.env.BookingsEnabled = bookingsEnabled
	return app
}

func authenticatedRouterRequest(t *testing.T, app *application, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token := configureTestAuth(t, app)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.router().ServeHTTP(rec, req)
	return rec
}

func TestBookingModeRouterPreservesBookingRoutesWhenEnabled(t *testing.T) {
	app := testBookingModeApp(true)

	rec := authenticatedRouterRequest(t, app, http.MethodGet, "/v1/categories", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected categories to remain routed when bookings are enabled, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = authenticatedRouterRequest(t, app, http.MethodPost, "/v1/notebook/create", []byte(`{}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected direct notebook create to be unavailable when bookings are enabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBookingModeRouterEnablesDirectNotebookRoutesWhenDisabled(t *testing.T) {
	app := testBookingModeApp(false)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/v1/notebook/create"},
		{name: "start", method: http.MethodPatch, path: "/v1/notebook/start"},
		{name: "stop", method: http.MethodPatch, path: "/v1/notebook/stop"},
		{name: "delete", method: http.MethodDelete, path: "/v1/notebook/delete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := authenticatedRouterRequest(t, app, tt.method, tt.path, []byte(`{}`))
			if rec.Code == http.StatusNotFound {
				t.Fatalf("expected %s %s to be routed when bookings are disabled, got 404: %s", tt.method, tt.path, rec.Body.String())
			}
		})
	}
}

func TestBookingModeRouterKeepsNotebookTokenSessionWhenDisabled(t *testing.T) {
	app := testBookingModeApp(false)

	rec := authenticatedRouterRequest(t, app, http.MethodPost, "/v1/bookings/not-a-number/notebook-token-session", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected booking token-session route to remain available and validate booking id, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = authenticatedRouterRequest(t, app, http.MethodPost, "/v1/notebook/bad/notebook-token-session", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected direct notebook token-session route to remain available and validate notebook name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotebookTokenRotationRequestDetectionByMode(t *testing.T) {
	bookingReq := httptest.NewRequest(http.MethodPut, "/v1/bookings/123/notebook-token-session", nil)
	directReq := httptest.NewRequest(http.MethodPut, "/v1/notebook/demo-cpu/notebook-token-session", nil)

	if !testBookingModeApp(true).isNotebookTokenRotationRequest(bookingReq) {
		t.Fatal("expected booking token rotation to be detected when bookings are enabled")
	}
	if testBookingModeApp(true).isNotebookTokenRotationRequest(directReq) {
		t.Fatal("did not expect direct token rotation to be detected when bookings are enabled")
	}
	if !testBookingModeApp(false).isNotebookTokenRotationRequest(bookingReq) {
		t.Fatal("expected booking token rotation to remain detected when bookings are disabled")
	}
	if !testBookingModeApp(false).isNotebookTokenRotationRequest(directReq) {
		t.Fatal("expected direct token rotation to be detected when bookings are disabled")
	}
}

func TestBookingModeRouterDisablesBookingAndSchedulingRoutes(t *testing.T) {
	app := testBookingModeApp(false)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create booking", method: http.MethodPost, path: "/v1/bookings"},
		{name: "list bookings", method: http.MethodGet, path: "/v1/bookings"},
		{name: "cancel booking", method: http.MethodPatch, path: "/v1/bookings/123/cancel"},
		{name: "available slots", method: http.MethodGet, path: "/v1/slots/available?category=cpu_basic&date=2099-01-01"},
		{name: "calendar", method: http.MethodGet, path: "/v1/slots/calendar?category=cpu_basic&month=2099-01"},
		{name: "categories", method: http.MethodGet, path: "/v1/categories"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := authenticatedRouterRequest(t, app, tt.method, tt.path, nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected disabled route 404, got %d: %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("expected structured JSON error, got %q: %v", rec.Body.String(), err)
			}
			if body["title"] != "Not Found" || body["detail"] != "Bookings and scheduling are disabled" || body["type"] != "error" {
				t.Fatalf("unexpected disabled response: %#v", body)
			}
		})
	}
}
