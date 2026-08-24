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
		w.Header().Set("X-Frame-Options", "DENY")
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
		if app.isJupyterLiteRequestPath(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}

		// IP-based rate limiting (protection against unauthenticated attacks)
		ip := getClientIP(r)
		logger := getLogger(r)
		if ip == "" {
			logger.Info("No IP address found, using default value for rate limiting")
			sendError(w, logger, http.StatusTooManyRequests, "Unable to identify client IP address")
			return
		}
		logger.Info("IP address found", "ip", ip)
		if !app.rateLimiter.GetLimiter(ip) {
			logger.Warn("IP rate limit exceeded", "ip", ip)
			sendError(w, logger, http.StatusTooManyRequests, "Too many requests in a short period. Please try again later")
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
		logger := getLogger(r)

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" && app.isJupyterLiteRequestPath(r.URL.Path) {
			if cookie, err := r.Cookie(jupyterLiteAuthCookieName); err == nil && cookie.Value != "" {
				authHeader = "Bearer " + cookie.Value
			}
		}
		if authHeader == "" {
			logger.Warn("Missing authorization header")
			sendError(w, logger, http.StatusUnauthorized, "Missing authorization header")
			return
		}

		tokenString, ok := bearerTokenFromAuthorizationHeader(authHeader)
		if !ok {
			logger.Warn("Invalid authorization header format")
			sendError(w, logger, http.StatusUnauthorized, "Invalid authorization header format")
			return
		}

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

		// Debug: Log detailed user information from JWT payload
		logger.Debug("JWT token successfully parsed and validated",
			"user_id", jwtPayload.Sub,
			"email", jwtPayload.Email,
			"name", jwtPayload.Name,
			"client_id", jwtPayload.Azp,
			"issuer", jwtPayload.Iss,
			"token_type", jwtPayload.Typ,
			"email_verified", jwtPayload.EmailVerified,
			"kyc_verified", jwtPayload.KycVerified,
			"realm_roles", jwtPayload.RealmAccess.Roles,
			"account_roles", jwtPayload.ResourceAccess.Account.Roles,
			"exp", jwtPayload.Exp,
			"iat", jwtPayload.Iat,
			"jti", jwtPayload.Jti,
		)

		expectedClientID := app.env.KeycloakClientID
		isNotebookTokenRotation := app.isNotebookTokenRotationRequest(r)
		if isNotebookTokenRotation {
			expectedClientID = app.env.PlatformTokenNotebookClientID
		}
		if jwtPayload.Azp == "" || jwtPayload.Azp != expectedClientID {
			logger.Warn("Authentication failed: Invalid client ID",
				"provided_client_id", jwtPayload.Azp,
				"expected_client_id", expectedClientID,
				"user_id", jwtPayload.Sub,
				"email", jwtPayload.Email,
			)
			sendError(w, logger, http.StatusUnauthorized, "Invalid client")
			return
		}
		if isNotebookTokenRotation && jwtPayload.Iss != app.platformTokenIssuer() {
			logger.Warn("Authentication failed: Invalid token issuer",
				"provided_issuer", jwtPayload.Iss,
				"expected_issuer", app.platformTokenIssuer(),
				"user_id", jwtPayload.Sub,
			)
			sendError(w, logger, http.StatusUnauthorized, "Invalid token issuer")
			return
		}

		currentTime := time.Now().Unix()
		exp, _ := jwtPayload.GetExpirationTime()
		if isNotebookTokenRotation && exp == nil {
			logger.Warn("Authentication failed: Missing token expiry", "user_id", jwtPayload.Sub)
			sendError(w, logger, http.StatusUnauthorized, "Invalid token expiry")
			return
		}
		if exp != nil && exp.Unix() < currentTime {
			logger.Warn("Authentication failed: Token expired",
				"exp", exp.Unix(),
				"current_time", currentTime,
				"expired_seconds_ago", currentTime-exp.Unix(),
				"user_id", jwtPayload.Sub,
				"email", jwtPayload.Email,
			)
			sendError(w, logger, http.StatusUnauthorized, "Token expired")
			return
		}

		// Check email verification
		if !jwtPayload.EmailVerified {
			logger.Warn("Authentication failed: Email not verified",
				"user_id", jwtPayload.Sub,
				"email", jwtPayload.Email,
				"name", jwtPayload.Name,
				"email_verified", jwtPayload.EmailVerified,
				"kyc_verified", jwtPayload.KycVerified,
			)
			sendError(w, logger, http.StatusUnauthorized, "Your email is not verified")
			return
		}

		if isEmailDomainBlocked(jwtPayload.Email, app.env.BlockedEmailDomains) {
			logger.Warn("Authorization failed: Email domain is blocked from sandbox access",
				"user_id", jwtPayload.Sub,
				"email", jwtPayload.Email,
			)
			sendError(w, logger, http.StatusForbidden, "Sandbox access is not allowed for this email domain")
			return
		}

		// Check KYC verification
		needKYC := app.env.KYCEnabled
		if r.URL.Path == "/v1/profile/create" ||
			r.URL.Path == "/v1/jupyterlite/session" ||
			app.isJupyterLiteRequestPath(r.URL.Path) {
			needKYC = false
		}
		if needKYC && !jwtPayload.KycVerified {
			logger.Warn("Authentication failed: KYC not verified",
				"user_id", jwtPayload.Sub,
				"email", jwtPayload.Email,
				"name", jwtPayload.Name,
				"email_verified", jwtPayload.EmailVerified,
				"kyc_verified", jwtPayload.KycVerified,
			)
			sendError(w, logger, http.StatusUnauthorized, "KYC is not verified")
			return
		}

		userInfo := UserInfo{
			Sub:      jwtPayload.Sub,
			Email:    jwtPayload.Email,
			Roles:    jwtPayload.RealmAccess.Roles,
			ClientID: jwtPayload.Azp,
		}

		// Debug: Log successful authentication with user context
		logger.Debug("User authentication successful, setting user context",
			"user_id", userInfo.Sub,
			"email", userInfo.Email,
			"roles", userInfo.Roles,
			"email_verified", jwtPayload.EmailVerified,
			"kyc_verified", jwtPayload.KycVerified,
		)

		ctx := context.WithValue(r.Context(), UserContextKey, userInfo)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

