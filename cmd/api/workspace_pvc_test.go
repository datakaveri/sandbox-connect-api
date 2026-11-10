package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"sandbox-backend-service/pkg/k8s"

	env "github.com/caarlos0/env/v11"
	"k8s.io/apimachinery/pkg/api/resource"
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
		env:       ApiEnv{WorkspaceEnabled: enabled, WorkspaceStorageClass: "ceph-filesystem", WorkspaceStorageSize: "50Gi"},
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
	if err := validateProfileWorkspacePVC(pvc, app.env.WorkspaceStorageClass, resource.MustParse(app.env.WorkspaceStorageSize)); err != nil {
		t.Fatalf("created workspace PVC is incompatible: %v", err)
	}
	if pvc.GetLabels()["sandbox-connect.tgdex.io/profile-workspace"] != "true" {
		t.Fatalf("workspace PVC label missing: %#v", pvc.GetLabels())
	}

	if err := app.ensureProfileWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatalf("compatible existing workspace PVC should be reused: %v", err)
	}
}

func TestEnsureProfileWorkspacePVCReusesExistingEFSClaim(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": profileWorkspacePVCName, "namespace": "user-ns"},
		"spec": map[string]any{
			"storageClassName": "efs-csi-storage-class",
			"accessModes":      []any{"ReadWriteMany"},
			"volumeMode":       "Filesystem",
			"resources":        map[string]any{"requests": map[string]any{"storage": "10Gi"}},
		},
	}}
	app := workspacePVCTestApp(true, profileNamespace("user-ns"), existing)
	app.env.WorkspaceStorageClass = "efs-csi-storage-class"
	app.env.WorkspaceStorageSize = "10Gi"
	if err := app.ensureProfileWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err != nil {
		t.Fatalf("existing EFS workspace claim should be reused: %v", err)
	}
}

func TestEnsureProfileWorkspacePVCCreatesConfiguredClaim(t *testing.T) {
	app := workspacePVCTestApp(true, profileNamespace("user-ns"))
	app.env.WorkspaceStorageClass = "efs-csi-storage-class"
	app.env.WorkspaceStorageSize = "10Gi"
	ctx := context.Background()
	if err := app.ensureProfileWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatal(err)
	}
	pvc, err := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace("user-ns").Get(ctx, profileWorkspacePVCName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProfileWorkspacePVC(pvc, app.env.WorkspaceStorageClass, resource.MustParse(app.env.WorkspaceStorageSize)); err != nil {
		t.Fatalf("created EFS workspace claim is incompatible: %v", err)
	}
}

func TestEnsureProfileWorkspacePVCRejectsInvalidSize(t *testing.T) {
	app := workspacePVCTestApp(true, profileNamespace("user-ns"))
	app.env.WorkspaceStorageSize = "invalid"
	if err := app.ensureProfileWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err == nil {
		t.Fatal("expected invalid workspace size to be rejected")
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
	for _, name := range []string{"WORKSPACE_ENABLED", "API_WORKSPACE_STORAGE_CLASS", "API_WORKSPACE_STORAGE_SIZE"} {
		previousValue, hadPreviousValue := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if hadPreviousValue {
				_ = os.Setenv(name, previousValue)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	var cfg ApiEnv
	if err := env.Parse(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceEnabled {
		t.Fatal("WORKSPACE_ENABLED must default to false")
	}
	if cfg.WorkspaceStorageClass != "ceph-filesystem" || cfg.WorkspaceStorageSize != "50Gi" {
		t.Fatalf("workspace storage defaults changed: class=%q size=%q", cfg.WorkspaceStorageClass, cfg.WorkspaceStorageSize)
	}
}
