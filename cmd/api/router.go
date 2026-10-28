package main

import (
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const jupyterLiteAuthCookieName = "sandbox_jupyterlite_auth"

func (app *application) router() http.Handler {
	rootMux := http.NewServeMux()

	_ = mime.AddExtensionType(".wasm", "application/wasm")
	jupyterLiteBasePath := app.jupyterLiteBasePath()
	rootMux.Handle(jupyterLiteBasePath, app.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, jupyterLiteBasePath+"/", http.StatusMovedPermanently)
	})))
	rootMux.Handle(jupyterLiteBasePath+"/", app.authMiddleware(app.jupyterLiteStaticHandler()))

	// Serve API documentation with ReDoc (no auth required)
	rootMux.HandleFunc("/v1/apis/", app.serveReDoc)

	// Register health endpoint directly (not behind auth)
	rootMux.HandleFunc("GET /v1/health", app.healthCheck)

	apiMux := http.NewServeMux()
	for _, rt := range app.apiRoutes() {
		apiMux.HandleFunc(rt.pattern, rt.handler)
	}

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

// apiRoute is one authenticated API route. Patterns with a method are
// operations and must be declared in openapi/openapi.yaml (the conformance
// test checks it); method-less patterns are the 404 responders for the
// booking surface when bookings are disabled.
type apiRoute struct {
	pattern string
	handler http.HandlerFunc
}

// apiRoutes is the authenticated route table for the configured booking mode.
func (app *application) apiRoutes() []apiRoute {
	// Standard notebook read/status routes (non-permission protected)
	routes := []apiRoute{
		{"GET /v1/notebook/list", app.listNotebooks},
		{"GET /v1/notebook/check-exists/{notebook_name}", app.checkNotebookExists},
		{"GET /v1/notebook/status/{notebook_name}", app.checkNotebookStatus},
		{"GET /v1/notebook/instance-types", app.listGPUInstanceTypes},
		{"GET /v1/notebook/gpu-instance-types", app.listGPUInstanceTypes},
	}
	if app.env.BookingsEnabled {
		routes = append(routes,
			apiRoute{"GET /v1/categories", app.listGPUCategories},
			apiRoute{"POST /v1/bookings", app.createGPUBooking},
			apiRoute{"GET /v1/bookings", app.listGPUBookings},
			apiRoute{"PATCH /v1/bookings/{id}/cancel", app.cancelGPUBooking},
			apiRoute{"PATCH /v1/bookings/{id}/extend", app.extendGPUBooking},
			apiRoute{"PATCH /v1/bookings/{id}/reset", app.resetGPUBooking},
			apiRoute{"PATCH /v1/bookings/{id}/terminate", app.terminateGPUBooking},
			apiRoute{"POST /v1/bookings/{id}/notebook-token-session", app.createNotebookTokenSession},
			apiRoute{"PUT /v1/bookings/{id}/notebook-token-session", app.rotateNotebookTokenSession},
			apiRoute{"GET /v1/slots/available", app.listGPUAvailableSlots},
			apiRoute{"GET /v1/slots/calendar", app.listGPUCalendarSlots},
		)
	} else {
		routes = append(routes,
			apiRoute{"POST /v1/notebook/create", app.createNotebook},
			apiRoute{"PATCH /v1/notebook/start", app.startNotebook},
			apiRoute{"PATCH /v1/notebook/stop", app.stopNotebook},
			apiRoute{"DELETE /v1/notebook/delete", app.deleteNotebook},
			apiRoute{"POST /v1/notebook/{notebook_name}/notebook-token-session", app.createDirectNotebookTokenSession},
			apiRoute{"PUT /v1/notebook/{notebook_name}/notebook-token-session", app.rotateDirectNotebookTokenSession},
			apiRoute{"POST /v1/bookings/{id}/notebook-token-session", app.createNotebookTokenSession},
			apiRoute{"PUT /v1/bookings/{id}/notebook-token-session", app.rotateNotebookTokenSession},
			apiRoute{"/v1/bookings", app.bookingsDisabled},
			apiRoute{"/v1/bookings/", app.bookingsDisabled},
			apiRoute{"/v1/slots/", app.bookingsDisabled},
			apiRoute{"/v1/categories", app.bookingsDisabled},
		)
	}
	routes = append(routes,
		apiRoute{"POST /v1/jupyterlite/session", app.createJupyterLiteSession},
		// Profile routes
		apiRoute{"POST /v1/profile/create", app.createProfile},
	)
	return routes
}

