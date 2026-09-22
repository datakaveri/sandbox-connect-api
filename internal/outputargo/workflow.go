package outputargo

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"sandbox-backend-service/internal/output"
	"sandbox-backend-service/internal/outputruntime"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type WorkflowConfig struct {
	Limits                         output.Limits
	RunnerImage                    string
	RunnerMaxLogBytes              int64
	UploaderImage                  string
	ServiceAccountName             string
	ScratchStorageClass            string
	ScratchStorageSize             string
	ReplacementConfigMapName       string
	ProductionEnvSecretName        string
	FileServiceSecretName          string
	DataAccessEnabled              bool
	DataAccessBaseURL              string
	DataAccessMTLSSecretName       string
	PlatformTokenSidecarImage      string
	PlatformTokenURL               string
	PlatformTokenClientID          string
	PlatformTokenSessionAPIBaseURL string
	ActiveDeadlineSeconds          int64
	TTLSecondsAfterFinished        int64
}

func (c WorkflowConfig) Validate() error {
	if err := c.Limits.WithDefaults().Validate(); err != nil {
		return err
	}
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
	if c.RunnerMaxLogBytes < 0 || c.RunnerMaxLogBytes > outputruntime.MaxAllowedLogBytes {
		return fmt.Errorf("runner max log bytes must be between 1 and %d", outputruntime.MaxAllowedLogBytes)
	}
	if c.ActiveDeadlineSeconds <= 0 || c.TTLSecondsAfterFinished <= 0 {
		return fmt.Errorf("workflow deadline and TTL must be positive")
	}
	if c.DataAccessEnabled {
		if !isDigestPinnedImage(c.PlatformTokenSidecarImage) {
			return fmt.Errorf("platform token sidecar image must be pinned by sha256 digest")
		}
		if c.DataAccessBaseURL == "" || c.DataAccessMTLSSecretName == "" || c.PlatformTokenClientID == "" || c.PlatformTokenSessionAPIBaseURL == "" || c.PlatformTokenURL == "" {
			return fmt.Errorf("data access URL, mTLS secret, platform token URL, client ID, and session API URL are required")
		}
		for _, endpoint := range []string{c.DataAccessBaseURL, c.PlatformTokenURL, c.PlatformTokenSessionAPIBaseURL} {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
				return fmt.Errorf("data access and platform token endpoints must be HTTPS URLs")
			}
		}
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

func WorkflowName(outputID string) string {
	compact := strings.ReplaceAll(outputID, "-", "")
	if len(compact) > 20 {
		compact = compact[:20]
	}
	return "output-" + compact
}

func BuildWorkflow(record output.Record, cfg WorkflowConfig) (*unstructured.Unstructured, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !output.IsSafeRelativePath(record.SourceNotebookPath) {
		return nil, fmt.Errorf("source notebook path must be a safe relative path")
	}
	if !output.IsSafeRelativePrefix(record.ReviewPrefix) {
		return nil, fmt.Errorf("review prefix must be a safe relative prefix")
	}
	if cfg.DataAccessEnabled && (record.NotebookName == "" || record.UserID == "" || len(record.NotebookName) > 240) {
		return nil, fmt.Errorf("data access requires a valid notebook name and owner")
	}
	name := WorkflowName(record.ID)
	labels := map[string]any{
		"app.kubernetes.io/name":             "output-argo-service",
		"sandbox-connect.tgdex.io/output-id": record.ID,
		"sandbox-connect.tgdex.io/notebook":  record.NotebookName,
	}
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata": map[string]any{
			"name": name, "namespace": record.Namespace, "labels": labels,
		},
		"spec": map[string]any{
			"entrypoint":                   "output",
			"serviceAccountName":           cfg.ServiceAccountName,
			"automountServiceAccountToken": false,
			"executor":                     map[string]any{"serviceAccountName": cfg.ServiceAccountName},
			"activeDeadlineSeconds":        cfg.ActiveDeadlineSeconds,
			"ttlStrategy":                  map[string]any{"secondsAfterCompletion": cfg.TTLSecondsAfterFinished},
			"securityContext": map[string]any{
				"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000),
				"fsGroup": int64(1000), "seccompProfile": map[string]any{"type": "RuntimeDefault"},
			},
			"podGC": map[string]any{"strategy": "OnWorkflowCompletion"},
			"arguments": map[string]any{"parameters": []any{
				map[string]any{"name": "output-id", "value": record.ID},
				map[string]any{"name": "notebook-path", "value": record.SourceNotebookPath},
				map[string]any{"name": "review-prefix", "value": record.ReviewPrefix},
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
			"templates": buildWorkflowTemplates(cfg, record),
		},
	}}
	return workflow, nil
}

