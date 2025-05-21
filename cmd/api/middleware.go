package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("origin")
		if r.Method != http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		allowedOrigins := parseAllowedOrigins(app.env.CORS_ORIGINS)
		if !contains(allowedOrigins, origin) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := uuid.New().String()
		ctx := context.WithValue(r.Context(), "requestID", requestID)
		startTime := time.Now()
		ctx = context.WithValue(ctx, "startTime", startTime)
		r = r.WithContext(ctx)

		logger := getLogger(r)
		logger.Info("Request Start")
		next.ServeHTTP(w, r)

	})
}

func (app *application) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		logger := getLogger(r)
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		authHeader := r.Header.Get("Authentication")
		if authHeader != app.env.API_KEY {
			logger.Warn("Unauthorized request: Invalid or missing API key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "Unauthorized: Invalid or missing API key"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
