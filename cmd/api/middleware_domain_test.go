package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAuthMiddlewareBlockedEmailDomains(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	publicKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyDER}))

	tests := []struct {
		name           string
		blockedDomains string
		email          string
		wantStatus     int
		wantNextCalled bool
	}{
		{
			name:           "restriction disabled",
			email:          "user@cbr.synthetic.org",
			wantStatus:     http.StatusNoContent,
			wantNextCalled: true,
		},
		{
			name:           "exact domain blocked",
			blockedDomains: "cbr.synthetic.org",
			email:          "user@cbr.synthetic.org",
			wantStatus:     http.StatusForbidden,
		},
		{
			name:           "domain comparison is case insensitive",
			blockedDomains: "CBR.SYNTHETIC.ORG",
			email:          "user@cbr.synthetic.org",
			wantStatus:     http.StatusForbidden,
		},
		{
			name:           "comma separated domains",
			blockedDomains: "example.org, cbr.synthetic.org",
			email:          "user@cbr.synthetic.org",
			wantStatus:     http.StatusForbidden,
		},
		{
			name:           "different domain allowed",
			blockedDomains: "cbr.synthetic.org",
			email:          "user@example.org",
			wantStatus:     http.StatusNoContent,
			wantNextCalled: true,
		},
		{
			name:           "suffix lookalike domain allowed",
			blockedDomains: "cbr.synthetic.org",
			email:          "user@notcbr.synthetic.org",
			wantStatus:     http.StatusNoContent,
			wantNextCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const clientID = "sandbox-connect-test"
			app := &application{env: ApiEnv{
				KeycloakClientID:    clientID,
				KeycloakPublicKey:   publicKey,
				BlockedEmailDomains: tt.blockedDomains,
			}}

			token := jwt.NewWithClaims(jwt.SigningMethodRS256, &JWTPayload{
				Exp:           time.Now().Add(time.Hour).Unix(),
				Iat:           time.Now().Unix(),
				Sub:           "00000000-0000-0000-0000-000000000001",
				Azp:           clientID,
				EmailVerified: true,
				Email:         tt.email,
			})
			tokenString, err := token.SignedString(privateKey)
			if err != nil {
				t.Fatalf("sign token: %v", err)
			}

			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			req := requestWithLoggerContext(http.MethodGet, "/v1/notebook/list", nil)
			req.Header.Set("Authorization", "Bearer "+tokenString)
			rec := httptest.NewRecorder()

			app.authMiddleware(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if nextCalled != tt.wantNextCalled {
				t.Fatalf("expected next called %t, got %t", tt.wantNextCalled, nextCalled)
			}
		})
	}
}
