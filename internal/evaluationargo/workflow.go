package evaluationargo

import (
	"encoding/hex"
	"fmt"
	"strings"

	"sandbox-backend-service/internal/evaluation"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type WorkflowConfig struct {
	RunnerImage              string
	UploaderImage            string
	ServiceAccountName       string
	ScratchStorageClass      string
	ScratchStorageSize       string
	ReplacementConfigMapName string
	ProductionEnvSecretName  string
	FileServiceSecretName    string
	ActiveDeadlineSeconds    int64
	TTLSecondsAfterFinished  int64
}

func (c WorkflowConfig) Validate() error {
	for name, image := range map[string]string{"runner": c.RunnerImage, "uploader": c.UploaderImage} {
		if !isDigestPinnedImage(image) {
			return fmt.Errorf("%s image must be pinned by sha256 digest", name)
		}
	}
	if c.ServiceAccountName == "" || c.ScratchStorageClass == "" || c.ScratchStorageSize == "" {
		return fmt.Errorf("workflow service account and scratch storage settings are required")
	}
	if c.ReplacementConfigMapName == "" || c.ProductionEnvSecretName == "" || c.FileServiceSecretName == "" {
		return fmt.Errorf("replacement map, production environment, and file-service configuration are required")
	}
	if c.ActiveDeadlineSeconds <= 0 || c.TTLSecondsAfterFinished <= 0 {
		return fmt.Errorf("workflow deadline and TTL must be positive")
	}
	return nil
}

func isDigestPinnedImage(image string) bool {
	separator := strings.LastIndex(image, "@sha256:")
	if separator <= 0 {
		return false
	}
	digest := image[separator+len("@sha256:"):]
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(digest) == 64 && len(decoded) == 32
}

func WorkflowName(evaluationID string) string {
	compact := strings.ReplaceAll(evaluationID, "-", "")
	if len(compact) > 20 {
		compact = compact[:20]
	}
	return "evaluation-" + compact
}

func BuildWorkflow(record evaluation.Record, cfg WorkflowConfig) (*unstructured.Unstructured, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !evaluation.IsSafeRelativePath(record.SourceNotebookPath) {
		return nil, fmt.Errorf("source notebook path must be a safe relative path")
	}
	name := WorkflowName(record.ID)
	labels := map[string]any{
		"app.kubernetes.io/name":                 "evaluation-argo-service",
		"sandbox-connect.tgdex.io/evaluation-id": record.ID,
		"sandbox-connect.tgdex.io/notebook":      record.NotebookName,
	}
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata": map[string]any{
			"name": name, "namespace": record.Namespace, "labels": labels,
		},
		"spec": map[string]any{
			"entrypoint":            "evaluation",
			"serviceAccountName":    cfg.ServiceAccountName,
			"activeDeadlineSeconds": cfg.ActiveDeadlineSeconds,
			"ttlStrategy":           map[string]any{"secondsAfterCompletion": cfg.TTLSecondsAfterFinished},
			"securityContext": map[string]any{
				"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000),
				"fsGroup": int64(1000), "seccompProfile": map[string]any{"type": "RuntimeDefault"},
			},
			"podGC": map[string]any{"strategy": "OnWorkflowCompletion"},
			"arguments": map[string]any{"parameters": []any{
				map[string]any{"name": "evaluation-id", "value": record.ID},
				map[string]any{"name": "notebook-path", "value": record.SourceNotebookPath},
				map[string]any{"name": "output-prefix", "value": record.OutputPrefix},
				map[string]any{"name": "production-env-secret", "value": cfg.ProductionEnvSecretName},
			}},
			"volumeClaimTemplates": []any{map[string]any{
				"metadata": map[string]any{"name": "scratch", "labels": labels},
				"spec": map[string]any{
					"storageClassName": cfg.ScratchStorageClass,
					"accessModes":      []any{"ReadWriteOnce"},
					"resources":        map[string]any{"requests": map[string]any{"storage": cfg.ScratchStorageSize}},
				},
			}},
			"templates": buildWorkflowTemplates(cfg, record.SourcePVC),
		},
	}}
	return workflow, nil
}

