package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

const (
	testUserID      = "00000000-0000-0000-0000-000000000001"
	testOtherUserID = "00000000-0000-0000-0000-000000000002"
	testWorkloadTok = "Bearer workload-token"
)

// fakeVerifier accepts exactly one credential, "workload-token", as the
// configured caller.
type fakeVerifier struct {
	caller    string
	asserters map[string]bool
}

func (f fakeVerifier) VerifyRequest(r *http.Request) (workload.Principal, error) {
	switch r.Header.Get(workload.HdrWorkload) {
	case "":
		return workload.Principal{}, workload.ErrNoCredential
	case testWorkloadTok:
		return workload.Principal{ID: f.caller}, nil
	default:
		return workload.Principal{}, workload.ErrInvalidCredential
	}
}

func (f fakeVerifier) MayAssertSubject(p workload.Principal) bool { return f.asserters[p.ID] }

type testSigner struct {
	key      *rsa.PrivateKey
	clientID string
}

func newGatewayTestApp(t *testing.T, mode authMode) (*application, testSigner) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	app := &application{
		env: ApiEnv{
			KeycloakClientID:              "sandbox-connect-test",
			KeycloakPublicKey:             string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
			KeycloakURL:                   "http://keycloak:8080",
			KeycloakRealm:                 "iudx",
			PlatformTokenNotebookClientID: "sandbox-notebook",
			JupyterLiteBaseURL:            "/jupyterlite",
			BookingsEnabled:               true,
			Gateway:                       GatewayConfig{RequireBearer: true},
		},
		authMode: mode,
		workload: fakeVerifier{
			caller:    "dx-gateway-go",
			asserters: map[string]bool{"dx-gateway-go": true},
		},
	}
	return app, testSigner{key: key, clientID: app.env.KeycloakClientID}
}

