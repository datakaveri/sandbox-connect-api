package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// authMode selects how callers are authenticated (API_AUTH_MODE).
//
// The same binary serves every mode; nothing outside this file and
// authMiddleware branches on it.
type authMode string

const (
	// authModeStandalone is the service's original behaviour: it is reached
	// directly and verifies each caller's bearer itself.
	authModeStandalone authMode = "standalone"
	// authModeGateway puts the service behind dx-gateway-go. Every request
	// must carry a verified workload credential from a subject asserter, and
	// the end user is the subject the gateway projected.
	authModeGateway authMode = "gateway"
	// authModeHybrid is the migration posture: a request with a workload
	// credential is handled as gateway, one without falls back to standalone.
	authModeHybrid authMode = "hybrid"
)

func parseAuthMode(s string) (authMode, error) {
	switch m := authMode(strings.ToLower(strings.TrimSpace(s))); m {
	case authModeStandalone, authModeGateway, authModeHybrid:
		return m, nil
	case "":
		return authModeStandalone, nil
	default:
		return "", fmt.Errorf("API_AUTH_MODE %q: want standalone, gateway or hybrid", s)
	}
}

// workloadVerifier is the part of *workload.Verifier the service uses; an
// interface so tests need no JWKS endpoint.
type workloadVerifier interface {
	VerifyRequest(r *http.Request) (workload.Principal, error)
	MayAssertSubject(p workload.Principal) bool
}

// newWorkloadVerifier builds the verifier gateway and hybrid modes require.
//
// It refuses to return nil for those modes: a service that believes it sits
// behind the gateway but verifies no caller would trust X-Subject-* headers
// from anyone who can reach it.
func newWorkloadVerifier(mode authMode, cfg WorkloadVerifierConfig) (*workload.Verifier, error) {
	if mode == authModeStandalone {
		return nil, nil
	}
	if workload.Enforcement(cfg.Enforcement) != workload.Required {
		return nil, fmt.Errorf("API_AUTH_MODE=%s needs WORKLOAD_VERIFIER_ENFORCEMENT=required (got %q): "+
			"the projected subject is trustworthy only from a verified gateway", mode, cfg.Enforcement)
	}
	if len(cfg.SubjectAsserters) == 0 {
		return nil, fmt.Errorf("API_AUTH_MODE=%s needs WORKLOAD_VERIFIER_SUBJECT_ASSERTERS "+
			"(normally dx-gateway-go)", mode)
	}
	return workload.NewVerifier(workload.VerifierConfig{
		Enforcement:      workload.Required,
		JwksURL:          cfg.JwksURL,
		Issuer:           cfg.Issuer,
		Service:          cfg.Service,
		LeewaySeconds:    cfg.LeewaySeconds,
		AllowedCallers:   cfg.AllowedCallers,
		SubjectAsserters: cfg.SubjectAsserters,
	})
}

// gatewayAuthMiddleware authenticates a request that arrives through
// dx-gateway-go.
//
//  1. The workload credential (X-DX-Workload) is verified. A present but
//     invalid one is always rejected — never downgraded to "absent".
//  2. Self-authenticating surfaces — notebook token rotation (the platform
//     notebook token) and JupyterLite assets (the session cookie) — keep their
//     local check, with or without a gateway hop: their credential is not a
//     user token the gateway can validate.
//  3. Everything else needs the gateway's projected subject. Hybrid mode
//     falls back to the standalone bearer path when no workload credential
//     was presented; gateway mode rejects.
//  4. When the user's bearer was forwarded (keep_authorization), the local
//     claim policy still runs on it and its subject must match the projected
//     one, so email/KYC/client checks hold in every mode.
func (app *application) gatewayAuthMiddleware(next http.Handler) http.Handler {
	standalone := app.bearerAuthMiddleware(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := getLogger(r)

		p, err := app.workload.VerifyRequest(r)
		present := !errors.Is(err, workload.ErrNoCredential)
		if present && err != nil {
			logger.Warn("Authentication failed: invalid workload credential", "error", err)
			sendError(w, logger, http.StatusUnauthorized, "Invalid workload credential")
			return
		}

		if app.isSelfAuthenticating(r) {
			// The subject headers mean nothing here; drop them so no handler
			// can mistake them for the authenticated caller.
			dxheaders.Strip(r.Header)
			standalone.ServeHTTP(w, r)
			return
		}

		if !present {
			if app.authMode == authModeHybrid {
				standalone.ServeHTTP(w, r)
				return
			}
			logger.Warn("Authentication failed: no workload credential in gateway mode")
			sendError(w, logger, http.StatusUnauthorized,
				"Workload credential required: this service is reachable only through the gateway")
			return
		}

		if !app.workload.MayAssertSubject(p) {
			logger.Warn("Authentication failed: workload may not assert a subject", "workload", p.ID)
			sendError(w, logger, http.StatusForbidden, "This workload may not assert an end-user subject")
			return
		}

		subject, err := dxheaders.Parse(r.Header)
		if err != nil {
			logger.Warn("Authentication failed: no projected subject", "workload", p.ID)
			sendError(w, logger, http.StatusUnauthorized, "Missing authenticated subject")
			return
		}

		userInfo := UserInfo{Sub: subject.ID, Email: subject.Email, Roles: subject.Roles}

		if authHeader := r.Header.Get("Authorization"); authHeader != "" {
			payload, authErr := app.authenticateBearer(r, authHeader)
			if authErr != nil {
				sendError(w, logger, authErr.status, authErr.message)
				return
			}
			if payload.Sub != subject.ID {
				logger.Warn("Authentication failed: bearer subject differs from projected subject",
					"bearer_sub", payload.Sub, "subject", subject.ID)
				sendError(w, logger, http.StatusUnauthorized, "Credential subject mismatch")
				return
			}
			userInfo = userInfoFromPayload(payload)
		} else {
			if app.env.Gateway.RequireBearer {
				logger.Warn("Authentication failed: gateway did not forward the bearer")
				sendError(w, logger, http.StatusUnauthorized,
					"Missing authorization header: the gateway route must set keep_authorization")
				return
			}
			// Without the bearer the KYC claim cannot be read. Refusing is the
			// only safe answer; serving would silently drop the KYC gate.
			if app.kycRequired(r) {
				logger.Warn("Authentication failed: KYC cannot be verified without the bearer",
					"user_id", subject.ID)
				sendError(w, logger, http.StatusForbidden,
					"KYC cannot be verified: the gateway must forward the bearer")
				return
			}
		}

		logger.Debug("Gateway authentication successful",
			"user_id", userInfo.Sub, "workload", p.ID, "roles", userInfo.Roles)

		ctx := context.WithValue(r.Context(), UserContextKey, userInfo)
		ctx = workload.With(ctx, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isSelfAuthenticating reports whether the request carries its own credential
// that the service, not the gateway, must verify.
func (app *application) isSelfAuthenticating(r *http.Request) bool {
	return app.isNotebookTokenRotationRequest(r) || app.isJupyterLiteRequestPath(r.URL.Path)
}
