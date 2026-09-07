package main

import (
	"context"
	"log/slog"
	"testing"

	"sandbox-backend-service/pkg/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func validEvaluationWorkspaceConfig() EvaluationWorkspaceConfig {
	return EvaluationWorkspaceConfig{
		Enabled: true, PVCName: "evaluation-workspace", StorageClass: "efs-evaluation-rwx",
		StorageProvisioner: "efs.csi.aws.com", StorageSize: "20Gi",
		AccessMode: "ReadWriteMany", VolumeMode: "Filesystem",
	}
}

func evaluationWorkspaceTestApp(config EvaluationWorkspaceConfig, objects ...runtime.Object) *application {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	return &application{
		env: ApiEnv{
			EvaluationConfig:          EvaluationConfig{WorkspaceMountPath: "/home/jovyan/evaluation-workspace"},
			EvaluationWorkspaceConfig: config,
		},
		k8sClient: &k8s.K8sClient{Dynamic: client},
	}
}

func TestEnsureEvaluationWorkspacePVCDisabled(t *testing.T) {
	app := evaluationWorkspaceTestApp(EvaluationWorkspaceConfig{})
	if err := app.ensureEvaluationWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationWorkspaceConfigurationRejectsRWOAndMissingClass(t *testing.T) {
	cfg := validEvaluationWorkspaceConfig()
	cfg.AccessMode = "ReadWriteOnce"
	if err := cfg.Validate("/home/jovyan/evaluation-workspace"); err == nil {
		t.Fatal("expected ReadWriteOnce to be rejected for a common workspace")
	}
	cfg = validEvaluationWorkspaceConfig()
	cfg.StorageClass = ""
	if err := cfg.Validate("/home/jovyan/evaluation-workspace"); err == nil {
		t.Fatal("expected an enabled workspace without a storage class to be rejected")
	}
}

func TestValidateEvaluationWorkspaceStorageClassProvisioner(t *testing.T) {
	cfg := validEvaluationWorkspaceConfig()
	storageClass := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass",
		"metadata":    map[string]any{"name": cfg.StorageClass},
		"provisioner": "efs.csi.aws.com",
	}}
	app := evaluationWorkspaceTestApp(cfg, storageClass)
	if err := app.validateEvaluationWorkspaceStorageClass(context.Background()); err != nil {
		t.Fatalf("expected EFS provisioner to be accepted: %v", err)
	}

	ebsClass := storageClass.DeepCopy()
	ebsClass.Object["provisioner"] = "ebs.csi.aws.com"
	app = evaluationWorkspaceTestApp(cfg, ebsClass)
	if err := app.validateEvaluationWorkspaceStorageClass(context.Background()); err == nil {
		t.Fatal("expected the EBS provisioner to be rejected")
	}
}

func TestEnsureEvaluationWorkspacePVCCreatesAndReusesOwnedRWXClaim(t *testing.T) {
	cfg := validEvaluationWorkspaceConfig()
	app := evaluationWorkspaceTestApp(cfg, profileNamespace("user-ns"))
	ctx := context.Background()
	if err := app.ensureEvaluationWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatal(err)
	}
	pvc, err := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace("user-ns").
		Get(ctx, cfg.PVCName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pvc.GetLabels()[evaluationWorkspaceLabel] != "true" {
		t.Fatalf("evaluation workspace ownership label is missing: %#v", pvc.GetLabels())
	}
	if err := validateEvaluationWorkspacePVC(pvc, cfg); err != nil {
		t.Fatalf("created claim is incompatible: %v", err)
	}
	if err := app.ensureEvaluationWorkspacePVC(ctx, slog.Default(), "user-ns"); err != nil {
		t.Fatalf("owned compatible claim should be reused: %v", err)
	}
}

func TestEnsureEvaluationWorkspacePVCRefusesToAdoptUnownedClaim(t *testing.T) {
	cfg := validEvaluationWorkspaceConfig()
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": cfg.PVCName, "namespace": "user-ns"},
		"spec": map[string]any{
			"storageClassName": cfg.StorageClass,
			"accessModes":      []any{cfg.AccessMode},
			"volumeMode":       cfg.VolumeMode,
			"resources":        map[string]any{"requests": map[string]any{"storage": cfg.StorageSize}},
		},
	}}
	app := evaluationWorkspaceTestApp(cfg, profileNamespace("user-ns"), existing)
	if err := app.ensureEvaluationWorkspacePVC(context.Background(), slog.Default(), "user-ns"); err == nil {
		t.Fatal("expected an unowned existing claim to be rejected")
	}
}