func (s testSigner) bearer(t *testing.T, mutate func(*JWTPayload)) string {
	t.Helper()
	claims := &JWTPayload{
		Exp:           time.Now().Add(time.Hour).Unix(),
		Iat:           time.Now().Unix(),
		Sub:           testUserID,
		Azp:           s.clientID,
		EmailVerified: true,
		KycVerified:   true,
		Email:         "test@example.com",
		RealmAccess:   RealmAccess{Roles: []string{"consumer", "compute"}},
	}
	if mutate != nil {
		mutate(claims)
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return "Bearer " + tok
}

type gwRequest struct {
	method, path      string
	workload, bearer  string
	subject           string
	subjectRoles      string
	jupyterLiteCookie string
}

// serve runs the request through authMiddleware and returns the status and
// the UserInfo the handler saw (zero if it was not reached).
func serve(app *application, gr gwRequest) (int, UserInfo, bool) {
	method := gr.method
	if method == "" {
		method = http.MethodGet
	}
	req := requestWithLoggerContext(method, gr.path, nil)
	if gr.workload != "" {
		req.Header.Set(workload.HdrWorkload, gr.workload)
	}
	if gr.bearer != "" {
		req.Header.Set("Authorization", gr.bearer)
	}
	if gr.subject != "" {
		req.Header.Set("X-Subject-Id", gr.subject)
		req.Header.Set("X-Subject-Email", "test@example.com")
		req.Header.Set("X-Subject-Roles", gr.subjectRoles)
	}
	if gr.jupyterLiteCookie != "" {
		req.AddCookie(&http.Cookie{Name: jupyterLiteAuthCookieName, Value: gr.jupyterLiteCookie})
	}

	var (
		seen    UserInfo
		reached bool
	)
	h := app.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(UserContextKey).(UserInfo)
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, seen, reached
}

func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]authMode{
		"": authModeStandalone, "standalone": authModeStandalone,
		"gateway": authModeGateway, " Hybrid ": authModeHybrid,
	} {
		got, err := parseAuthMode(in)
		if err != nil || got != want {
			t.Errorf("parseAuthMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseAuthMode("gatway"); err == nil {
		t.Error("a misspelled mode must be a startup error, not a silent standalone")
	}
}

func TestNewWorkloadVerifierRefusesUnverifiedGatewayMode(t *testing.T) {
	if v, err := newWorkloadVerifier(authModeStandalone, WorkloadVerifierConfig{}); v != nil || err != nil {
		t.Fatalf("standalone needs no verifier; got %v, %v", v, err)
	}
	for _, mode := range []authMode{authModeGateway, authModeHybrid} {
		if _, err := newWorkloadVerifier(mode, WorkloadVerifierConfig{Enforcement: "off"}); err == nil {
			t.Errorf("%s with enforcement off must fail: the subject headers would be trusted from anyone", mode)
		}
		if _, err := newWorkloadVerifier(mode, WorkloadVerifierConfig{Enforcement: "required"}); err == nil {
			t.Errorf("%s with no subject asserters must fail", mode)
		}
	}
}

func TestGatewayModeAuthentication(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeGateway)
	good := signer.bearer(t, nil)

	cases := []struct {
		name     string
		req      gwRequest
		want     int
		wantUser string
	}{
		{"gateway hop with forwarded bearer", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID, bearer: good}, 204, testUserID},
		{"no workload credential is rejected", gwRequest{path: "/v1/notebook/list", subject: testUserID, bearer: good}, 401, ""},
		{"direct bearer alone is rejected", gwRequest{path: "/v1/notebook/list", bearer: good}, 401, ""},
		{"invalid workload credential is rejected", gwRequest{path: "/v1/notebook/list", workload: "Bearer forged", subject: testUserID, bearer: good}, 401, ""},
		{"verified workload without subject", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, bearer: good}, 401, ""},
		{"bearer for a different user than the subject", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testOtherUserID, bearer: good}, 401, ""},
		{"bearer not forwarded while required", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID}, 401, ""},
		{"email not verified still rejected behind the gateway", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID,
			bearer: signer.bearer(t, func(p *JWTPayload) { p.EmailVerified = false })}, 401, ""},
		{"wrong client still rejected behind the gateway", gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID,
			bearer: signer.bearer(t, func(p *JWTPayload) { p.Azp = "other-client" })}, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, user, _ := serve(app, tc.req)
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
			if user.Sub != tc.wantUser {
				t.Fatalf("user = %q, want %q", user.Sub, tc.wantUser)
			}
		})
	}
}

func TestGatewayModeUnlistedWorkloadMayNotAssertSubject(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeGateway)
	app.workload = fakeVerifier{caller: "dx-catalogue-go", asserters: map[string]bool{"dx-gateway-go": true}}

	code, _, reached := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok,
		subject: testUserID, bearer: signer.bearer(t, nil)})
	if code != http.StatusForbidden || reached {
		t.Fatalf("status = %d reached = %v; a non-asserter workload must not speak for a user", code, reached)
	}
}

func TestGatewayModeKYC(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeGateway)
	app.env.KYCEnabled = true
	noKYC := signer.bearer(t, func(p *JWTPayload) { p.KycVerified = false })

	if code, _, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID, bearer: noKYC}); code != 401 {
		t.Fatalf("unverified KYC via gateway: status = %d, want 401", code)
	}
	if code, _, _ := serve(app, gwRequest{method: http.MethodPost, path: "/v1/profile/create", workload: testWorkloadTok, subject: testUserID, bearer: noKYC}); code != 204 {
		t.Fatalf("profile creation is KYC-exempt: status = %d, want 204", code)
	}

	// Bearer optional: KYC cannot be read, so a KYC-gated route is refused
	// rather than served with the gate silently dropped.
	app.env.Gateway.RequireBearer = false
	if code, _, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID}); code != 403 {
		t.Fatalf("KYC without bearer: status = %d, want 403", code)
	}
	app.env.KYCEnabled = false
	code, user, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID, subjectRoles: "compute,consumer"})
	if code != 204 || user.Sub != testUserID || len(user.Roles) != 2 {
		t.Fatalf("subject-only identity: status = %d user = %+v", code, user)
	}
}

