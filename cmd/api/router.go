package main

import (
	"net/http"
)

func (app *application) router() http.Handler {
	rootMux := http.NewServeMux()

	apiMux := http.NewServeMux()

	apiMux.HandleFunc("POST /notebook/create", app.createNotebook)
	apiMux.HandleFunc("PATCH /notebook/stop", app.stopNotebook)
	apiMux.HandleFunc("PATCH /notebook/start", app.startNotebook)
	apiMux.HandleFunc("DELETE /notebook/delete", app.deleteNotebook)
	apiMux.HandleFunc("GET /notebook/list", app.listNotebooks)
	apiMux.HandleFunc("GET /notebook/check-exists/{notebook_name}", app.checkNotebookExists)
	apiMux.HandleFunc("GET /notebook/status/{notebook_name}", app.checkNotebookStatus)
	apiMux.HandleFunc("POST /profile/create", app.createProfile)
	authHandler := app.authMiddleware(apiMux)
	rootMux.HandleFunc("GET /health", healthCheck)
	rootMux.Handle("/", authHandler)
	handler := app.enableCORS(rootMux)
	handler = app.contextTimeout(handler)
	handler = app.rateLimitMiddleware(handler)
	handler = loggingMiddleware(handler)

	return http.MaxBytesHandler(handler, int64(app.env.MaxBodySizeInMB)<<20)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	sendResponse(w, r, getLogger(r), http.StatusOK, "ok")
}
