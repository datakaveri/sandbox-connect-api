package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	platformTokenSecretLabel       = "sandbox-connect/platform-token"
	platformTokenSecretKey         = "refresh_token"
	platformTokenClientSecretKey   = "client_secret"
	platformTokenSecretNameSuffix  = "-plt-token"
	platformTokenSecretMaxNameSize = 253
)

var platformTokenSecretGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "secrets",
}

func platformTokenSecretName(notebookName string) string {
	name := strings.TrimSpace(notebookName)
	maxBaseLen := platformTokenSecretMaxNameSize - len(platformTokenSecretNameSuffix)
	if len(name) > maxBaseLen {
		name = strings.TrimRight(name[:maxBaseLen], "-")
	}
	return name + platformTokenSecretNameSuffix
}

func (app *application) platformTokenSecretOwnerReference(ctx context.Context, namespace, notebookName string) (*metav1.OwnerReference, error) {
	if namespace == "" || notebookName == "" {
		return nil, nil
	}
	notebookGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1beta1",
		Resource: "notebooks",
	}
	notebook, err := app.k8sClient.Dynamic.Resource(notebookGVR).Namespace(namespace).Get(ctx, notebookName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	uid := notebook.GetUID()
	if uid == "" {
		return nil, nil
	}
	return &metav1.OwnerReference{
		APIVersion: "kubeflow.org/v1beta1",
		Kind:       "Notebook",
		Name:       notebookName,
		UID:        uid,
	}, nil
}

func (app *application) createOrUpdatePlatformTokenSecret(ctx context.Context, namespace, notebookName string, bookingID *int64, userID, refreshToken string) (string, error) {
	secretName := platformTokenSecretName(notebookName)
	clientSecret := strings.TrimSpace(app.env.PlatformTokenExchangeClientSecret)
	if clientSecret == "" {
		return "", fmt.Errorf("platform token client secret is not configured")
	}
	encodedToken := base64.StdEncoding.EncodeToString([]byte(refreshToken))
	encodedClientSecret := base64.StdEncoding.EncodeToString([]byte(clientSecret))
	labels := map[string]string{
		platformTokenSecretLabel:                 "true",
		"sandbox-connect/notebook-name":          notebookName,
		"sandbox-connect/user-id":                userID,
		"app.kubernetes.io/managed-by":           "sandbox-connect",
		"app.kubernetes.io/component":            "platform-token",
		"app.kubernetes.io/part-of":              "sandbox-connect",
		"sandbox-connect/platform-token-version": "v1",
	}
	if bookingID != nil && *bookingID > 0 {
		labels["sandbox-connect/booking-id"] = strconv.FormatInt(*bookingID, 10)
	}

	ownerRef, err := app.platformTokenSecretOwnerReference(ctx, namespace, notebookName)
	if err != nil {
		return "", fmt.Errorf("load platform token secret owner: %w", err)
	}

	newSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": namespace,
		},
		"type": "Opaque",
		"data": map[string]any{
			platformTokenSecretKey:       encodedToken,
			platformTokenClientSecretKey: encodedClientSecret,
		},
	}}
	newSecret.SetLabels(labels)
	if ownerRef != nil {
		newSecret.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})
	}

	existing, err := app.k8sClient.Dynamic.Resource(platformTokenSecretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = app.k8sClient.Dynamic.Resource(platformTokenSecretGVR).Namespace(namespace).Create(ctx, newSecret, metav1.CreateOptions{})
			return secretName, err
		}
		return "", err
	}

	data, found, err := unstructured.NestedStringMap(existing.Object, "data")
	if err != nil || !found {
		data = map[string]string{}
	}
	data[platformTokenSecretKey] = encodedToken
	data[platformTokenClientSecretKey] = encodedClientSecret
	unstructured.SetNestedStringMap(existing.Object, data, "data")

	existingLabels := existing.GetLabels()
	if existingLabels == nil {
		existingLabels = map[string]string{}
	}
	for key, value := range labels {
		existingLabels[key] = value
	}
	if bookingID == nil || *bookingID <= 0 {
		delete(existingLabels, "sandbox-connect/booking-id")
	}
	existing.SetLabels(existingLabels)
	if ownerRef != nil {
		existing.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})
	}

	_, err = app.k8sClient.Dynamic.Resource(platformTokenSecretGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return secretName, err
}

