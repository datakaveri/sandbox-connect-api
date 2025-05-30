package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sandbox-backend-service/pkg/constants"
	"strings"
	"sync"
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
func sendError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, userMessage string) {
	startTime, ok := r.Context().Value("startTime").(time.Time)
	if !ok {
		startTime = time.Now()
	}
	message := map[string]string{"error": userMessage}
	duration := time.Since(startTime)
	logger.Error("Request End",
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

func determineNotebookState(latestEvent constants.Events, k8sSpec map[string]any) NotebookState {
	if k8sSpec == nil {
		if latestEvent == constants.StatusPVCApplyFailed ||
			latestEvent == constants.StatusPVCUploadFailed ||
			latestEvent == constants.StatusPVCUploadApplyFailed ||
			latestEvent == constants.StatusNotebookApplyFailed ||
			latestEvent == constants.StatusPVCCreationFailed {
			return NotebookStateFailed
		}

		if latestEvent == "" || latestEvent != constants.StatusNotebookApplied {
			return NotebookStatePending
		}
		return NotebookStateOrphaned
	}

	if metadataMap, hasMetadata := k8sSpec["metadata"].(map[string]any); hasMetadata {
		if annotationsMap, hasAnnotations := metadataMap["annotations"].(map[string]any); hasAnnotations {
			_, isStopped := annotationsMap["kubeflow-resource-stopped"]
			if isStopped {
				return NotebookStateStopped
			}
		}
	}

	if statusMap, hasStatus := k8sSpec["status"].(map[string]any); hasStatus {
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

func generateNotebookURL(baseURL, namespace, notebookName string) string {
	if baseURL == "" {
		return ""
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	notebookPath := fmt.Sprintf("notebook/%s/%s/lab", namespace, notebookName)

	return baseURL + "/" + notebookPath
}

type IPRateLimiter struct {
	ips        map[string]*IpRateLimiterRequests
	mu         sync.RWMutex
	rate       int
	windowSecs int
}

type IpRateLimiterRequests struct {
	count    int
	firstReq time.Time
}

func NewIPRateLimiter(rate, windowSecs int) *IPRateLimiter {
	return &IPRateLimiter{
		ips:        make(map[string]*IpRateLimiterRequests),
		rate:       rate,
		windowSecs: windowSecs,
		mu:         sync.RWMutex{},
	}
}

func (i *IPRateLimiter) isAllowed(ip string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := time.Now()
	req, exists := i.ips[ip]

	if !exists {
		i.ips[ip] = &IpRateLimiterRequests{
			count:    1,
			firstReq: now,
		}
		return true
	}

	if now.Sub(req.firstReq) >= time.Duration(i.windowSecs)*time.Second {
		req.count = 1
		req.firstReq = now
		return true
	}

	if req.count < i.rate {
		req.count++
		return true
	}

	return false
}

func (i *IPRateLimiter) GetLimiter(ip string) bool {
	return i.isAllowed(ip)
}
func parseAllowedOrigins(originsStr string) []string {
	if originsStr == "" {
		return []string{}
	}
	origins := strings.Split(originsStr, ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}
	return origins
}
