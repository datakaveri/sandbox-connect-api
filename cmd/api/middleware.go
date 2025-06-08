package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("origin")
		allowedOrigins := parseAllowedOrigins(app.env.CORS_ORIGINS)

		if origin != "" && (contains(allowedOrigins, origin) || contains(allowedOrigins, "*")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestID string
		headerRequestID := r.Header.Get("X-Request-ID")

		if headerRequestID != "" {
			parsedUUID, err := uuid.Parse(headerRequestID)
			if err == nil {
				requestID = parsedUUID.String()
			} else {
				requestID = uuid.New().String()
			}
		} else {
			requestID = uuid.New().String()
		}

		ctx := context.WithValue(r.Context(), "requestID", requestID)
		startTime := time.Now()
		ctx = context.WithValue(ctx, "startTime", startTime)
		r = r.WithContext(ctx)
		logger := getLogger(r)
		logger.Info("Request Start")
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
		logger.Info("Request End",
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
	})
}

func (app *application) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		ip := r.RemoteAddr
		logger := getLogger(r)

		if ip == "" {
			logger.Info("No IP address found, using default value for rate limiting")
			sendError(w, logger, http.StatusTooManyRequests, "No IP address found")
			return
		}
		host, _, err := net.SplitHostPort(ip)
		if err != nil {
			logger.Error("Failed to split IP address", "error", err)
			sendError(w, logger, http.StatusTooManyRequests, "Invalid IP address")
			return
		}
		if !app.rateLimiter.GetLimiter(host) {
			sendError(w, logger, http.StatusTooManyRequests, "Too Many Requests")
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

func (app *application) notebookCreationPermissionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := getLogger(r)

		userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
		if !ok {
			logger.Error("user info not found in context")
			sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
			return
		}

		var canCreateNotebook bool
		query := `SELECT can_create_notebook FROM profile WHERE user_id = $1`
		err := app.pgPool.Pool.QueryRow(r.Context(), query, userInfo.Sub).Scan(&canCreateNotebook)

		if err != nil {
			if err == pgx.ErrNoRows {
				logger.Warn("no permission record found for user", "user_id", userInfo.Sub)
				sendError(w, logger, http.StatusForbidden, "You don't have enough credit for this operation")
				return
			}

			logger.Error("failed to check user permission", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Failed to verify permission")
			return
		}

		if !canCreateNotebook {
			logger.Warn("user doesn't have permission to create notebooks", "user_id", userInfo.Sub)
			sendError(w, logger, http.StatusForbidden, "You don't have enough credit for this operation")
			return
		}

		next.ServeHTTP(w, r)
	})
}
func (app *application) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := getLogger(r)

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			logger.Warn("Unauthorized request: Missing authorization header")
			sendError(w, logger, http.StatusUnauthorized, "Unauthorized: Missing authorization header")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			logger.Warn("Unauthorized request: Invalid authorization header format")
			sendError(w, logger, http.StatusUnauthorized, "Unauthorized: Invalid authorization header format")
			return
		}
		tokenString := parts[1]

		formattedPublicKey := app.env.KeycloakPublicKey
		if !strings.Contains(formattedPublicKey, "BEGIN PUBLIC KEY") {
			formattedPublicKey = fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", formattedPublicKey)
		}

		publicKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(formattedPublicKey))
		if err != nil {
			logger.Error("Failed to parse public key", "error", err)
			sendError(w, logger, http.StatusInternalServerError, "Internal server error")
			return
		}

		var jwtPayload JWTPayload
		token, err := jwt.ParseWithClaims(tokenString, &jwtPayload, func(token *jwt.Token) (interface{}, error) {
			if token.Method != jwt.SigningMethodRS256 {
				return nil, fmt.Errorf("unexpected signing method: %v, expected RS256", token.Header["alg"])
			}
			return publicKey, nil
		}, jwt.WithValidMethods([]string{"RS256"}))

		if err != nil {
			logger.Error("Failed to validate token", "error", err)
			sendError(w, logger, http.StatusUnauthorized, "Invalid token")
			return
		}

		if !token.Valid {
			logger.Warn("Invalid token")
			sendError(w, logger, http.StatusUnauthorized, "Invalid token")
			return
		}

		if jwtPayload.Azp == "" || jwtPayload.Azp != app.env.KeycloakClientID {
			logger.Warn("Invalid client ID", "client_id", jwtPayload.Azp)
			sendError(w, logger, http.StatusUnauthorized, "Invalid client")
			return
		}

		currentTime := time.Now().Unix()
		exp, _ := jwtPayload.GetExpirationTime()
		if exp != nil && exp.Unix() < currentTime {
			logger.Warn("Token expired", "exp", exp, "current_time", currentTime)
			sendError(w, logger, http.StatusUnauthorized, "Token expired")
			return
		}

		userInfo := UserInfo{
			Sub:   jwtPayload.Sub,
			Email: jwtPayload.Email,
			Roles: jwtPayload.RealmAccess.Roles,
		}

		ctx := context.WithValue(r.Context(), UserContextKey, userInfo)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}
