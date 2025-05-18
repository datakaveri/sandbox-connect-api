package main

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var allowedOrigins = []string{"http://localhost:3000"}

func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("origin")
		if !contains(allowedOrigins, origin) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
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