func (app *application) deletePlatformTokenSecret(ctx context.Context, namespace, notebookName string) error {
	if namespace == "" || notebookName == "" {
		return nil
	}
	err := app.k8sClient.Dynamic.Resource(platformTokenSecretGVR).Namespace(namespace).Delete(ctx, platformTokenSecretName(notebookName), metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

type platformTokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type notebookTokenBooking struct {
	Status       string
	NotebookName string
	Namespace    *string
	Name         *string
	LatestEvent  *string
}

func (app *application) isNotebookTokenRotationRequest(r *http.Request) bool {
	if r.Method != http.MethodPut {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[2] == "" || parts[3] != "notebook-token-session" {
		return false
	}
	if parts[1] == "bookings" {
		return true
	}
	return !app.env.BookingsEnabled && parts[1] == "notebook"
}

func (app *application) platformTokenEndpoint() string {
	if endpoint := strings.TrimSpace(app.env.PlatformTokenExchangeTokenURL); endpoint != "" {
		return endpoint
	}
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		strings.TrimRight(strings.TrimSpace(app.env.KeycloakURL), "/"),
		url.PathEscape(strings.TrimSpace(app.env.KeycloakRealm)))
}

func (app *application) platformTokenIssuer() string {
	return fmt.Sprintf("%s/realms/%s",
		strings.TrimRight(strings.TrimSpace(app.env.KeycloakURL), "/"),
		url.PathEscape(strings.TrimSpace(app.env.KeycloakRealm)))
}

func (app *application) validateDelegatedAccessToken(tokenString, expectedUserID string) error {
	formattedPublicKey := app.env.KeycloakPublicKey
	if !strings.Contains(formattedPublicKey, "BEGIN PUBLIC KEY") {
		formattedPublicKey = fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", formattedPublicKey)
	}
	publicKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(formattedPublicKey))
	if err != nil {
		return fmt.Errorf("parse Keycloak public key: %w", err)
	}
	claims := &JWTPayload{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return publicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(app.platformTokenIssuer()))
	if err != nil || !token.Valid {
		return fmt.Errorf("invalid delegated access token")
	}
	if claims.Sub != expectedUserID {
		return fmt.Errorf("delegated token subject does not match booking owner")
	}
	if claims.Azp != strings.TrimSpace(app.env.PlatformTokenNotebookClientID) {
		return fmt.Errorf("delegated token client does not match notebook client")
	}
	return nil
}

func (app *application) exchangeNotebookToken(ctx context.Context, subjectToken, expectedUserID string) (*platformTokenExchangeResponse, error) {
	clientID := strings.TrimSpace(app.env.PlatformTokenExchangeClientID)
	clientSecret := strings.TrimSpace(app.env.PlatformTokenExchangeClientSecret)
	notebookClientID := strings.TrimSpace(app.env.PlatformTokenNotebookClientID)
	if clientID == "" || clientSecret == "" || notebookClientID == "" {
		return nil, fmt.Errorf("platform token exchange is not configured")
	}
	if clientID != notebookClientID {
		return nil, fmt.Errorf("platform token exchange and notebook client IDs must match")
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("subject_token", subjectToken)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:access_token")
	form.Set("requested_token_type", "urn:ietf:params:oauth:token-type:refresh_token")
	if scope := strings.TrimSpace(app.env.PlatformTokenExchangeScope); scope != "" {
		form.Set("scope", scope)
	}
	if audience := strings.TrimSpace(app.env.PlatformTokenExchangeAudience); audience != "" {
		form.Set("audience", audience)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, app.platformTokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Keycloak token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Keycloak token exchange returned status %d", resp.StatusCode)
	}
	var exchanged platformTokenExchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&exchanged); err != nil {
		return nil, fmt.Errorf("decode Keycloak token exchange response: %w", err)
	}
	exchanged.AccessToken = strings.TrimSpace(exchanged.AccessToken)
	exchanged.RefreshToken = strings.TrimSpace(exchanged.RefreshToken)
	if exchanged.AccessToken == "" || exchanged.RefreshToken == "" {
		return nil, fmt.Errorf("Keycloak token exchange did not return access and refresh tokens")
	}
	if err := app.validateDelegatedAccessToken(exchanged.AccessToken, expectedUserID); err != nil {
		return nil, err
	}
	return &exchanged, nil
}

func (app *application) loadOwnedNotebookTokenBooking(ctx context.Context, bookingID int64, userID string) (*notebookTokenBooking, error) {
	var booking notebookTokenBooking
	err := app.pgPool.Pool.QueryRow(ctx, `
		SELECT b.status, b.notebook_name,
		       n.namespace, n.name, n.events[array_upper(n.events, 1)]::text AS notebook_latest_event
		FROM bookings b
		LEFT JOIN LATERAL (
			SELECT namespace, name, events
			FROM notebooks
			WHERE events[array_upper(events, 1)] <> 'deleted'
			  AND (id = b.notebook_id OR booking_id = b.id)
			ORDER BY id DESC
			LIMIT 1
		) n ON true
		WHERE b.id = $1 AND b.user_id = $2
	`, bookingID, userID).Scan(&booking.Status, &booking.NotebookName, &booking.Namespace, &booking.Name, &booking.LatestEvent)
	if err != nil {
		return nil, err
	}
	return &booking, nil
}

func (app *application) loadOwnedNotebookTokenNotebook(ctx context.Context, notebookName, userID string) (*notebookTokenBooking, error) {
	var notebook notebookTokenBooking
	err := app.pgPool.Pool.QueryRow(ctx, `
		SELECT namespace, name, events[array_upper(events, 1)]::text AS notebook_latest_event
		FROM notebooks
		WHERE namespace = $1
		  AND name = $2
		  AND events[array_upper(events, 1)] <> 'deleted'
	`, userID, notebookName).Scan(&notebook.Namespace, &notebook.Name, &notebook.LatestEvent)
	if err != nil {
		return nil, err
	}
	notebook.Status = "direct"
	notebook.NotebookName = notebookName
	return &notebook, nil
}

func validateNotebookTokenBooking(booking *notebookTokenBooking, userID string) error {
	if booking.Status != "ready" && booking.Status != "active" {
		return fmt.Errorf("only ready or active bookings can create notebook token sessions")
	}
	if booking.Namespace != nil && *booking.Namespace != userID {
		return fmt.Errorf("booking notebook namespace mismatch")
	}
	if booking.Status == "active" &&
		(booking.Name == nil || booking.LatestEvent == nil || *booking.LatestEvent != string(constants.StatusNotebookApplied)) {
		return fmt.Errorf("notebook is not ready for token session")
	}
	return nil
}

func validateNotebookTokenNotebook(notebook *notebookTokenBooking, userID string) error {
	if notebook.Namespace == nil || *notebook.Namespace != userID {
		return fmt.Errorf("notebook namespace mismatch")
	}
	if notebook.Name == nil || strings.TrimSpace(*notebook.Name) == "" {
		return fmt.Errorf("notebook not found")
	}
	return nil
}

func parseNotebookTokenBookingID(r *http.Request) (int64, error) {
	bookingID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || bookingID <= 0 {
		return 0, fmt.Errorf("invalid booking id")
	}
	return bookingID, nil
}

// @Summary      Create notebook token session
// @Description  Exchanges the caller access token for a notebook-specific delegated refresh token and stores it in a notebook-scoped Kubernetes Secret. Browser refresh tokens are never accepted or stored.
// @Tags         bookings
// @Produce      json
// @Param        id  path  int  true  "Booking ID"
// @Success      200  {object}  NotebookTokenSessionResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/notebook-token-session [post]
func (app *application) createNotebookTokenSession(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok || userInfo.ClientID != app.env.KeycloakClientID {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	bookingID, err := parseNotebookTokenBookingID(r)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	booking, err := app.loadOwnedNotebookTokenBooking(r.Context(), bookingID, userInfo.Sub)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	if err := validateNotebookTokenBooking(booking, userInfo.Sub); err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	subjectToken, ok := bearerTokenFromAuthorizationHeader(r.Header.Get("Authorization"))
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Invalid authorization header format")
		return
	}
	exchanged, err := app.exchangeNotebookToken(r.Context(), subjectToken, userInfo.Sub)
	if err != nil {
		logger.Error("failed to exchange notebook token", "error", err, "booking_id", bookingID)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	if err := app.waitForNamespace(r.Context(), logger, userInfo.Sub); err != nil {
		logger.Error("namespace not ready for platform token setup", "error", err, "namespace", userInfo.Sub)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	secretName, err := app.createOrUpdatePlatformTokenSecret(r.Context(), userInfo.Sub, booking.NotebookName, &bookingID, userInfo.Sub, exchanged.RefreshToken)
	if err != nil {
		logger.Error("failed to create platform token secret", "error", err, "namespace", userInfo.Sub, "secret", secretName)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	sendResponseJson(w, logger, http.StatusOK, NotebookTokenSessionResponse{
		BookingID: bookingID, Status: "ready", SecretName: secretName,
	})
}

// @Summary      Create direct notebook token session
// @Description  Exchanges the caller access token for a notebook-specific delegated refresh token and stores it in a notebook-scoped Kubernetes Secret. Available for direct notebooks when API_BOOKINGS_ENABLED=false.
// @Tags         notebook
// @Produce      json
// @Param        notebook_name  path  string  true  "Notebook Name"
// @Success      200  {object}  NotebookTokenSessionResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/{notebook_name}/notebook-token-session [post]
func (app *application) createDirectNotebookTokenSession(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok || userInfo.ClientID != app.env.KeycloakClientID {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	notebookName := strings.TrimSpace(r.PathValue("notebook_name"))
	if errorMessage, gotError := getErrorMessageForNotebookName(notebookName); gotError {
		sendError(w, logger, http.StatusBadRequest, errorMessage)
		return
	}
	notebook, err := app.loadOwnedNotebookTokenNotebook(r.Context(), notebookName, userInfo.Sub)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	if err := validateNotebookTokenNotebook(notebook, userInfo.Sub); err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	subjectToken, ok := bearerTokenFromAuthorizationHeader(r.Header.Get("Authorization"))
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Invalid authorization header format")
		return
	}
	exchanged, err := app.exchangeNotebookToken(r.Context(), subjectToken, userInfo.Sub)
	if err != nil {
		logger.Error("failed to exchange direct notebook token", "error", err, "notebook_name", notebookName)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	if err := app.waitForNamespace(r.Context(), logger, userInfo.Sub); err != nil {
		logger.Error("namespace not ready for platform token setup", "error", err, "namespace", userInfo.Sub)
		sendError(w, logger, http.StatusInternalServerError, "Internal server error")
		return
	}
	secretName, err := app.createOrUpdatePlatformTokenSecret(r.Context(), userInfo.Sub, notebook.NotebookName, nil, userInfo.Sub, exchanged.RefreshToken)
	if err != nil {
		logger.Error("failed to create platform token secret", "error", err, "namespace", userInfo.Sub, "secret", secretName)
		sendError(w, logger, http.StatusInternalServerError, "Failed to create notebook token session")
		return
	}
	sendResponseJson(w, logger, http.StatusOK, NotebookTokenSessionResponse{
		NotebookName: notebook.NotebookName, Status: "ready", SecretName: secretName,
	})
}

// @Summary      Persist direct notebook refresh-token rotation
// @Description  Replaces the direct notebook refresh token after Keycloak rotation. This route accepts only a delegated notebook-client access token belonging to the notebook owner. Available when API_BOOKINGS_ENABLED=false.
// @Tags         notebook
// @Accept       json
// @Produce      json
// @Param        notebook_name  path  string                        true  "Notebook Name"
// @Param        request        body  NotebookTokenRotationRequest  true  "Rotated refresh token"
// @Success      200  {object}  NotebookTokenSessionResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      422  {object}  Error422
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/notebook/{notebook_name}/notebook-token-session [put]
func (app *application) rotateDirectNotebookTokenSession(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok || userInfo.ClientID != app.env.PlatformTokenNotebookClientID {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	notebookName := strings.TrimSpace(r.PathValue("notebook_name"))
	if errorMessage, gotError := getErrorMessageForNotebookName(notebookName); gotError {
		sendError(w, logger, http.StatusBadRequest, errorMessage)
		return
	}
	req, err := utils.DecodeAndValidate[NotebookTokenRotationRequest](r.Body, logger)
	if err != nil || strings.TrimSpace(req.RefreshToken) == "" {
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid body")
		return
	}
	notebook, err := app.loadOwnedNotebookTokenNotebook(r.Context(), notebookName, userInfo.Sub)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Notebook not found")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Failed to update notebook token session")
		return
	}
	if err := validateNotebookTokenNotebook(notebook, userInfo.Sub); err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	secretName, err := app.createOrUpdatePlatformTokenSecret(r.Context(), userInfo.Sub, notebook.NotebookName, nil, userInfo.Sub, strings.TrimSpace(req.RefreshToken))
	if err != nil {
		logger.Error("failed to update platform token secret", "error", err, "namespace", userInfo.Sub, "secret", secretName)
		sendError(w, logger, http.StatusInternalServerError, "Failed to update notebook token session")
		return
	}
	sendResponseJson(w, logger, http.StatusOK, NotebookTokenSessionResponse{
		NotebookName: notebook.NotebookName, Status: "updated", SecretName: secretName,
	})
}

// @Summary      Persist notebook refresh-token rotation
// @Description  Replaces the notebook refresh token after Keycloak rotation. This route accepts only a delegated notebook-client access token belonging to the booking owner.
// @Tags         bookings
// @Accept       json
// @Produce      json
// @Param        id       path  int                           true  "Booking ID"
// @Param        request  body  NotebookTokenRotationRequest  true  "Rotated refresh token"
// @Success      200  {object}  NotebookTokenSessionResponse
// @Failure      400  {object}  Error400
// @Failure      401  {object}  Error401
// @Failure      404  {object}  Error404
// @Failure      422  {object}  Error422
// @Failure      429  {object}  Error429
// @Failure      500  {object}  Error500
// @Security     BearerAuth
// @Router       /v1/bookings/{id}/notebook-token-session [put]
func (app *application) rotateNotebookTokenSession(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	userInfo, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok || userInfo.ClientID != app.env.PlatformTokenNotebookClientID {
		sendResponse(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	bookingID, err := parseNotebookTokenBookingID(r)
	if err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	req, err := utils.DecodeAndValidate[NotebookTokenRotationRequest](r.Body, logger)
	if err != nil || strings.TrimSpace(req.RefreshToken) == "" {
		sendError(w, logger, http.StatusUnprocessableEntity, "Invalid body")
		return
	}
	booking, err := app.loadOwnedNotebookTokenBooking(r.Context(), bookingID, userInfo.Sub)
	if err != nil {
		if err == pgx.ErrNoRows {
			sendError(w, logger, http.StatusNotFound, "Booking not found")
			return
		}
		sendError(w, logger, http.StatusInternalServerError, "Failed to update notebook token session")
		return
	}
	if err := validateNotebookTokenBooking(booking, userInfo.Sub); err != nil {
		sendError(w, logger, http.StatusBadRequest, err.Error())
		return
	}
	secretName, err := app.createOrUpdatePlatformTokenSecret(r.Context(), userInfo.Sub, booking.NotebookName, &bookingID, userInfo.Sub, strings.TrimSpace(req.RefreshToken))
	if err != nil {
		logger.Error("failed to update platform token secret", "error", err, "namespace", userInfo.Sub, "secret", secretName)
		sendError(w, logger, http.StatusInternalServerError, "Failed to update notebook token session")
		return
	}
	sendResponseJson(w, logger, http.StatusOK, NotebookTokenSessionResponse{
		BookingID: bookingID, Status: "updated", SecretName: secretName,
	})
}
