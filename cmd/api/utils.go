package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"
	"strings"
	"sync"
	"time"
)

func checkNotebookFailed(latestEvent constants.Events) bool {
	return latestEvent == constants.StatusPVCUploadFailed ||
		latestEvent == constants.StatusPVCUploadApplyFailed ||
		latestEvent == constants.StatusNotebookApplyFailed ||
		latestEvent == constants.StatusPVCCreationFailed ||
		latestEvent == constants.StatusPVCApplyFailed

}

func jsonResponse(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}
func sendResponse(w http.ResponseWriter, logger *slog.Logger, status int, userMessage string) {
	formattedMessage := formatMessage(userMessage)
	message := map[string]string{"message": formattedMessage}
	logger.Info("Request Status",
		"status", status)
	jsonResponse(w, status, message)
}
func sendError(w http.ResponseWriter, logger *slog.Logger, status int, userMessage string) {
	formattedMessage := formatMessage(userMessage)
	message := map[string]string{"detail": formattedMessage, "type": "error"}
	logger.Warn("Request Status",
		"status", status,
		"detail", formattedMessage,
		"type", "error")
	jsonResponse(w, status, message)
}
func sendResponseJson(w http.ResponseWriter, logger *slog.Logger, status int, userMessage any) {
	logger.Info("Request Status",
		"status", status)
	jsonResponse(w, status, userMessage)
}

func formatMessage(message string) string {
	if message == "" {
		return ""
	}
	return strings.ToUpper(string(message[0])) + message[1:]
}

func getLogger(r *http.Request) *slog.Logger {
	requestID := r.Context().Value("requestID").(string)
	logger := slog.With(
		slog.String("request_id", requestID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("user_agent", r.UserAgent()),
	)

	// Add comprehensive user information if available in context
	if userInfo, ok := r.Context().Value(UserContextKey).(UserInfo); ok {
		logger = logger.With(
			slog.String("user_id", userInfo.Sub),
			slog.String("user_email", userInfo.Email),
			slog.String("user_roles", strings.Join(userInfo.Roles, ",")),
			slog.String("namespace", userInfo.Sub), // commonly used as namespace
		)
	}

	return logger
}

func determineNotebookState(latestEvent constants.Events, k8sSpec map[string]any) NotebookState {
	if k8sSpec == nil {
		if checkNotebookFailed(latestEvent) {
			return NotebookStateFailed
		}

		if latestEvent == "" || latestEvent != constants.StatusNotebookApplied {
			return NotebookStateOpening
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
	return NotebookStateOpening
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

func generateNotebookURL(baseURL, namespace, notebookName string, imageName *string) string {
	if baseURL == "" {
		return ""
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	fileName := "demo.ipynb"
	if imageName != nil {
		if *imageName == "098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps1-v3" {
			fileName = "nha_ps1_skeletal_notebook_main.ipynb"
		} else if *imageName == "098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps2-v3" {
			fileName = "nha_ps2_skeletal_notebook_main.ipynb"
		} else if *imageName == "098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps3-v2" {
			fileName = "nha_ps3_skeletal_notebook_main.ipynb"
		}
	}

	notebookPath := fmt.Sprintf("notebook/%s/%s/lab/tree/%s", namespace, notebookName, fileName)

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

func WithK8sRetry(ctx context.Context, logger *slog.Logger, operation func() (constants.ShouldContinue, error)) error {
	return utils.WithLinearRetry(ctx, k8sRetryConfig, logger, operation)
}
func WithDBRetry(ctx context.Context, logger *slog.Logger, operation func() (constants.ShouldContinue, error)) error {
	return utils.WithLinearRetry(ctx, dbRetryConfig, logger, operation)
}
func ValidateNotebookName(name string) bool {
	pattern := `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	match, _ := regexp.MatchString(pattern, name)
	return match
}
func getErrorMessageForNotebookName(name string) (string, bool) {
	if len(name) > 50 {
		return "Notebook name cannot exceed 50 characters", true
	}
	if len(name) < 4 {
		return "Notebook name must be at least 4 characters long", true
	}
	if !ValidateNotebookName(name) {
		return "Notebook name can only contain lowercase letters, numbers, and hyphens. It must start and end with a letter or number.", true
	}
	return "", false
}

func IsNotebookRunningFromAnnotations(annotations map[string]any) bool {
	_, isStopped := annotations["kubeflow-resource-stopped"]
	return !isStopped
}
