package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

type JWTPayload struct {
	Azp string `json:"azp" validate:"required"`
}

type UserInfo struct {
	Sub               string `json:"sub" validate:"required,uuid"`
	EmailVerified     bool   `json:"email_verified"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Email             string `json:"email" validate:"required,email"`
}

type userContextKey string

const UserContextKey userContextKey = "user"

func (app *application) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			ip = strings.Split(forwardedFor, ",")[0]
		}

		if !app.rateLimiter.GetLimiter(ip) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type IPRateLimiter struct {
	ips           map[string]*IpRateLimiterRequests
	mu            sync.RWMutex
	ratePerMinute int
}

type IpRateLimiterRequests struct {
	count    int
	firstReq time.Time
}

func NewIPRateLimiter(r int) *IPRateLimiter {
	return &IPRateLimiter{
		ips:           make(map[string]*IpRateLimiterRequests),
		ratePerMinute: r,
		mu:            sync.RWMutex{},
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

	if now.Sub(req.firstReq) >= time.Minute {
		req.count = 1
		req.firstReq = now
		return true
	}

	if req.count < i.ratePerMinute {
		req.count++
		return true
	}

	return false
}

func (i *IPRateLimiter) GetLimiter(ip string) bool {
	return i.isAllowed(ip)
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
			msg := map[string]string{"error": "Unauthorized: Missing authorization header"}
			sendResponseJson(w, r, logger, http.StatusUnauthorized, msg)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			logger.Warn("Unauthorized request: Invalid authorization header format")
			msg := map[string]string{"error": "Unauthorized: Invalid authorization header format"}
			sendResponseJson(w, r, logger, http.StatusUnauthorized, msg)
			return
		}
		token := parts[1]

		parts = strings.Split(token, ".")
		if len(parts) != 3 {
			logger.Warn("Invalid JWT token format")
			sendResponseJson(w, r, logger, http.StatusUnauthorized, map[string]string{"error": "Invalid token format"})
			return
		}

		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			logger.Error("Failed to decode JWT payload", "error", err)
			sendResponseJson(w, r, logger, http.StatusUnauthorized, map[string]string{"error": "Invalid token format"})
			return
		}

		var payload JWTPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			logger.Error("Failed to parse JWT payload", "error", err)
			sendResponseJson(w, r, logger, http.StatusUnauthorized, map[string]string{"error": "Invalid token format"})
			return
		}

		if payload.Azp == "" || payload.Azp != app.env.KeycloakClientID {
			logger.Warn("Invalid client ID", "client_id", payload.Azp)
			sendResponseJson(w, r, logger, http.StatusUnauthorized, map[string]string{"error": "Invalid client"})
			return
		}

		userInfoURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", app.env.KeycloakURL, app.env.KeycloakRealm)
		req, err := http.NewRequest("GET", userInfoURL, nil)
		if err != nil {
			logger.Error("Failed to create userinfo request", "error", err)
			msg := map[string]string{"error": "Internal server error"}
			sendResponseJson(w, r, logger, http.StatusInternalServerError, msg)
			return
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			logger.Error("Failed to validate token with Keycloak", "error", err)
			sendResponseJson(w, r, logger, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			logger.Warn("Token validation failed", "status", resp.StatusCode)
			msg := map[string]string{"error": "Unauthorized: Invalid token"}
			sendResponseJson(w, r, logger, http.StatusUnauthorized, msg)
			return
		}

		var userInfo UserInfo
		if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
			logger.Error("Failed to decode userinfo response", "error", err)
			sendResponseJson(w, r, logger, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, userInfo)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}
