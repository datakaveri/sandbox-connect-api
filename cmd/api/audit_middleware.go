package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// auditedEndpoints maps "METHOD /path" to a human-readable action name.
// Only these endpoints produce audit messages, and only on 2xx responses.
var auditedEndpoints = map[string]string{
	"POST /v1/bookings":                             "Create",
	"POST /v1/bookings/{id}/notebook-token-session": "CreateNotebookTokenSession",
	"PUT /v1/bookings/{id}/notebook-token-session":  "RotateNotebookTokenSession",
	"PATCH /v1/bookings/{id}/cancel":                "Cancel",
	"PATCH /v1/bookings/{id}/extend":                "Extend",
	"PATCH /v1/bookings/{id}/reset":                 "Reset",
	"PATCH /v1/bookings/{id}/terminate":             "Terminate",
}

// ---------------------------------------------------------------------------
// Audit metadata — shared pointer passed through context
// ---------------------------------------------------------------------------

// AuditMetadata carries handler-supplied metadata back to the audit middleware.
// The middleware injects a pointer into the request context; the handler writes
// to the struct; the middleware reads it after the handler returns.
type AuditMetadata struct {
	SandboxType string // "cpu" or "gpu"
}

type auditMetadataKeyType struct{}

var auditMetadataKey = auditMetadataKeyType{}

// SetAuditSandboxType sets the sandbox type ("cpu" or "gpu") on the audit
// metadata stored in the request context. Handlers call this to annotate the
// audit message with the notebook type.
// Safe to call even when auditing is disabled (no-op).
func SetAuditSandboxType(r *http.Request, sandboxType string) {
	if md, ok := r.Context().Value(auditMetadataKey).(*AuditMetadata); ok {
		md.SandboxType = sandboxType
	}
}

// ---------------------------------------------------------------------------
// Response writer wrapper — captures the HTTP status code
// ---------------------------------------------------------------------------

// auditResponseWriter wraps http.ResponseWriter to capture the status code
// written by the handler so the audit middleware can decide whether to audit.
type auditResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (w *auditResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.statusCode = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// Implicit 200 OK when Write is called without WriteHeader
		w.statusCode = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap returns the original ResponseWriter so that http.ResponseController
// and other Go 1.20+ mechanisms can access additional interfaces (Flusher, etc.).
func (w *auditResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// auditMiddleware intercepts HTTP responses and asynchronously publishes
// audit messages for configured endpoints on successful (2xx) responses.
//
// It should be applied AFTER the auth middleware (so UserInfo is in context)
// and BEFORE the route handlers.
//
// Design principles (matching the file server reference):
//   - Non-blocking: audit runs in a goroutine, never delays the API response
//   - Fault-tolerant: all errors are caught and logged, never returned
//   - Success-only: only 2xx responses are audited
func (app *application) auditMiddleware(next http.Handler) http.Handler {
	// If audit is not configured, pass through without wrapping
	if app.auditService == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check upfront if this endpoint is auditable (avoid wrapping if not)
		action := getAuditAction(r.Method, r.URL.Path)
		if action == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Capture request metadata before the handler runs
		authToken := r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, "/notebook-token-session") {
			authToken = ""
		}
		ipAddress := getClientIP(r)
		userAgent := r.UserAgent()
		api := r.URL.Path
		method := r.Method
		requestID, _ := r.Context().Value("requestID").(string)

		// Inject audit metadata pointer so handlers can set sandbox_type
		metadata := &AuditMetadata{}
		ctxWithMeta := context.WithValue(r.Context(), auditMetadataKey, metadata)
		r = r.WithContext(ctxWithMeta)

		// Wrap the ResponseWriter to capture the status code
		aw := &auditResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		// Execute the handler
		next.ServeHTTP(aw, r)

		// Only audit successful (2xx) responses
		if !isSuccessStatus(aw.statusCode) {
			slog.Debug("Skipping audit for non-success response",
				"method", method,
				"path", api,
				"statusCode", aw.statusCode,
				"action", action,
			)
			return
		}

		// Extract user info set by auth middleware
		userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
		if !ok {
			slog.Warn("Skipping audit: user info not found in context",
				"method", method,
				"path", api,
			)
			return
		}

		role := determineUserRole(userInfo.Roles)

		slog.Debug("Audit middleware matched endpoint",
			"method", method,
			"path", api,
			"action", action,
			"userId", userInfo.Sub,
			"role", role,
			"statusCode", aw.statusCode,
		)

		// Build audit context
		auditCtx := AuditContext{
			API:         api,
			Method:      method,
			Action:      action,
			UserID:      userInfo.Sub,
			Role:        role,
			AuthToken:   authToken,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			RequestID:   requestID,
			LogType:     AuditLogTypeCompute,
			SandboxType: metadata.SandboxType,
		}

		// Fire-and-forget: publish asynchronously so we never delay the response
		go app.auditService.PublishAuditMessage(auditCtx)
	})
}

// getAuditAction returns the action name for a given HTTP method + path,
// or an empty string if the endpoint is not audited.
func getAuditAction(method, path string) string {
	key := method + " " + path
	if action, ok := auditedEndpoints[key]; ok {
		return action
	}

	if method == http.MethodPatch || method == http.MethodPost || method == http.MethodPut {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 4 && parts[0] == "v1" && parts[1] == "bookings" && parts[2] != "" {
			key = method + " /v1/bookings/{id}/" + parts[3]
			if action, ok := auditedEndpoints[key]; ok {
				return action
			}
		}
	}

	return ""
}

// isSuccessStatus returns true for HTTP 2xx status codes.
func isSuccessStatus(code int) bool {
	return code >= 200 && code < 300
}
