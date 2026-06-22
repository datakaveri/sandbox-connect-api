package main

import "testing"

func TestPlatformTokenSidecarEnabledOnlyForConfiguredCPU(t *testing.T) {
	bookingID := int64(42)
	w := worker{app: &application{env: Env{
		PLATFORM_TOKEN_SIDECAR_IMAGE:          "token-sidecar:latest",
		PLATFORM_FILE_API_BASE_URL:            "https://example.com/files-connect-api",
		PLATFORM_KEYCLOAK_TOKEN_URL:           "https://example.com/token",
		PLATFORM_KEYCLOAK_CLIENT_ID:           "sandbox-notebook",
		PLATFORM_SANDBOX_CONNECT_API_BASE_URL: "https://sandbox.example.com",
	}}, notebook: Notebook{BookingID: &bookingID}}
	if !w.platformTokenSidecarEnabled("cpu") {
		t.Fatal("expected sidecar to be enabled for configured CPU notebook")
	}
	if w.platformTokenSidecarEnabled("gpu") {
		t.Fatal("expected sidecar to be disabled for GPU notebook")
	}

	w.app.env.PLATFORM_KEYCLOAK_TOKEN_URL = ""
	if w.platformTokenSidecarEnabled("cpu") {
		t.Fatal("expected sidecar to be disabled without token URL")
	}
}

func TestPlatformTokenPodFragments(t *testing.T) {
	bookingID := int64(42)
	w := worker{
		app: &application{env: Env{
			PLATFORM_TOKEN_SIDECAR_IMAGE:          "token-sidecar:latest",
			PLATFORM_FILE_API_BASE_URL:            "https://example.com/files-connect-api",
			PLATFORM_KEYCLOAK_TOKEN_URL:           "https://example.com/token",
			PLATFORM_KEYCLOAK_CLIENT_ID:           "sandbox-notebook",
			PLATFORM_SANDBOX_CONNECT_API_BASE_URL: "https://sandbox.example.com",
		}},
		notebook: Notebook{Name: "demo-notebook", Namespace: "user-123", BookingID: &bookingID},
	}

	volumes := w.platformTokenVolumes()
	if len(volumes) != 2 {
		t.Fatalf("expected two platform token volumes, got %d", len(volumes))
	}
	refreshVolume := volumes[0].(map[string]any)
	secret := refreshVolume["secret"].(map[string]any)
	if refreshVolume["name"] != platformRefreshTokenVolumeName {
		t.Fatalf("unexpected refresh volume name: %#v", refreshVolume["name"])
	}
	if secret["secretName"] != "demo-notebook-plt-token" || secret["optional"] != true || secret["defaultMode"] != 0400 {
		t.Fatalf("unexpected refresh secret volume: %#v", secret)
	}
	cacheVolume := volumes[1].(map[string]any)
	emptyDir := cacheVolume["emptyDir"].(map[string]any)
	if cacheVolume["name"] != platformTokenCacheVolumeName || emptyDir["medium"] != "Memory" {
		t.Fatalf("unexpected cache volume: %#v", cacheVolume)
	}

	notebookMount := platformTokenNotebookVolumeMount()
	if notebookMount["name"] != platformTokenCacheVolumeName || notebookMount["readOnly"] != true {
		t.Fatalf("notebook should mount only token cache read-only: %#v", notebookMount)
	}

	sidecar := w.platformTokenSidecarContainer()
	if sidecar["name"] != "platform-token-sidecar" || sidecar["image"] != "token-sidecar:latest" {
		t.Fatalf("unexpected sidecar identity: %#v", sidecar)
	}
	securityContext := sidecar["securityContext"].(map[string]any)
	if securityContext["readOnlyRootFilesystem"] != true || securityContext["allowPrivilegeEscalation"] != false {
		t.Fatalf("sidecar security context is not hardened: %#v", securityContext)
	}
	envByName := map[string]any{}
	for _, item := range sidecar["env"].([]any) {
		env := item.(map[string]any)
		envByName[env["name"].(string)] = env["value"]
	}
	if envByName["TOKEN_SESSION_URL"] != "https://sandbox.example.com/v1/bookings/42/notebook-token-session" {
		t.Fatalf("unexpected token session URL: %#v", envByName["TOKEN_SESSION_URL"])
	}
	if envByName["EXPECTED_USER_ID"] != "user-123" || envByName["EXPECTED_CLIENT_ID"] != "sandbox-notebook" {
		t.Fatalf("unexpected sidecar identity env: %#v", envByName)
	}
	if envByName["KEYCLOAK_CLIENT_SECRET_FILE"] != platformClientSecretFile {
		t.Fatalf("sidecar client secret file missing: %#v", envByName)
	}
	mounts := sidecar["volumeMounts"].([]any)
	refreshMount := mounts[0].(map[string]any)
	if refreshMount["name"] != platformRefreshTokenVolumeName || refreshMount["readOnly"] != true {
		t.Fatalf("refresh token secret must be mounted read-only only in sidecar: %#v", refreshMount)
	}
	cacheMount := mounts[1].(map[string]any)
	if cacheMount["name"] != platformTokenCacheVolumeName {
		t.Fatalf("sidecar cache mount missing: %#v", cacheMount)
	}
}