func (app *application) bookingsDisabled(w http.ResponseWriter, r *http.Request) {
	sendError(w, getLogger(r), http.StatusNotFound, "Bookings and scheduling are disabled")
}

func (app *application) jupyterLiteBasePath() string {
	base := strings.TrimSpace(app.env.JupyterLiteServePath)
	if base == "" {
		base = strings.TrimSpace(app.env.JupyterLiteBaseURL)
	}
	if base == "" {
		base = "/jupyterlite"
	}
	if u, err := url.Parse(base); err == nil && u.IsAbs() {
		base = u.Path
	}
	if base == "" || base == "/" {
		base = "/jupyterlite"
	}
	base = "/" + strings.Trim(base, "/")
	return base
}

func (app *application) jupyterLiteLaunchURL() string {
	base := strings.TrimRight(strings.TrimSpace(app.env.JupyterLiteBaseURL), "/")
	if base == "" {
		base = "/jupyterlite"
	}
	return base + "/lab/index.html"
}

func bearerTokenFromAuthorizationHeader(authHeader string) (string, bool) {
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (app *application) isJupyterLiteRequestPath(requestPath string) bool {
	basePath := app.jupyterLiteBasePath()
	return requestPath == basePath || strings.HasPrefix(requestPath, basePath+"/")
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// @Summary      Create JupyterLite launch session
// @Description  Validates the current bearer token and sets an HttpOnly cookie so browser navigations to JupyterLite can authenticate without putting the token in the URL.
// @Tags         jupyterlite
// @Produce      json
// @Success      200  {object}  JupyterLiteSessionResponse
// @Failure      401  {object}  Error401
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @ID           createJupyterLiteSession
// @x-dx-authz   {"authentication":"required","authorization":"identity"}
// @Router       /v1/jupyterlite/session [post]
func (app *application) createJupyterLiteSession(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	tokenString, ok := bearerTokenFromAuthorizationHeader(r.Header.Get("Authorization"))
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Invalid authorization header format")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     jupyterLiteAuthCookieName,
		Value:    tokenString,
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	sendResponseJson(w, logger, http.StatusOK, JupyterLiteSessionResponse{
		LaunchURL: app.jupyterLiteLaunchURL(),
	})
}

func (app *application) jupyterLiteStaticHandler() http.Handler {
	basePath := app.jupyterLiteBasePath()
	fs := http.Dir(app.env.JupyterLiteStaticDir)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, basePath)
		rel = "/" + strings.TrimLeft(rel, "/")
		if rel == "/" {
			rel = "/index.html"
		}
		name := strings.TrimPrefix(path.Clean(rel), "/")

		file, err := fs.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close() //nolint:errcheck // read-only file

		stat, err := file.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if stat.IsDir() {
			_ = file.Close()
			name = path.Join(name, "index.html")
			file, err = fs.Open(name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer file.Close() //nolint:errcheck // read-only file
			stat, err = file.Stat()
			if err != nil || stat.IsDir() {
				http.NotFound(w, r)
				return
			}
		}

		http.ServeContent(w, r, name, stat.ModTime(), file)
	})
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

	if path == "swagger.json" || path == "swagger.yaml" || path == "openapi.yaml" {
		// openapi.yaml is the OpenAPI 3 operation manifest the gateway
		// enforces; the swagger files are the human-facing reference.
		dir := "docs"
		if path == "openapi.yaml" {
			dir = "openapi"
		}
		filePath := filepath.Join("/app", dir, path)

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			filePath = filepath.Join(dir, path)
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

// @Summary      Health check
// @Description  Liveness/readiness probe. Unauthenticated.
// @Tags         health
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @ID           healthCheck
// @x-dx-authz   {"authentication":"none"}
// @Router       /v1/health [get]
func (app *application) healthCheck(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	sendResponseJson(w, logger, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"version": app.env.Version,
	})
}