func TestGatewayModeSelfAuthenticatingSurfaces(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeGateway)
	good := signer.bearer(t, nil)

	// JupyterLite assets are served beside the gateway and carry the session
	// cookie, never a workload credential.
	if code, user, _ := serve(app, gwRequest{path: "/jupyterlite/lab/index.html", jupyterLiteCookie: good[len("Bearer "):]}); code != 204 || user.Sub != testUserID {
		t.Fatalf("jupyterlite cookie: status = %d user = %q", code, user.Sub)
	}
	if code, _, _ := serve(app, gwRequest{path: "/jupyterlite/lab/index.html"}); code != 401 {
		t.Fatalf("jupyterlite without cookie: status = %d, want 401", code)
	}

	// Notebook token rotation authenticates with the platform notebook token.
	rotation := signer.bearer(t, func(p *JWTPayload) {
		p.Azp = "sandbox-notebook"
		p.Iss = "http://keycloak:8080/realms/iudx"
	})
	for _, wl := range []string{"", testWorkloadTok} {
		code, user, _ := serve(app, gwRequest{method: http.MethodPut, path: "/v1/bookings/7/notebook-token-session",
			workload: wl, bearer: rotation, subject: testOtherUserID})
		if code != 204 || user.Sub != testUserID {
			t.Fatalf("rotation (workload %q): status = %d user = %q — the token, not the projected subject, decides", wl, code, user.Sub)
		}
	}
	if code, _, _ := serve(app, gwRequest{method: http.MethodPut, path: "/v1/bookings/7/notebook-token-session",
		workload: "Bearer forged", bearer: rotation}); code != 401 {
		t.Fatalf("rotation with an invalid workload credential: status = %d, want 401 (no downgrade)", code)
	}
}

func TestHybridModeFallsBackOnlyWithoutWorkloadCredential(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeHybrid)
	good := signer.bearer(t, nil)

	if code, user, _ := serve(app, gwRequest{path: "/v1/notebook/list", bearer: good}); code != 204 || user.Sub != testUserID {
		t.Fatalf("direct bearer in hybrid: status = %d user = %q", code, user.Sub)
	}
	if code, _, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID, bearer: good}); code != 204 {
		t.Fatalf("gateway hop in hybrid: status = %d", code)
	}
	if code, _, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: "Bearer forged", bearer: good}); code != 401 {
		t.Fatalf("invalid workload credential in hybrid: status = %d, want 401 — never a downgrade to the direct path", code)
	}
	// A client-supplied subject without a workload credential is ignored: the
	// identity is the bearer's.
	if code, user, _ := serve(app, gwRequest{path: "/v1/notebook/list", bearer: good, subject: testOtherUserID}); code != 204 || user.Sub != testUserID {
		t.Fatalf("spoofed subject in hybrid: status = %d user = %q", code, user.Sub)
	}
}

func TestStandaloneModeIgnoresGatewayHeaders(t *testing.T) {
	app, signer := newGatewayTestApp(t, authModeStandalone)
	app.workload = nil

	if code, _, _ := serve(app, gwRequest{path: "/v1/notebook/list", workload: testWorkloadTok, subject: testUserID}); code != 401 {
		t.Fatalf("standalone must not trust projected headers: status = %d", code)
	}
	if code, user, _ := serve(app, gwRequest{path: "/v1/notebook/list", bearer: signer.bearer(t, nil), subject: testOtherUserID}); code != 204 || user.Sub != testUserID {
		t.Fatalf("standalone bearer: status = %d user = %q", code, user.Sub)
	}
}

func TestFakeVerifierMatchesSentinels(t *testing.T) {
	// The middleware branches on workload.ErrNoCredential; pin that the fake
	// and the real package agree on it.
	_, err := fakeVerifier{}.VerifyRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	if !errors.Is(err, workload.ErrNoCredential) {
		t.Fatalf("absent credential = %v", err)
	}
}
