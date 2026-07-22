package main

import (
	"fmt"
	"strings"
)

const (
	platformTokenSecretNameSuffix           = "-plt-token"
	platformRefreshTokenVolumeName          = "platform-refresh-token"
	platformTokenCacheVolumeName            = "platform-token-cache"
	platformRefreshTokenMountPath           = "/var/run/sandbox-connect/refresh"
	platformTokenCacheMountPath             = "/var/run/sandbox-connect/platform"
	platformRefreshTokenFile                = "/var/run/sandbox-connect/refresh/refresh_token"
	platformClientSecretFile                = "/var/run/sandbox-connect/refresh/client_secret"
	platformAccessTokenFile                 = "/var/run/sandbox-connect/platform/token"
	platformTokenNotebookLabel              = "sandbox-connect/notebook-name"
	defaultPlatformRefreshSkewSeconds       = "60"
	defaultPlatformCheckIntervalSecs        = "30"
	defaultPlatformSecretWaitIntervalSecs   = "1"
	defaultPlatformRefreshRetryIntervalSecs = "5"
)

func platformTokenSecretName(notebookName string) string {
	return strings.TrimSpace(notebookName) + platformTokenSecretNameSuffix
}

func (w *worker) platformTokenSidecarEnabled() bool {
	return w.template != nil && w.template.Spec.Lifecycle.PlatformToken != nil
}

func (w *worker) platformTokenSessionURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(w.template.Spec.Lifecycle.PlatformToken.SessionAPIBaseURL), "/")
	if w.notebook.BookingID != nil && *w.notebook.BookingID > 0 {
		return fmt.Sprintf("%s/v1/bookings/%d/notebook-token-session", baseURL, *w.notebook.BookingID)
	}
	return fmt.Sprintf("%s/v1/notebook/%s/notebook-token-session", baseURL, w.notebook.Name)
}

func (w *worker) patchPlatformTokenResources(containers, volumes []map[string]any) error {
	if !w.platformTokenSidecarEnabled() {
		return nil
	}
	refreshVolume, ok := findNamedItem(volumes, platformRefreshTokenVolumeName)
	if !ok {
		return fmt.Errorf("platform token refresh volume is missing")
	}
	secret, ok := refreshVolume["secret"].(map[string]any)
	if !ok {
		return fmt.Errorf("platform token refresh volume must be a Secret volume")
	}
	secret["secretName"] = platformTokenSecretName(w.notebook.Name)
	refreshVolume["secret"] = secret

	sidecar, ok := findNamedItem(containers, platformTokenSidecarName)
	if !ok {
		return fmt.Errorf("platform token sidecar is missing")
	}
	if err := setContainerEnvValue(sidecar, "TOKEN_SESSION_URL", w.platformTokenSessionURL()); err != nil {
		return err
	}
	return setContainerEnvValue(sidecar, "EXPECTED_USER_ID", w.notebook.Namespace)
}

func setContainerEnvValue(container map[string]any, name, value string) error {
	env, err := nestedSliceOrEmpty(container, "env")
	if err != nil {
		return err
	}
	for _, item := range env {
		entry, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("container %s has an invalid env entry", container["name"])
		}
		if entry["name"] == name {
			entry["value"] = value
			delete(entry, "valueFrom")
			container["env"] = env
			return nil
		}
	}
	return fmt.Errorf("container %s is missing env %s", container["name"], name)
}
