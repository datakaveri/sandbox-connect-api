package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"sandbox-backend-service/pkg/k8s"

	env "github.com/caarlos0/env/v11"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func profileNamespace(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": name},
	}}
}

func workspacePVCTestApp(enabled bool, objects ...runtime.Object) *application {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	return &application{
		env:       ApiEnv{WorkspaceEnabled: enabled},
		k8sClient: &k8s.K8sClient{Dynamic: client},
	}
}

func TestEnsureProfileWorkspacePVCDisabled(t *testing.T) {
	app := workspacePVCTestApp(false)
	if err := app.ensureProfileWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureProfileWorkspacePVCCreatesAndReusesCompatibleClaim(t *testing.T) {
	app := workspacePVCTestApp(true, profileNamespace("user-ns"))
	ctx := context.Background()
	if err := app.ensureProfileWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatal(err)
	}
	pvc, err := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace("user-ns").Get(ctx, profileWorkspacePVCName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("workspace PVC was not created: %v", err)
	}
	if err := validateProfileWorkspacePVC(pvc); err != nil {
		t.Fatalf("created workspace PVC is incompatible: %v", err)
	}
	if pvc.GetLabels()["sandbox-connect.tgdex.io/profile-workspace"] != "true" {
		t.Fatalf("workspace PVC label missing: %#v", pvc.GetLabels())
	}

	if err := app.ensureProfileWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatalf("compatible existing workspace PVC should be reused: %v", err)
	}
}

func TestEnsureProfileWorkspacePVCRejectsIncompatibleClaim(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": profileWorkspacePVCName, "namespace": "user-ns"},
		"spec": map[string]any{
			"storageClassName": "ceph-block",
			"accessModes":      []any{"ReadWriteOnce"},
			"volumeMode":       "Filesystem",
			"resources":        map[string]any{"requests": map[string]any{"storage": "10Gi"}},
		},
	}}
	app := workspacePVCTestApp(true, profileNamespace("user-ns"), existing)
	if err := app.ensureProfileWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err == nil {
		t.Fatal("expected incompatible workspace PVC to be rejected")
	}
}

func TestWorkspaceFlagDefaultsDisabled(t *testing.T) {
	required := map[string]string{
		"API_ADDRESS": "127.0.0.1:0", "API_POSTGRES_URL": "postgres://example",
		"API_KEYCLOAK_URL": "https://idp.example.test/auth", "API_KEYCLOAK_REALM": "sandbox",
		"API_KEYCLOAK_CLIENT_ID": "sandbox", "API_KEYCLOAK_PUBLIC_KEY": "key",
		"API_KYC_ENABLED": "false", "API_VERSION": "test",
		"API_DEFAULT_CPU_STORAGE_SIZE": "10Gi", "API_DEFAULT_GPU_STORAGE_SIZE": "50Gi",
		"API_DEFAULT_CPU_REQUEST": "1", "API_DEFAULT_CPU_LIMIT": "1",
		"API_DEFAULT_MEMORY_REQUEST": "1Gi", "API_DEFAULT_MEMORY_LIMIT": "1Gi",
		"API_DEFAULT_GPU_TYPE": "nvidia.com/gpu", "API_DEFAULT_GPU_REQUEST": "1",
		"API_DEFAULT_GPU_LIMIT": "1", "API_DEFAULT_GPU_MEMORY_REQUEST": "1Gi",
		"API_DEFAULT_GPU_MEMORY_LIMIT": "1Gi", "API_DEFAULT_GPU_CPU_REQUEST": "1",
		"API_DEFAULT_GPU_CPU_LIMIT": "1", "API_GPU_NODE_INSTANCE_TYPES": "gpu",
		"API_KUBEFLOW_URL": "https://kubeflow.example.test",
	}
	for key, value := range required {
		t.Setenv(key, value)
	}
	previousValue, hadPreviousValue := os.LookupEnv("WORKSPACE_ENABLED")
	if err := os.Unsetenv("WORKSPACE_ENABLED"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPreviousValue {
			_ = os.Setenv("WORKSPACE_ENABLED", previousValue)
		} else {
			_ = os.Unsetenv("WORKSPACE_ENABLED")
		}
	})
	var cfg ApiEnv
	if err := env.Parse(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceEnabled {
		t.Fatal("WORKSPACE_ENABLED must default to false")
	}
}
