package evaluationargo

import (
	"fmt"
	"path/filepath"
	"strings"

	"sandbox-backend-service/internal/evaluation"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type CopyJobConfig struct {
	CopierImage           string
	ServiceAccountName    string
	WorkspacePVCName      string
	UserWorkspacePath     string
	WorkspaceMountPath    string
	FileServiceSecretName string
	ActiveDeadlineSeconds int64
	TTLSecondsAfterFinish int64
}

func (c CopyJobConfig) Validate() error {
	if !isDigestPinnedImage(c.CopierImage) {
		return fmt.Errorf("copier image must be pinned by sha256 digest")
	}
	if c.ServiceAccountName == "" || c.WorkspacePVCName == "" || c.UserWorkspacePath == "" ||
		c.WorkspaceMountPath == "" || c.FileServiceSecretName == "" {
		return fmt.Errorf("copy service account, workspace PVC, and file-service configuration are required")
	}
	if c.ActiveDeadlineSeconds <= 0 || c.TTLSecondsAfterFinish <= 0 {
		return fmt.Errorf("copy deadline and TTL must be positive")
	}
	return nil
}

func CopyJobName(evaluationID string, attempt int) string {
	compact := strings.ReplaceAll(evaluationID, "-", "")
	if len(compact) > 20 {
		compact = compact[:20]
	}
	return fmt.Sprintf("evaluation-copy-%s-a%d", compact, attempt)
}

func BuildCopyJob(work evaluation.ApprovalWork, cfg CopyJobConfig) (*unstructured.Unstructured, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if work.Approval.AttemptCount <= 0 {
		return nil, fmt.Errorf("approval attempt must be positive")
	}
	relativeDestination, err := filepath.Rel(filepath.Clean(cfg.UserWorkspacePath), filepath.Clean(work.Approval.Destination))
	if err != nil || relativeDestination == "." || relativeDestination == ".." ||
		strings.HasPrefix(relativeDestination, "../") || filepath.IsAbs(relativeDestination) {
		return nil, fmt.Errorf("approval destination is outside the user workspace")
	}
	containerDestination := filepath.Join(cfg.WorkspaceMountPath, relativeDestination)
	name := CopyJobName(work.Evaluation.ID, work.Approval.AttemptCount)
	labels := map[string]any{
		"app.kubernetes.io/name":                    "evaluation-argo-service",
		"app.kubernetes.io/component":               "approval-copy",
		"sandbox-connect.tgdex.io/evaluation-id":    work.Evaluation.ID,
		"sandbox-connect.tgdex.io/approval-attempt": fmt.Sprintf("%d", work.Approval.AttemptCount),
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{
			"name": name, "namespace": work.Evaluation.Namespace, "labels": labels,
		},
		"spec": map[string]any{
			"backoffLimit": int64(1), "activeDeadlineSeconds": cfg.ActiveDeadlineSeconds,
			"ttlSecondsAfterFinished": cfg.TTLSecondsAfterFinish,
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					"restartPolicy": "Never", "serviceAccountName": cfg.ServiceAccountName,
					"securityContext": map[string]any{
						"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000),
						"fsGroup": int64(1000), "seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"volumes": []any{
						map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": cfg.WorkspacePVCName}},
						map[string]any{"name": "tmp", "emptyDir": map[string]any{}},
					},
					"containers": []any{map[string]any{
						"name": "copy", "image": cfg.CopierImage, "imagePullPolicy": "IfNotPresent",
						"command": []any{"/usr/local/bin/evaluation-copier"},
						"args": []any{"--manifest-key", work.Evaluation.ManifestKey,
							"--destination", containerDestination, "--workspace-root", cfg.WorkspaceMountPath},
						"env": []any{
							map[string]any{"name": "HOME", "value": "/tmp/home"},
							map[string]any{"name": "TMPDIR", "value": "/tmp"},
						},
						"envFrom":         []any{map[string]any{"secretRef": map[string]any{"name": cfg.FileServiceSecretName}}},
						"volumeMounts":    []any{map[string]any{"name": "workspace", "mountPath": cfg.WorkspaceMountPath}, map[string]any{"name": "tmp", "mountPath": "/tmp"}},
						"securityContext": restrictedContainerSecurityContext(), "resources": workflowResources(),
					}},
				},
			},
		},
	}}, nil
}
