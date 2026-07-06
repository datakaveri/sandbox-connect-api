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
	defaultPlatformRefreshSkewSeconds       = "60"
	defaultPlatformCheckIntervalSecs        = "30"
	defaultPlatformSecretWaitIntervalSecs   = "1"
	defaultPlatformRefreshRetryIntervalSecs = "5"
)

func platformTokenSecretName(notebookName string) string {
	return strings.TrimSpace(notebookName) + platformTokenSecretNameSuffix
}

func (w *worker) platformTokenSidecarEnabled() bool {
	return strings.TrimSpace(w.app.env.PLATFORM_TOKEN_SIDECAR_IMAGE) != "" &&
		strings.TrimSpace(w.app.env.PLATFORM_FILE_API_BASE_URL) != "" &&
		strings.TrimSpace(w.app.env.PLATFORM_KEYCLOAK_TOKEN_URL) != "" &&
		strings.TrimSpace(w.app.env.PLATFORM_KEYCLOAK_CLIENT_ID) != "" &&
		strings.TrimSpace(w.app.env.PLATFORM_SANDBOX_CONNECT_API_BASE_URL) != ""
}

func (w *worker) platformTokenVolumes() []any {
	return []any{
		map[string]any{
			"name": platformRefreshTokenVolumeName,
			"secret": map[string]any{
				"secretName":  platformTokenSecretName(w.notebook.Name),
				"optional":    true,
				"defaultMode": 0400,
			},
		},
		map[string]any{
			"name": platformTokenCacheVolumeName,
			"emptyDir": map[string]any{
				"medium": "Memory",
			},
		},
	}
}

func platformTokenNotebookVolumeMount() map[string]any {
	return map[string]any{
		"name":      platformTokenCacheVolumeName,
		"mountPath": platformTokenCacheMountPath,
		"readOnly":  true,
	}
}

func (w *worker) platformTokenNotebookEnv() []any {
	return []any{
		map[string]any{"name": "MAHAAGX_TOKEN_FILE", "value": platformAccessTokenFile},
		map[string]any{"name": "MAHAAGX_FILE_API_BASE_URL", "value": strings.TrimSpace(w.app.env.PLATFORM_FILE_API_BASE_URL)},
	}
}

func (w *worker) platformTokenSessionURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(w.app.env.PLATFORM_SANDBOX_CONNECT_API_BASE_URL), "/")
	if w.notebook.BookingID != nil && *w.notebook.BookingID > 0 {
		return fmt.Sprintf("%s/v1/bookings/%d/notebook-token-session", baseURL, *w.notebook.BookingID)
	}
	return fmt.Sprintf("%s/v1/notebook/%s/notebook-token-session", baseURL, w.notebook.Name)
}

func (w *worker) platformTokenSidecarContainer() map[string]any {
	return map[string]any{
		"name":  "platform-token-sidecar",
		"image": strings.TrimSpace(w.app.env.PLATFORM_TOKEN_SIDECAR_IMAGE),
		"env": []any{
			map[string]any{"name": "KEYCLOAK_TOKEN_URL", "value": strings.TrimSpace(w.app.env.PLATFORM_KEYCLOAK_TOKEN_URL)},
			map[string]any{"name": "KEYCLOAK_CLIENT_ID", "value": strings.TrimSpace(w.app.env.PLATFORM_KEYCLOAK_CLIENT_ID)},
			map[string]any{"name": "KEYCLOAK_CLIENT_SECRET_FILE", "value": platformClientSecretFile},
			map[string]any{"name": "REFRESH_TOKEN_FILE", "value": platformRefreshTokenFile},
			map[string]any{"name": "ACCESS_TOKEN_FILE", "value": platformAccessTokenFile},
			map[string]any{"name": "TOKEN_SESSION_URL", "value": w.platformTokenSessionURL()},
			map[string]any{"name": "EXPECTED_USER_ID", "value": w.notebook.Namespace},
			map[string]any{"name": "EXPECTED_CLIENT_ID", "value": strings.TrimSpace(w.app.env.PLATFORM_KEYCLOAK_CLIENT_ID)},
			map[string]any{"name": "REFRESH_SKEW_SECONDS", "value": defaultPlatformRefreshSkewSeconds},
			map[string]any{"name": "CHECK_INTERVAL_SECONDS", "value": defaultPlatformCheckIntervalSecs},
			map[string]any{"name": "SECRET_WAIT_INTERVAL_SECONDS", "value": defaultPlatformSecretWaitIntervalSecs},
			map[string]any{"name": "REFRESH_RETRY_INTERVAL_SECONDS", "value": defaultPlatformRefreshRetryIntervalSecs},
		},
		"securityContext": map[string]any{
			"privileged":               false,
			"procMount":                "Default",
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   true,
			"capabilities": map[string]any{
				"drop": []string{"ALL"},
			},
		},
		"volumeMounts": []any{
			map[string]any{
				"name":      platformRefreshTokenVolumeName,
				"mountPath": platformRefreshTokenMountPath,
				"readOnly":  true,
			},
			map[string]any{
				"name":      platformTokenCacheVolumeName,
				"mountPath": platformTokenCacheMountPath,
			},
		},
	}
}
