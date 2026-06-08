package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sandbox-backend-service/pkg/constants"
	"testing"
)

func TestSendErrorIncludesTitle(t *testing.T) {
	rec := httptest.NewRecorder()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sendError(rec, logger, http.StatusBadRequest, "active booking limit exceeded")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["title"] != "Bad Request" {
		t.Fatalf("expected title Bad Request, got %q", body["title"])
	}
	if body["detail"] != "Active booking limit exceeded" {
		t.Fatalf("expected formatted detail, got %q", body["detail"])
	}
	if body["type"] != "error" {
		t.Fatalf("expected type error, got %q", body["type"])
	}

	rec = httptest.NewRecorder()
	sendResponse(rec, logger, http.StatusUnauthorized, "unauthorized")
	body = map[string]string{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode sendResponse error response: %v", err)
	}
	if body["title"] != "Unauthorized" || body["detail"] != "Unauthorized" || body["type"] != "error" {
		t.Fatalf("expected sendResponse error shape with title/detail/type, got %#v", body)
	}
}

func TestDetermineNotebookState(t *testing.T) {

	t.Run("nil k8sSpec, event applied", func(t *testing.T) {
		result := determineNotebookState(constants.StatusNotebookApplied, nil)
		if result != NotebookStateOrphaned {
			t.Errorf("expected orphaned, got %v", result)
		}
	})

	t.Run("nil k8sSpec, event failed", func(t *testing.T) {
		result := determineNotebookState(constants.StatusNotebookApplyFailed, nil)
		if result != NotebookStateFailed {
			t.Errorf("expected failed, got %v", result)
		}
	})

	t.Run("nil k8sSpec, event empty", func(t *testing.T) {
		result := determineNotebookState("", nil)
		if result != NotebookStateRunning {
			t.Errorf("expected running, got %v", result)
		}
	})

	t.Run("non-nil k8sSpec, event applied, orphan check", func(t *testing.T) {
		k8sSpec := map[string]any{
			"metadata": map[string]any{},
			"status":   map[string]any{"readyReplicas": int64(1)},
		}
		result := determineNotebookState(constants.StatusNotebookApplied, k8sSpec)
		if result != NotebookStateRunning {
			t.Errorf("expected running, got %v", result)
		}
	})
}

func TestValidateNotebookName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"Simple lowercase alphanumeric", "mynotebook1", true},
		{"With hyphen", "my-notebook", true},
		{"Single character letter", "a", true},
		{"Single character number", "1", true},
		{"Starts with number", "1notebook", true},
		{"Hyphen between alphanumerics", "note-book", true},
		{"Multiple hyphens", "my-web-app-v1", true},
		{"Numbers and hyphens", "123-test-456", true},
		{"Complex valid name", "kafka-broker-01", true},
		{"Database cluster style", "database-cluster-prod", true},

		{"Empty string", "", false},
		{"Starts with hyphen", "-notebook", false},
		{"Ends with hyphen", "notebook-", false},
		{"Contains uppercase", "Notebook", false},
		{"Contains underscore", "note_book", false},
		{"Contains special characters", "notebook@test", false},
		{"Contains dots", "my.notebook", false},
		{"Contains spaces", "note book", false},
		{"Uppercase", "APPLE", false},
		{"Mixed case", "MyApp", false},
		{"Double hyphens", "note--book", true},
		{"Only hyphen", "-", false},
		{"Hyphen at start", "-abc", false},
		{"Hyphen at end", "abc-", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateNotebookName(tt.input)
			if result != tt.expected {
				t.Errorf("ValidateNotebookName(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMaxContinuousSlotSelectionMessage(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{
			name:  "two slot limit",
			limit: 2,
			want:  "You can select a maximum of two continuous slots. Please reduce your selection to two slots or fewer.",
		},
		{
			name:  "single slot limit",
			limit: 1,
			want:  "You can select a maximum of one continuous slot. Please reduce your selection to one slot or fewer.",
		},
		{
			name:  "numeric fallback",
			limit: 3,
			want:  "You can select a maximum of 3 continuous slots. Please reduce your selection to 3 slots or fewer.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxContinuousSlotSelectionMessage(tt.limit); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
