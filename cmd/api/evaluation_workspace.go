package main

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"sandbox-backend-service/pkg/constants"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const evaluationWorkspaceLabel = "sandbox-connect.tgdex.io/evaluation-workspace"

type EvaluationWorkspaceConfig struct {
	Enabled            bool   `env:"API_EVALUATION_WORKSPACE_ENABLED" envDefault:"false"`
	PVCName            string `env:"API_EVALUATION_WORKSPACE_PVC_NAME" envDefault:"evaluation-workspace"`
	StorageClass       string `env:"API_EVALUATION_WORKSPACE_STORAGE_CLASS" envDefault:""`
	StorageProvisioner string `env:"API_EVALUATION_WORKSPACE_STORAGE_PROVISIONER" envDefault:"efs.csi.aws.com"`
	StorageSize        string `env:"API_EVALUATION_WORKSPACE_STORAGE_SIZE" envDefault:"20Gi"`
	AccessMode         string `env:"API_EVALUATION_WORKSPACE_ACCESS_MODE" envDefault:"ReadWriteMany"`
	VolumeMode         string `env:"API_EVALUATION_WORKSPACE_VOLUME_MODE" envDefault:"Filesystem"`
}

func (c EvaluationWorkspaceConfig) Validate(mountPath string) error {
	if !c.Enabled {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(c.PVCName); len(errs) > 0 {
		return fmt.Errorf("evaluation workspace PVC name is invalid: %s", strings.Join(errs, ", "))
	}
	if strings.TrimSpace(c.StorageClass) == "" {
		return fmt.Errorf("API_EVALUATION_WORKSPACE_STORAGE_CLASS is required when the evaluation workspace is enabled")
	}
	if errs := validation.IsDNS1123Subdomain(c.StorageClass); len(errs) > 0 {
		return fmt.Errorf("evaluation workspace storage class is invalid: %s", strings.Join(errs, ", "))
	}
	if strings.TrimSpace(c.StorageProvisioner) == "" {
		return fmt.Errorf("API_EVALUATION_WORKSPACE_STORAGE_PROVISIONER is required when the evaluation workspace is enabled")
	}
	if c.AccessMode != "ReadWriteMany" {
		return fmt.Errorf("evaluation common workspace requires ReadWriteMany; %s is not safe for concurrent sandboxes", c.AccessMode)
	}
	if c.VolumeMode != "Filesystem" {
		return fmt.Errorf("evaluation common workspace volume mode must be Filesystem")
	}
	size, err := resource.ParseQuantity(c.StorageSize)
	if err != nil || size.Sign() <= 0 {
		return fmt.Errorf("evaluation workspace storage size %q is invalid", c.StorageSize)
	}
	if !path.IsAbs(mountPath) || path.Clean(mountPath) != mountPath {
		return fmt.Errorf("evaluation workspace mount path must be a clean absolute path")
	}
	return nil
}

func (app *application) validateEvaluationWorkspaceStorageClass(ctx context.Context) error {
	cfg := app.env.EvaluationWorkspaceConfig
	if !cfg.Enabled {
		return nil
	}
	storageClassGVR := schema.GroupVersionResource{
		Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses",
	}
	storageClass, err := app.k8sClient.Dynamic.Resource(storageClassGVR).
		Get(ctx, cfg.StorageClass, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get evaluation workspace StorageClass %q: %w", cfg.StorageClass, err)
	}
	provisioner, _, err := unstructured.NestedString(storageClass.Object, "provisioner")
	if err != nil {
		return fmt.Errorf("read evaluation workspace StorageClass provisioner: %w", err)
	}
	if provisioner != cfg.StorageProvisioner {
		return fmt.Errorf("evaluation workspace StorageClass %q uses provisioner %q, expected %q", cfg.StorageClass, provisioner, cfg.StorageProvisioner)
	}
	return nil
}

func (app *application) ensureEvaluationWorkspacePVC(ctx context.Context, logger *slog.Logger, namespace string) error {
	cfg := app.env.EvaluationWorkspaceConfig
	if !cfg.Enabled {
		return nil
	}
	if err := cfg.Validate(app.env.EvaluationConfig.WorkspaceMountPath); err != nil {
		return err
	}
	if err := app.waitForNamespace(ctx, logger, namespace); err != nil {
		return err
	}

	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      cfg.PVCName,
			"namespace": namespace,
			"labels": map[string]any{
				evaluationWorkspaceLabel: "true",
			},
		},
		"spec": map[string]any{
			"storageClassName": cfg.StorageClass,
			"accessModes":      []any{cfg.AccessMode},
			"volumeMode":       cfg.VolumeMode,
			"resources": map[string]any{
				"requests": map[string]any{"storage": cfg.StorageSize},
			},
		},
	}}

	workspaceLogger := logger.With("operation", "ensureEvaluationWorkspacePVC", "namespace", namespace, "pvc", cfg.PVCName)
	return WithK8sRetry(ctx, workspaceLogger, func() (constants.ShouldContinue, error) {
		_, err := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace(namespace).
			Create(ctx, pvc, metav1.CreateOptions{})
		if err == nil {
			workspaceLogger.Info("evaluation workspace PVC created")
			return constants.RetryStop, nil
		}
		if !k8serrors.IsAlreadyExists(err) {
			return constants.RetryContinue, err
		}
		existing, getErr := app.k8sClient.Dynamic.Resource(profileWorkspacePVCGVR).Namespace(namespace).
			Get(ctx, cfg.PVCName, metav1.GetOptions{})
		if getErr != nil {
			return constants.RetryContinue, getErr
		}
		if validateErr := validateEvaluationWorkspacePVC(existing, cfg); validateErr != nil {
			return constants.RetryStop, validateErr
		}
		workspaceLogger.Info("compatible evaluation workspace PVC already exists")
		return constants.RetryStop, nil
	})
}

