package main

import (
	"sandbox-backend-service/pkg/constants"
	"testing"
)

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
		if result != NotebookStatePending {
			t.Errorf("expected pending, got %v", result)
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
		{"With dots", "my.notebook", true},
		{"Complex with dots and hyphens", "my-notebook.v1.2", true},
		{"Single character", "a", true},
		{"Single number", "1", true},
		{"Starts with number", "1notebook", true},
		{"Multiple segments", "notebook.test.v1", true},
		{"Hyphen between alphanumerics", "note-book", true},

		{"Empty string", "", false},
		{"Starts with hyphen", "-notebook", false},
		{"Ends with hyphen", "notebook-", false},
		{"Starts with dot", ".notebook", false},
		{"Ends with dot", "notebook.", false},
		{"Contains uppercase", "Notebook", false},
		{"Contains underscore", "note_book", false},
		{"Contains special characters", "notebook@test", false},
		{"Segment starts with hyphen", "notebook.-test", false},
		{"Segment ends with hyphen", "example.com", true},
		{"Double dots", "notebook..test", false},
		{"Double hyphens", "note--book", true},
		{"Contains spaces", "note book", false},
		{"uppercase", "APPLE", false},
		{"lot of .", "a.b.e.d.f.g", true},
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