func buildWorkflowTemplates(cfg WorkflowConfig, sourcePVC string) []any {
	steps := []any{
		map[string]any{"name": "prepare", "template": "prepare"},
		map[string]any{"name": "convert", "template": "convert"},
		map[string]any{"name": "configure", "template": "configure"},
		map[string]any{"name": "execute", "template": "execute"},
		map[string]any{"name": "upload", "template": "upload"},
	}
	return []any{
		map[string]any{
			"name": "evaluation",
			"steps": []any{
				[]any{steps[0]}, []any{steps[1]}, []any{steps[2]}, []any{steps[3]}, []any{steps[4]},
			},
			"outputs": map[string]any{"parameters": []any{map[string]any{
				"name": "manifest-json", "valueFrom": map[string]any{
					"parameter": "{{steps.upload.outputs.parameters.manifest-json}}"},
			}}},
		},
		runnerTemplate("prepare", cfg.RunnerImage, []string{
			"prepare", "--source", "/mnt/participant/{{workflow.parameters.notebook-path}}",
			"--workspace", "/workspace", "--evaluation-id", "{{workflow.parameters.evaluation-id}}",
		}, sourcePVC, "", false),
		runnerTemplate("convert", cfg.RunnerImage,
			[]string{"convert", "--workspace", "/workspace"}, "", "", false),
		runnerTemplate("configure", cfg.RunnerImage,
			[]string{"configure", "--workspace", "/workspace", "--map", "/etc/evaluation/replacements.json"},
			"", cfg.ReplacementConfigMapName, false),
		runnerTemplate("execute", cfg.RunnerImage,
			[]string{"execute", "--workspace", "/workspace", "--output", "/workspace/output"},
			"", "", true),
		uploadTemplate(cfg),
	}
}

func runnerTemplate(name, image string, args []string, participantPVC, replacementConfigMap string, productionEnv bool) map[string]any {
	mounts := []any{map[string]any{"name": "scratch", "mountPath": "/workspace"}}
	if participantPVC != "" {
		mounts = append(mounts, map[string]any{
			"name": "participant", "mountPath": "/mnt/participant", "readOnly": true})
	}
	if replacementConfigMap != "" {
		mounts = append(mounts, map[string]any{
			"name": "replacement-map", "mountPath": "/etc/evaluation", "readOnly": true})
	}
	container := map[string]any{
		"image": image, "imagePullPolicy": "IfNotPresent",
		"command": []any{"/usr/local/bin/evaluation-runner"}, "args": stringSlice(args),
		"volumeMounts": mounts, "securityContext": restrictedContainerSecurityContext(),
		"resources": workflowResources(),
		"env": []any{
			map[string]any{"name": "HOME", "value": "/workspace/home"},
			map[string]any{"name": "TMPDIR", "value": "/workspace/tmp"},
		},
	}
	if productionEnv {
		container["envFrom"] = []any{map[string]any{"secretRef": map[string]any{
			"name": "{{workflow.parameters.production-env-secret}}"}}}
	}
	template := map[string]any{
		"name": name, "container": container,
		"metadata": map[string]any{"labels": map[string]any{"evaluation-stage": name}},
	}
	if participantPVC != "" {
		template["volumes"] = []any{map[string]any{"name": "participant", "persistentVolumeClaim": map[string]any{
			"claimName": participantPVC, "readOnly": true}}}
	}
	if replacementConfigMap != "" {
		template["volumes"] = []any{map[string]any{"name": "replacement-map", "configMap": map[string]any{
			"name": replacementConfigMap, "optional": false}}}
	}
	return template
}

func uploadTemplate(cfg WorkflowConfig) map[string]any {
	return map[string]any{
		"name":     "upload",
		"metadata": map[string]any{"labels": map[string]any{"evaluation-stage": "upload"}},
		"container": map[string]any{
			"image": cfg.UploaderImage, "imagePullPolicy": "IfNotPresent",
			"command": []any{"/usr/local/bin/evaluation-uploader"},
			"args": []any{"--workspace", "/workspace", "--output-prefix", "{{workflow.parameters.output-prefix}}",
				"--manifest", "/workspace/manifest.json"},
			"env": []any{
				map[string]any{"name": "HOME", "value": "/workspace/home"},
				map[string]any{"name": "TMPDIR", "value": "/workspace/tmp"},
			},
			"envFrom":         []any{map[string]any{"secretRef": map[string]any{"name": cfg.FileServiceSecretName}}},
			"volumeMounts":    []any{map[string]any{"name": "scratch", "mountPath": "/workspace"}},
			"securityContext": restrictedContainerSecurityContext(), "resources": workflowResources(),
		},
		"outputs": map[string]any{"parameters": []any{map[string]any{
			"name": "manifest-json", "valueFrom": map[string]any{"path": "/workspace/manifest.json"},
		}}},
	}
}

func restrictedContainerSecurityContext() map[string]any {
	return map[string]any{
		"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000),
		"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
		"capabilities": map[string]any{"drop": []any{"ALL"}},
	}
}

func workflowResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "256Mi", "ephemeral-storage": "128Mi"},
		"limits":   map[string]any{"cpu": "2", "memory": "4Gi", "ephemeral-storage": "1Gi"},
	}
}

func stringSlice(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
