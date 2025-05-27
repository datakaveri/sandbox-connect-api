package main

import (
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"testing"
)

func TestDetermineNotebookState(t *testing.T) {
	logger := slog.Default()

	t.Run("nil k8sSpec, event applied", func(t *testing.T) {
		result := determineNotebookState(constants.StatusNotebookApplied, nil, logger)
		if result != NotebookStateOrphaned {
			t.Errorf("expected orphaned, got %v", result)
		}
	})

	t.Run("nil k8sSpec, event failed", func(t *testing.T) {
		result := determineNotebookState(constants.StatusNotebookApplyFailed, nil, logger)
		if result != NotebookStateFailed {
			t.Errorf("expected failed, got %v", result)
		}
	})

	t.Run("nil k8sSpec, event empty", func(t *testing.T) {
		result := determineNotebookState("", nil, logger)
		if result != NotebookStatePending {
			t.Errorf("expected pending, got %v", result)
		}
	})

	t.Run("non-nil k8sSpec, event applied, orphan check", func(t *testing.T) {
		k8sSpec := map[string]any{
			"metadata": map[string]any{},
			"status":   map[string]any{"readyReplicas": int64(1)},
		}
		result := determineNotebookState(constants.StatusNotebookApplied, k8sSpec, logger)
		if result != NotebookStateRunning {
			t.Errorf("expected running, got %v", result)
		}
	})
}