func buildWorkflowTemplates(cfg WorkflowConfig, record output.Record) []any {
	steps := []any{
		map[string]any{"name": "prepare", "template": "prepare"},
		map[string]any{"name": "convert", "template": "convert"},
		map[string]any{"name": "configure", "template": "configure"},
		map[string]any{"name": "execute", "template": "execute"},
		map[string]any{"name": "upload", "template": "upload"},
	}
	execute := runnerTemplate("execute", cfg.RunnerImage,
		runnerStageArgs(cfg, "execute", "--workspace", "/workspace", "--output", "/workspace/output"),
		"", "", true)
	if cfg.DataAccessEnabled {
		configureDataAccessExecute(execute, cfg, record)
	}
	return []any{
		map[string]any{
			"name": "output",
			"steps": []any{
				[]any{steps[0]}, []any{steps[1]}, []any{steps[2]}, []any{steps[3]}, []any{steps[4]},
			},
			"outputs": map[string]any{"parameters": []any{map[string]any{
				"name": "manifest-json", "valueFrom": map[string]any{
					"parameter": "{{steps.upload.outputs.parameters.manifest-json}}"},
			}}},
		},
		runnerTemplate("prepare", cfg.RunnerImage, runnerStageArgs(cfg,
			"prepare", "--source", "/mnt/participant/{{workflow.parameters.notebook-path}}",
			"--workspace", "/workspace", "--output-id", "{{workflow.parameters.output-id}}"), record.SourcePVC, "", false),
		runnerTemplate("convert", cfg.RunnerImage,
			runnerStageArgs(cfg, "convert", "--workspace", "/workspace"), "", "", false),
		runnerTemplate("configure", cfg.RunnerImage,
			runnerStageArgs(cfg, "configure", "--workspace", "/workspace", "--map", "/etc/output/replacements.json"),
			"", cfg.ReplacementConfigMapName, false),
		execute,
		uploadTemplate(cfg),
	}
}

