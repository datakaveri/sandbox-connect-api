package main

import (
	"net/http"

	httpSwagger "github.com/swaggo/http-swagger"
)

func (app *application) router() http.Handler {
	rootMux := http.NewServeMux()

	// Serve Swagger docs
	rootMux.Handle("/v1/docs/", httpSwagger.Handler())

	// Register health endpoint directly (not behind auth)
	rootMux.HandleFunc("GET /v1/health", app.healthCheck)

	apiMux := http.NewServeMux()

	// Standard notebook routes (non-permission protected)
	apiMux.HandleFunc("PATCH /v1/notebook/stop", app.stopNotebook)
	apiMux.HandleFunc("DELETE /v1/notebook/delete", app.deleteNotebook)
	apiMux.HandleFunc("GET /v1/notebook/list", app.listNotebooks)
	apiMux.HandleFunc("GET /v1/notebook/check-exists/{notebook_name}", app.checkNotebookExists)
	apiMux.HandleFunc("GET /v1/notebook/status/{notebook_name}", app.checkNotebookStatus)

	// Profile routes
	apiMux.HandleFunc("POST /v1/profile/create", app.createProfile)

	// Apply notebook creation permission middleware
	permissionMux := http.NewServeMux()
	permissionMux.HandleFunc("POST /v1/notebook/create", app.createNotebook)
	permissionMux.HandleFunc("PATCH /v1/notebook/start", app.startNotebook)
	permissionHandler := app.notebookCreationPermissionMiddleware(permissionMux)

	// Override with permission-protected routes
	apiMux.Handle("POST /v1/notebook/create", permissionHandler)
	apiMux.Handle("PATCH /v1/notebook/start", permissionHandler)

	// Apply auth middleware to all API routes
	authHandler := app.authMiddleware(apiMux)

	// Mount the authenticated API handler to the main router
	rootMux.Handle("/v1/", authHandler)

	handler := app.enableCORS(rootMux)
	handler = app.contextTimeout(handler)
	handler = app.rateLimitMiddleware(handler)
	handler = loggingMiddleware(handler)
	return http.MaxBytesHandler(handler, int64(app.env.MaxBodySizeInMB)<<20)
}

// healthCheck godoc
// @Summary      Health check endpoint
// @Description  Returns the health status of the API
// @Tags         system
// @Produce      json
// @Success      200  {object}  SwaggerHealthResponse
// @Failure      429  {object}  Error429
// @Router       /v1/health [get]
// @Security
func (app *application) healthCheck(w http.ResponseWriter, r *http.Request) {
	response := map[string]string{
		"status":  "ok",
		"version": app.env.Version,
	}
	sendResponseJson(w, getLogger(r), http.StatusOK, response)
}
