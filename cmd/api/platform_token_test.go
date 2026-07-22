package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	k8spkg "sandbox-backend-service/pkg/k8s"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestCreateOrUpdatePlatformTokenSecretSetsNotebookOwnerReference(t *testing.T) {
	ctx := context.Background()
	namespace := "user-namespace"
	notebookName := "demo-notebook"
	notebookUID := "11111111-2222-3333-4444-555555555555"

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	notebook := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubeflow.org/v1beta1",
		"kind":       "Notebook",
		"metadata": map[string]any{
			"name":      notebookName,
			"namespace": namespace,
			"uid":       notebookUID,
		},
	}}
	client := dynamicfake.NewSimpleDynamicClient(scheme, notebook)
	app := application{
		env:       ApiEnv{PlatformTokenExchangeClientSecret: "client-secret"},
		k8sClient: &k8spkg.K8sClient{Dynamic: client},
	}

	secretName, err := app.createOrUpdatePlatformTokenSecret(ctx, namespace, notebookName, nil, namespace, "refresh-token", "access-token", "session-1")
	if err != nil {
		t.Fatalf("createOrUpdatePlatformTokenSecret failed: %v", err)
	}
	secret, err := client.Resource(platformTokenSecretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get token secret: %v", err)
	}
	refs := secret.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("ownerReferences length = %d, want 1", len(refs))
	}
	ref := refs[0]
	if ref.APIVersion != "kubeflow.org/v1beta1" || ref.Kind != "Notebook" || ref.Name != notebookName || string(ref.UID) != notebookUID {
		t.Fatalf("ownerReference = %#v", ref)
	}
	if got := platformTokenSessionIDFromSecret(secret); got != "session-1" {
		t.Fatalf("bootstrap session ID = %q, want session-1", got)
	}
	data, found, err := unstructured.NestedStringMap(secret.Object, "data")
	if err != nil || !found || data[platformTokenBootstrapKey] == "" {
		t.Fatalf("bootstrap token data missing: found=%v err=%v data=%#v", found, err, data)
	}
	if _, err := app.createOrUpdatePlatformTokenSecret(ctx, namespace, notebookName, nil, namespace, "refresh-token-2", "access-token-2", ""); err != nil {
		t.Fatalf("rotate platform token secret: %v", err)
	}
	secret, err = client.Resource(platformTokenSecretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get rotated token secret: %v", err)
	}
	if got := platformTokenSessionIDFromSecret(secret); got != "session-1" {
		t.Fatalf("rotated bootstrap session ID = %q, want session-1", got)
	}
	data, _, _ = unstructured.NestedStringMap(secret.Object, "data")
	decoded, err := base64.StdEncoding.DecodeString(data[platformTokenBootstrapKey])
	if err != nil {
		t.Fatalf("decode rotated bootstrap: %v", err)
	}
	var bootstrap platformTokenBootstrap
	if err := json.Unmarshal(decoded, &bootstrap); err != nil {
		t.Fatalf("parse rotated bootstrap: %v", err)
	}
	if bootstrap.AccessToken != "access-token-2" {
		t.Fatalf("rotated bootstrap access token = %q", bootstrap.AccessToken)
	}
}

func TestIsPlatformTokenReadyRequiresMatchingSession(t *testing.T) {
	readyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("sessionId")
		if sessionID != "session-1" {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(platformTokenReadyResponse{Status: "ready", SessionID: sessionID})
	}))
	defer readyServer.Close()
	_, portString, err := net.SplitHostPort(strings.TrimPrefix(readyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("parse readiness server address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse readiness server port: %v", err)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      "demo-notebook-0",
			"namespace": "user-namespace",
			"labels": map[string]any{
				platformTokenNotebookLabel: "demo-notebook",
			},
		},
		"status": map[string]any{"podIP": "127.0.0.1"},
	}}
	client := dynamicfake.NewSimpleDynamicClient(scheme, pod)
	app := application{
		env:       ApiEnv{PlatformTokenReadyPort: port},
		k8sClient: &k8spkg.K8sClient{Dynamic: client},
	}

	ready, err := app.isPlatformTokenReady(context.Background(), readyServer.Client(), "user-namespace", "demo-notebook", "session-1")
	if err != nil || !ready {
		t.Fatalf("matching session ready=%v err=%v", ready, err)
	}
	ready, err = app.isPlatformTokenReady(context.Background(), readyServer.Client(), "user-namespace", "demo-notebook", "session-2")
	if err != nil || ready {
		t.Fatalf("mismatched session ready=%v err=%v", ready, err)
	}
}

func TestDeletePlatformTokenSecret(t *testing.T) {
	ctx := context.Background()
	namespace := "user-namespace"
	notebookName := "demo-notebook"
	secretName := platformTokenSecretName(notebookName)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": namespace,
		},
	}}
	client := dynamicfake.NewSimpleDynamicClient(scheme, secret)
	app := application{k8sClient: &k8spkg.K8sClient{Dynamic: client}}

	if err := app.deletePlatformTokenSecret(ctx, namespace, notebookName); err != nil {
		t.Fatalf("deletePlatformTokenSecret failed: %v", err)
	}
	_, err := client.Resource(platformTokenSecretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("secret lookup error = %v, want not found", err)
	}
}

func TestDeletePlatformTokenSecretIgnoresMissingSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	client := dynamicfake.NewSimpleDynamicClient(scheme)
	app := application{k8sClient: &k8spkg.K8sClient{Dynamic: client}}

	if err := app.deletePlatformTokenSecret(context.Background(), "user-namespace", "missing-notebook"); err != nil {
		t.Fatalf("deletePlatformTokenSecret missing secret error = %v", err)
	}
}