func validateEvaluationWorkspacePVC(pvc *unstructured.Unstructured, cfg EvaluationWorkspaceConfig) error {
	if pvc.GetDeletionTimestamp() != nil {
		return fmt.Errorf("evaluation workspace PVC is terminating")
	}
	if pvc.GetLabels()[evaluationWorkspaceLabel] != "true" {
		return fmt.Errorf("PVC %q exists but is not owned by the evaluation workspace feature", pvc.GetName())
	}
	storageClass, _, err := unstructured.NestedString(pvc.Object, "spec", "storageClassName")
	if err != nil || storageClass != cfg.StorageClass {
		return fmt.Errorf("evaluation workspace storageClassName is %q, expected %q", storageClass, cfg.StorageClass)
	}
	accessModes, _, err := unstructured.NestedStringSlice(pvc.Object, "spec", "accessModes")
	if err != nil || len(accessModes) != 1 || accessModes[0] != cfg.AccessMode {
		return fmt.Errorf("evaluation workspace accessModes are %v, expected [%s]", accessModes, cfg.AccessMode)
	}
	volumeMode, found, err := unstructured.NestedString(pvc.Object, "spec", "volumeMode")
	if err != nil {
		return fmt.Errorf("read evaluation workspace volumeMode: %w", err)
	}
	if !found || volumeMode == "" {
		volumeMode = "Filesystem"
	}
	if volumeMode != cfg.VolumeMode {
		return fmt.Errorf("evaluation workspace volumeMode is %q, expected %q", volumeMode, cfg.VolumeMode)
	}
	storage, _, err := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	if err != nil {
		return fmt.Errorf("read evaluation workspace storage request: %w", err)
	}
	actual, err := resource.ParseQuantity(storage)
	if err != nil {
		return fmt.Errorf("parse evaluation workspace storage request %q: %w", storage, err)
	}
	required := resource.MustParse(cfg.StorageSize)
	if actual.Cmp(required) < 0 {
		return fmt.Errorf("evaluation workspace storage is %s, expected at least %s", actual.String(), required.String())
	}
	return nil
}