func configureDataAccessExecute(template map[string]any, cfg WorkflowConfig, record output.Record) {
	container := template["container"].(map[string]any)
	container["volumeMounts"] = append(container["volumeMounts"].([]any),
		map[string]any{"name": "platform-token-cache", "mountPath": "/var/run/sandbox-connect/platform", "readOnly": true},
		map[string]any{"name": "data-access-mtls", "mountPath": "/var/run/sandbox-connect/mtls", "readOnly": true},
	)
	container["env"] = append(container["env"].([]any),
		map[string]any{"name": "NHA_TOKEN_FILE", "value": "/var/run/sandbox-connect/platform/token"},
		map[string]any{"name": "ABAC_BASE_URL", "value": cfg.DataAccessBaseURL},
		map[string]any{"name": "NHA_MTLS_CA_FILE", "value": "/var/run/sandbox-connect/mtls/ca.crt"},
		map[string]any{"name": "NHA_MTLS_CLIENT_CERT_FILE", "value": "/var/run/sandbox-connect/mtls/client.crt"},
		map[string]any{"name": "NHA_MTLS_CLIENT_KEY_FILE", "value": "/var/run/sandbox-connect/mtls/client.key"},
	)
	template["volumes"] = []any{
		map[string]any{"name": "platform-refresh-token", "secret": map[string]any{"secretName": record.NotebookName + "-plt-token"}},
		map[string]any{"name": "platform-token-cache", "emptyDir": map[string]any{"medium": "Memory"}},
		map[string]any{"name": "data-access-mtls", "secret": map[string]any{"secretName": cfg.DataAccessMTLSSecretName}},
	}
	template["sidecars"] = []any{map[string]any{
		"name": "platform-token-sidecar", "image": cfg.PlatformTokenSidecarImage,
		"command":         []any{"/app/platform-token-sidecar"},
		"imagePullPolicy": "IfNotPresent",
		"env": []any{
			map[string]any{"name": "KEYCLOAK_TOKEN_URL", "value": cfg.PlatformTokenURL},
			map[string]any{"name": "KEYCLOAK_CLIENT_ID", "value": cfg.PlatformTokenClientID},
			map[string]any{"name": "KEYCLOAK_CLIENT_SECRET_FILE", "value": "/var/run/sandbox-connect/refresh/client_secret"},
			map[string]any{"name": "REFRESH_TOKEN_FILE", "value": "/var/run/sandbox-connect/refresh/refresh_token"},
			map[string]any{"name": "BOOTSTRAP_TOKEN_FILE", "value": "/var/run/sandbox-connect/refresh/bootstrap.json"},
			map[string]any{"name": "ACCESS_TOKEN_FILE", "value": "/var/run/sandbox-connect/platform/token"},
			map[string]any{"name": "READY_ADDRESS", "value": "127.0.0.1:8081"},
			map[string]any{"name": "TOKEN_SESSION_URL", "value": strings.TrimRight(cfg.PlatformTokenSessionAPIBaseURL, "/") + "/v1/notebook/" + url.PathEscape(record.NotebookName) + "/notebook-token-session"},
			map[string]any{"name": "EXPECTED_USER_ID", "value": record.UserID},
			map[string]any{"name": "EXPECTED_CLIENT_ID", "value": cfg.PlatformTokenClientID},
		},
		"volumeMounts": []any{
			map[string]any{"name": "platform-refresh-token", "mountPath": "/var/run/sandbox-connect/refresh", "readOnly": true},
			map[string]any{"name": "platform-token-cache", "mountPath": "/var/run/sandbox-connect/platform"},
		},
		"securityContext": restrictedContainerSecurityContext(),
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "10m", "memory": "32Mi"},
			"limits":   map[string]any{"cpu": "100m", "memory": "128Mi"},
		},
	}}
}

func (c WorkflowConfig) runnerMaxLogBytes() int64 {
	if c.RunnerMaxLogBytes > 0 {
		return c.RunnerMaxLogBytes
	}
	return outputruntime.MaxLogBytes
}

func runnerStageArgs(cfg WorkflowConfig, values ...string) []string {
	return append(values, "--stream-logs", "--max-log-bytes", strconv.FormatInt(cfg.runnerMaxLogBytes(), 10))
}

func runnerTemplate(name, image string, args []string, participantPVC, replacementConfigMap string, productionEnv bool) map[string]any {
	mounts := []any{map[string]any{"name": "scratch", "mountPath": "/workspace"}}
	if participantPVC != "" {
		mounts = append(mounts, map[string]any{
			"name": "participant", "mountPath": "/mnt/participant", "readOnly": true})
	}
	if replacementConfigMap != "" {
		mounts = append(mounts, map[string]any{
			"name": "replacement-map", "mountPath": "/etc/output", "readOnly": true})
	}
	container := map[string]any{
		"image": image, "imagePullPolicy": "IfNotPresent",
		"command": []any{"/usr/local/bin/output-runner"}, "args": stringSlice(args),
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
		"metadata": map[string]any{"labels": map[string]any{"output-stage": name}},
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
	limits := cfg.Limits.WithDefaults()
	return map[string]any{
		"name":     "upload",
		"metadata": map[string]any{"labels": map[string]any{"output-stage": "upload"}},
		"container": map[string]any{
			"image": cfg.UploaderImage, "imagePullPolicy": "IfNotPresent",
			"command": []any{"/usr/local/bin/output-uploader"},
			"args": []any{"--workspace", "/workspace", "--review-prefix", "{{workflow.parameters.review-prefix}}",
				"--manifest", "/workspace/manifest.json", "--csv-only",
				"--output-id", "{{workflow.parameters.output-id}}",
				"--max-manifest-bytes", strconv.Itoa(limits.MaxManifestBytes),
				"--max-files", strconv.Itoa(limits.MaxManifestFiles),
				"--max-file-bytes", strconv.FormatInt(limits.MaxFileBytes, 10),
				"--max-output-bytes", strconv.FormatInt(limits.MaxOutputBytes, 10)},
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
