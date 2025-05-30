package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

func (app *application) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		logger := getLogger(r)

		if ip == "" {
			logger.Info("No IP address found, using default value for rate limiting")
			sendError(w, r, logger, http.StatusTooManyRequests, "No IP address found")
			return
		}
		host, _, err := net.SplitHostPort(ip)
		if err != nil {
			logger.Error("Failed to split IP address", "error", err)
			sendError(w, r, logger, http.StatusTooManyRequests, "Invalid IP address")
			return
		}
		if !app.rateLimiter.GetLimiter(host) {
			sendError(w, r, logger, http.StatusTooManyRequests, "Too Many Requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}
func (app *application) contextTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(app.env.TimeoutInSecs)*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}
func (app *application) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := slog.With(
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
		)

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			logger.Warn("Unauthorized request: Missing authorization header")
			sendError(w, r, logger, http.StatusUnauthorized, "Unauthorized: Missing authorization header")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			logger.Warn("Unauthorized request: Invalid authorization header format")
			sendError(w, r, logger, http.StatusUnauthorized, "Unauthorized: Invalid authorization header format")
			return
		}
		token := parts[1]

		parts = strings.Split(token, ".")
		if len(parts) != 3 {
			logger.Warn("Invalid JWT token format")
			sendError(w, r, logger, http.StatusUnauthorized, "Invalid token format")
			return
		}

		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			logger.Error("Failed to decode JWT payload", "error", err)
			sendError(w, r, logger, http.StatusUnauthorized, "Invalid token format")
			return
		}

		var payload JWTPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			logger.Error("Failed to parse JWT payload", "error", err)
			sendError(w, r, logger, http.StatusUnauthorized, "Invalid token format")
			return
		}

		if payload.Azp == "" || payload.Azp != app.env.KeycloakClientID {
			logger.Warn("Invalid client ID", "client_id", payload.Azp)
			sendError(w, r, logger, http.StatusUnauthorized, "Invalid client")
			return
		}

		userInfoURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", app.env.KeycloakURL, app.env.KeycloakRealm)
		req, err := http.NewRequestWithContext(r.Context(), "GET", userInfoURL, nil)
		if err != nil {
			logger.Error("Failed to create userinfo request", "error", err)
			sendError(w, r, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

		client := &http.Client{}
		resp, err := client.Do(req)

		if err != nil {
			logger.Error("Failed to validate token with Keycloak", "error", err)
			sendError(w, r, logger, http.StatusInternalServerError, "Internal server error")
			return
		}
		defer resp.Body.Close()

		// Print response headers
		logger.Info("Userinfo response headers", "headers", resp.Header)

		if resp.StatusCode != http.StatusOK {
			logger.Warn("Token validation failed", "status", resp.StatusCode)
			sendError(w, r, logger, http.StatusUnauthorized, "Unauthorized: Invalid token")
			return
		}

		var userInfo UserInfo
		if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
			logger.Error("Failed to decode userinfo response", "error", err)
			sendError(w, r, logger, http.StatusInternalServerError, "Internal server error")
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, userInfo)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}