func isEmailDomainBlocked(email, blockedDomains string) bool {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}

	emailDomain := strings.TrimSpace(email[at+1:])
	for _, configuredDomain := range strings.Split(blockedDomains, ",") {
		if strings.EqualFold(emailDomain, strings.TrimSpace(configuredDomain)) {
			return true
		}
	}

	return false
}

// getClientIP extracts the real client IP address from the request,
// considering proxy headers like X-Forwarded-For, X-Real-IP, etc.
func getClientIP(r *http.Request) string {
	logger := getLogger(r)
	// Check X-Forwarded-For header first (most common)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		logger.Debug("found X-Forwarded-For header", "header_value", xff)
		// X-Forwarded-For can contain multiple IPs separated by commas
		// The first IP is usually the original client IP
		ips := strings.Split(xff, ",")
		for _, ip := range ips {
			ip = strings.TrimSpace(ip)
			if ip != "" && isValidIP(ip) {
				logger.Debug("using IP from X-Forwarded-For", "ip", ip, "method", "X-Forwarded-For")
				return ip
			}
		}
		logger.Debug("X-Forwarded-For header found but no valid IP extracted", "header_value", xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		logger.Debug("found X-Real-IP header", "header_value", xri)
		xri = strings.TrimSpace(xri)
		if isValidIP(xri) {
			logger.Debug("using IP from X-Real-IP", "ip", xri, "method", "X-Real-IP")
			return xri
		}
		logger.Debug("X-Real-IP header found but invalid IP", "header_value", xri)
	}

	// Check X-Forwarded header
	if xf := r.Header.Get("X-Forwarded"); xf != "" {
		logger.Debug("found X-Forwarded header", "header_value", xf)
		// X-Forwarded format: for=192.168.1.1;proto=http;by=proxy
		parts := strings.Split(xf, ";")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "for=") {
				ip := strings.TrimPrefix(part, "for=")
				if isValidIP(ip) {
					logger.Debug("using IP from X-Forwarded", "ip", ip, "method", "X-Forwarded")
					return ip
				}
			}
		}
		logger.Debug("X-Forwarded header found but no valid IP extracted", "header_value", xf)
	}

	// Fallback to RemoteAddr
	if r.RemoteAddr != "" {
		logger.Debug("checking RemoteAddr", "remote_addr", r.RemoteAddr)
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			// If SplitHostPort fails, RemoteAddr might be just an IP without port
			if isValidIP(r.RemoteAddr) {
				logger.Debug("using IP from RemoteAddr (no port)", "ip", r.RemoteAddr, "method", "RemoteAddr")
				return r.RemoteAddr
			}
			logger.Debug("RemoteAddr found but invalid format", "remote_addr", r.RemoteAddr, "error", err)
		} else {
			if isValidIP(host) {
				logger.Debug("using IP from RemoteAddr (with port)", "ip", host, "method", "RemoteAddr", "original", r.RemoteAddr)
				return host
			}
			logger.Debug("RemoteAddr host found but invalid IP", "host", host, "original", r.RemoteAddr)
		}
	}

	logger.Debug("no valid IP found using any method")
	return ""
}

// isValidIP checks if the given string is a valid IP address
func isValidIP(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	// Reject link-local addresses
	if parsedIP.IsLinkLocalUnicast() || parsedIP.IsLinkLocalMulticast() {
		return false
	}

	// Check for private IP ranges (you might want to allow these depending on your setup)
	if parsedIP.IsPrivate() {
		// Uncomment the line below if you want to reject private IPs
		// return false
	}

	return true
}
