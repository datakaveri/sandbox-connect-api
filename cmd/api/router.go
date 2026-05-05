package main

import (
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (app *application) router() http.Handler {
	rootMux := http.NewServeMux()

	// Serve API documentation with ReDoc (no auth required)
	rootMux.HandleFunc("/v1/apis/", app.serveReDoc)

	// Register health endpoint directly (not behind auth)
	rootMux.HandleFunc("GET /v1/health", app.healthCheck)

	apiMux := http.NewServeMux()

	// Standard notebook read/status routes (non-permission protected)
	apiMux.HandleFunc("GET /v1/notebook/list", app.listNotebooks)
	apiMux.HandleFunc("GET /v1/notebook/check-exists/{notebook_name}", app.checkNotebookExists)
	apiMux.HandleFunc("GET /v1/notebook/status/{notebook_name}", app.checkNotebookStatus)
	apiMux.HandleFunc("GET /v1/notebook/instance-types", app.listGPUInstanceTypes)
	apiMux.HandleFunc("GET /v1/notebook/gpu-instance-types", app.listGPUInstanceTypes)
	apiMux.HandleFunc("GET /v1/categories", app.listGPUCategories)
	apiMux.HandleFunc("POST /v1/bookings", app.createGPUBooking)
	apiMux.HandleFunc("GET /v1/bookings", app.listGPUBookings)
	apiMux.HandleFunc("PATCH /v1/bookings/{id}/cancel", app.cancelGPUBooking)
	apiMux.HandleFunc("PATCH /v1/bookings/{id}/extend", app.extendGPUBooking)
	apiMux.HandleFunc("PATCH /v1/bookings/{id}/reset", app.resetGPUBooking)
	apiMux.HandleFunc("PATCH /v1/bookings/{id}/terminate", app.terminateGPUBooking)
	apiMux.HandleFunc("GET /v1/slots/available", app.listGPUAvailableSlots)
	apiMux.HandleFunc("GET /v1/slots/calendar", app.listGPUCalendarSlots)

	// Profile routes
	apiMux.HandleFunc("POST /v1/profile/create", app.createProfile)

	// Apply audit middleware (after auth, before handlers)
	// Auth runs first → sets UserInfo in context → Audit wraps handlers to capture status code
	auditHandler := app.auditMiddleware(apiMux)

	// Apply auth middleware to all API routes
	authHandler := app.authMiddleware(auditHandler)

	// Mount the authenticated API handler to the main router
	rootMux.Handle("/v1/", authHandler)

	handler := app.contextTimeout(rootMux)
	handler = app.rateLimitMiddleware(handler)
	handler = app.enableCORS(handler)
	handler = loggingMiddleware(handler)
	return http.MaxBytesHandler(handler, int64(app.env.MaxBodySizeInMB)<<20)
}

// Template for ReDoc UI
const redocTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}} - v{{.Version}}</title>
    <link rel="icon" href="https://cdn.redoc.ly/redoc/logo-mini.svg">
    <style>
        body {
            margin: 0;
            padding: 0;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
        }
        .api-info {
            background-color: #f8f9fa;
            padding: 10px 20px;
            border-bottom: 1px solid #e9ecef;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .api-info h1 {
            font-size: 1.5rem;
            margin: 0;
            color: #343a40;
        }
        .api-version {
            background-color: #6c757d;
            color: white;
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 0.8rem;
        }
    </style>
</head>
<body>
    <div class="api-info">
        <h1>{{.Title}}</h1>
        <span class="api-version">v{{.Version}}</span>
    </div>
    <redoc spec-url="swagger.json" hide-hostname="true" expand-responses="200,201"></redoc>
    <script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>
</body>
</html>`

func (app *application) serveReDoc(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	path := strings.TrimPrefix(r.URL.Path, "/v1/apis/")

	if path == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		tmpl, err := template.New("redoc").Parse(redocTemplate)
		if err != nil {
			logger.Error("failed to parse redoc template", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		data := map[string]string{
			"Title":   "Sandbox Connect API Documentation",
			"Version": app.env.Version,
		}

		if err := tmpl.Execute(w, data); err != nil {
			logger.Error("failed to execute redoc template", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	if path == "swagger.json" || path == "swagger.yaml" {
		filePath := filepath.Join("/app/docs", path)

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			filePath = filepath.Join("docs", path)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				logger.Warn("swagger file not found", "path", filePath)
				sendError(w, logger, http.StatusNotFound, "Not Found")
				return
			}
		}

		if strings.HasSuffix(path, ".json") {
			w.Header().Set("Content-Type", "application/json")
		} else if strings.HasSuffix(path, ".yaml") {
			w.Header().Set("Content-Type", "application/yaml")
		}

		logger.Info("serving swagger file", "path", filePath)
		http.ServeFile(w, r, filePath)
		return
	}

	sendError(w, logger, http.StatusNotFound, "Not Found")
}

func (app *application) healthCheck(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	sendResponseJson(w, logger, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"version": app.env.Version,
	})
}
