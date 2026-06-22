package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPlatformTokenSecretName(t *testing.T) {
	if got := platformTokenSecretName(" notebook-abc "); got != "notebook-abc-plt-token" {
		t.Fatalf("platformTokenSecretName trimmed name = %q", got)
	}

	longName := strings.Repeat("a", 300)
	got := platformTokenSecretName(longName)
	if len(got) > platformTokenSecretMaxNameSize {
		t.Fatalf("platformTokenSecretName length = %d, want <= %d", len(got), platformTokenSecretMaxNameSize)
	}
	if !strings.HasSuffix(got, platformTokenSecretNameSuffix) {
		t.Fatalf("platformTokenSecretName suffix = %q, want suffix %q", got, platformTokenSecretNameSuffix)
	}
}

func TestExchangeNotebookToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	userID := "00000000-0000-0000-0000-000000000001"

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "sandbox-notebook" || clientSecret != "exchange-secret" {
			t.Error("token exchange did not use confidential client authentication")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse exchange form: %v", err)
		}
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("subject_token") != "browser-access-token" {
			t.Errorf("subject_token = %q", r.Form.Get("subject_token"))
		}
		if r.Form.Get("requested_token_type") != "urn:ietf:params:oauth:token-type:refresh_token" {
			t.Errorf("requested_token_type = %q", r.Form.Get("requested_token_type"))
		}
		if r.Form.Get("audience") != "" {
			t.Errorf("audience = %q", r.Form.Get("audience"))
		}

		claims := &JWTPayload{
			Exp:           time.Now().Add(5 * time.Minute).Unix(),
			Iat:           time.Now().Unix(),
			Iss:           server.URL + "/realms/test",
			Sub:           userID,
			Typ:           "Bearer",
			Azp:           "sandbox-notebook",
			EmailVerified: true,
			KycVerified:   true,
			Email:         "user@example.com",
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
		if err != nil {
			t.Fatalf("sign delegated access token: %v", err)
		}
		_ = json.NewEncoder(w).Encode(platformTokenExchangeResponse{
			AccessToken:  signed,
			RefreshToken: "notebook-refresh-token",
			ExpiresIn:    300,
		})
	}))
	defer server.Close()

	app := application{env: ApiEnv{
		KeycloakURL:                       server.URL,
		KeycloakRealm:                     "test",
		KeycloakPublicKey:                 string(publicPEM),
		PlatformTokenExchangeClientID:     "sandbox-notebook",
		PlatformTokenExchangeClientSecret: "exchange-secret",
		PlatformTokenNotebookClientID:     "sandbox-notebook",
		PlatformTokenExchangeTokenURL:     server.URL,
		PlatformTokenExchangeScope:        "openid profile email",
		PlatformTokenExchangeAudience:     "",
	}}
	exchanged, err := app.exchangeNotebookToken(context.Background(), "browser-access-token", userID)
	if err != nil {
		t.Fatalf("exchangeNotebookToken failed: %v", err)
	}
	if exchanged.RefreshToken != "notebook-refresh-token" {
		t.Fatalf("refresh token = %q", exchanged.RefreshToken)
	}
}
