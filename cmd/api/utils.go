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

	"github.com/jackc/pgx/v5"
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

func generateNotebookURL(baseURL, namespace, notebookName string) string {
	if baseURL == "" {
		return ""
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	notebookPath := fmt.Sprintf("notebook/%s/%s/lab/tree/demo.ipynb", namespace, notebookName)

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

// tryRestoreGPUAccess attempts to process pending credits and restore GPU access
// Returns: (hasAccess bool, error)
// - hasAccess: true if user now has GPU access, false if still insufficient credits
// - error: non-nil if there was a system error during processing
func (app *application) tryRestoreGPUAccess(ctx context.Context, tx pgx.Tx, profile *BillingProfile, logger *slog.Logger) (bool, error) {
	logger = logger.With("operation", "tryRestoreGPUAccess", "user_id", profile.UserID)

	// If user already has access, return true
	if profile.CanCreateGpuNotebook {
		logger.Info("user already has GPU access")
		return true, nil
	}

	// Check if last sync was less than 3 minutes ago
	currentTime := time.Now()
	timeSinceLastSync := currentTime.Sub(profile.AaaAndOpenCostSyncedAt)

	var cost float64
	syncAtTime := time.Now()

	if timeSinceLastSync < 3*time.Minute {
		logger.Info("last sync was less than 3 minutes ago, setting OpenCost to 0 but checking pending deductions",
			"last_sync", profile.AaaAndOpenCostSyncedAt,
			"time_since_last_sync", timeSinceLastSync.String(),
			"pending_deduction", profile.PendingDeduction)
		cost = 0 // Don't call OpenCost for short intervals
	} else {
		logger.Info("processing credits - sufficient time since last sync",
			"last_sync", profile.AaaAndOpenCostSyncedAt,
			"time_since_last_sync", timeSinceLastSync.String())

		// Calculate cost from OpenCost
		start := profile.AaaAndOpenCostSyncedAt

		var err error
		cost, err = app.getProfileCostFromOpenCost(start, syncAtTime, profile)
		if err != nil {
			logger.Error("failed to get profile cost from OpenCost", "error", err)
			return false, fmt.Errorf("failed to get profile cost: %w", err)
		}
		logger.Info("cost calculated from OpenCost", "cost", cost)
	}

	// Calculate total deduction needed
	totalDeduction := profile.PendingDeduction + cost

	// Initialize variables for updates
	canCreateGpuNotebook := false
	pendingDeduction := profile.PendingDeduction
	lastSyncBalance := profile.LastSyncBalance
	totalPaidCredit := profile.TotalPaidCredit

	if totalDeduction > 0 {
		logger.Info("total deduction > 0, checking user balance", "total_deduction", totalDeduction)

		// Get Keycloak token
		token, tokenErr := app.getKeycloakToken()
		if tokenErr != nil {
			logger.Error("failed to get Keycloak token", "error", tokenErr)
			return false, fmt.Errorf("failed to get Keycloak token: %w", tokenErr)
		}

		// Get current user balance
		balance, balanceErr := app.getUserBalance(ctx, profile.UserID, token)
		if balanceErr != nil {
			logger.Error("failed to get user balance", "error", balanceErr)
			return false, fmt.Errorf("failed to get user balance: %w", balanceErr)
		}

		logger.Info("retrieved user balance", "balance", balance, "required", totalDeduction)

		if balance == 0 {
			logger.Info("user has zero balance, keeping pending deduction")
			pendingDeduction = totalDeduction
			canCreateGpuNotebook = false
		} else if balance < totalDeduction {
			logger.Info("user has partial balance, deducting available amount",
				"balance", balance, "total_required", totalDeduction)

			res, deductErr := app.costDeductionRequest(ctx, profile.UserID, balance, token)

			canCreateGpuNotebook = false
			if deductErr != nil {
				logger.Error("failed to deduct partial balance", "error", deductErr)
				pendingDeduction = totalDeduction
			} else {
				lastSyncBalance = res.Result.UpdatedBalance
				pendingDeduction = totalDeduction - balance
				totalPaidCredit += balance
				logger.Info("successfully deducted partial balance",
					"deducted_amount", balance, "new_balance", res.Result.UpdatedBalance, "remaining_pending", pendingDeduction)
			}
		} else {
			// User has sufficient balance - deduct full amount
			logger.Info("user has sufficient balance, deducting full amount",
				"balance", balance, "total_deduction", totalDeduction)

			res, deductErr := app.costDeductionRequest(ctx, profile.UserID, totalDeduction, token)
			if deductErr != nil {
				logger.Error("failed to deduct full amount despite sufficient balance", "error", deductErr)
				pendingDeduction = totalDeduction
				canCreateGpuNotebook = false
			} else {
				logger.Info("successfully deducted full amount, enabling GPU access",
					"deducted_amount", totalDeduction, "new_balance", res.Result.UpdatedBalance)
				totalPaidCredit += totalDeduction
				pendingDeduction = 0
				lastSyncBalance = res.Result.UpdatedBalance
				canCreateGpuNotebook = true
			}
		}
	}

	// Update profile in database
	_, updateErr := tx.Exec(ctx, `UPDATE profiles SET
		can_create_gpu_notebook = $2,
		pending_deduction = $3,
		aaa_and_opencost_synced_at = $4,
		last_sync_balance = $5,
		total_paid_credit = $6
		WHERE user_id = $1`,
		profile.UserID, canCreateGpuNotebook, pendingDeduction, syncAtTime, lastSyncBalance, totalPaidCredit)

	if updateErr != nil {
		logger.Error("failed to update profile", "error", updateErr)
		return false, fmt.Errorf("failed to update profile: %w", updateErr)
	}

	logger.Info("credit processing completed",
		"can_create_gpu_notebook", canCreateGpuNotebook,
		"pending_deduction", pendingDeduction)

	return canCreateGpuNotebook, nil
}
