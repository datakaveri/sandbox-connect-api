package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"time"
)

func jsonResponse(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}
func sendResponse(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, userMessage string) {
	startTime, ok := r.Context().Value("startTime").(time.Time)
	if !ok {
		startTime = time.Now()
	}
	message := map[string]string{"message": userMessage}
	duration := time.Since(startTime)
	logger.Info("Request End",
		"duration_ms", duration.Milliseconds(),
		"status", status)
	jsonResponse(w, status, message)
}
func sendResponseJson(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, userMessage any) {
	startTime, ok := r.Context().Value("startTime").(time.Time)
	if !ok {
		startTime = time.Now()
	}
	duration := time.Since(startTime)
	logger.Info("Request End",
		"duration_ms", duration.Milliseconds(),
		"status", status)
	jsonResponse(w, status, userMessage)
}
func getLogger(r *http.Request) *slog.Logger {
	requestID := r.Context().Value("requestID").(string)
	logger := slog.With(
		slog.String("request_id", requestID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_ip", r.RemoteAddr),
		slog.String("user_agent", r.UserAgent()),
	)
	return logger
}

func determineNotebookState(latestEvent string, k8sSpec any, logger *slog.Logger) NotebookState {
	if k8sSpec == nil {
		if latestEvent == string(constants.StatusPVCApplyFailed) ||
			latestEvent == string(constants.StatusPVCUploadFailed) ||
			latestEvent == string(constants.StatusPVCUploadApplyFailed) ||
			latestEvent == string(constants.StatusNotebookApplyFailed) ||
			latestEvent == string(constants.StatusPVCCreationFailed) {
			return NotebookStateFailed
		}

		if latestEvent == "" || latestEvent != string(constants.StatusNotebookApplied) {
			return NotebookStatePending
		}

		return NotebookStateOrphaned
	}

	k8sSpecMap, ok := k8sSpec.(map[string]any)
	if !ok {
		logger.Warn("expected map[string]any for k8s notebook spec")
		return NotebookStatePending
	}

	if metadataMap, hasMetadata := k8sSpecMap["metadata"].(map[string]any); hasMetadata {
		if annotationsMap, hasAnnotations := metadataMap["annotations"].(map[string]any); hasAnnotations {
			_, isStopped := annotationsMap["kubeflow-resource-stopped"]
			if isStopped {
				return NotebookStateStopped
			}
		}
	}

	if statusMap, hasStatus := k8sSpecMap["status"].(map[string]any); hasStatus {
		if readyReplicas, ok := statusMap["readyReplicas"].(int64); ok && readyReplicas > 0 {
			return NotebookStateRunning
		}
	}
	return NotebookStatePending
}

var SupportedGPUResources = []string{
	"nvidia.com/gpu",
	"amd.com/gpu",
	"cloud.google.com/tpu",
}

func IsValidGPUResource(gpuType string) bool {
	return contains(SupportedGPUResources, gpuType)
}
func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
