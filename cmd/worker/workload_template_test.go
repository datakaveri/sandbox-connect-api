package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPointer(value bool) *bool { return &value }

func testSandboxTemplate(workload string) *SandboxNotebookTemplate {
	values := []any{"cpu"}
	image := "registry/cpu:latest"
	if workload == "gpu" {
		values = []any{"gpu", "aic"}
		image = "registry/gpu:latest"
	}
	return &SandboxNotebookTemplate{
		APIVersion: notebookTemplateAPIVersion,
		Kind:       notebookTemplateKind,
		Metadata:   SandboxTemplateMetadata{Name: workload},
		Spec: SandboxNotebookTemplateSpec{
			Lifecycle: NotebookLifecycleConfig{
				ExternalPVCWaitTimeout: "20ms",
				WorkspaceVolumeName:    "user-data",
				VolumePolicies: []VolumeLifecyclePolicy{{Name: "user-data", Managed: &ManagedVolumePolicy{
					RetentionPolicy: retentionDeleteWithNotebook,
					Spec:            map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "{storageSize}"}}},
				}}},
				InstanceTypeOverride: InstanceTypeOverrideConfig{SelectorKey: "node.kubernetes.io/instance-type"},
				RuntimeInjection:     RuntimeInjectionConfig{Image: "alpine/git:2.45.2"},
			},
			Notebook: map[string]any{
				"apiVersion": "kubeflow.org/v1beta1", "kind": "Notebook", "metadata": map[string]any{},
				"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
					"serviceAccountName": "default-editor", "automountServiceAccountToken": false,
					"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "node.kubernetes.io/instance-type", "operator": "In", "values": values}}}}}}},
					"volumes":  []any{map[string]any{"name": "user-data", "persistentVolumeClaim": map[string]any{"claimName": "{pvcName}"}}, map[string]any{"name": "shared", "nfs": map[string]any{"server": "10.0.0.1", "path": "/data", "readOnly": true}}},
					"containers": []any{map[string]any{
						"name": "notebook", "image": image,
						"resources":       map[string]any{"requests": map[string]any{"ephemeral-storage": "1Gi"}, "limits": map[string]any{"ephemeral-storage": "10Gi"}},
						"securityContext": map[string]any{"privileged": false, "allowPrivilegeEscalation": false, "procMount": "Default"},
						"volumeMounts":    []any{map[string]any{"name": "user-data", "mountPath": "/home/jovyan"}, map[string]any{"name": "shared", "mountPath": "/mnt/shared", "readOnly": true}},
					}},
				}}},
			},
		},
	}
}

func writeTemplateYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "template.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validTemplateYAML(name string) string {
	return `apiVersion: sandbox-connect/v1alpha1
kind: SandboxNotebookTemplate
metadata:
  name: ` + name + `
spec:
  lifecycle:
    workspaceVolumeName: user-data
    volumePolicies:
      - name: user-data
        managed:
          retentionPolicy: DeleteWithNotebook
          spec:
            accessModes: [ReadWriteOnce]
            resources: {requests: {storage: "{storageSize}"}}
    runtimeInjection: {image: alpine/git:2.45.2}
  notebook:
    apiVersion: kubeflow.org/v1beta1
    kind: Notebook
    metadata: {}
    spec:
      template:
        spec:
          automountServiceAccountToken: false
          volumes:
            - name: user-data
              persistentVolumeClaim: {claimName: "{pvcName}"}
          containers:
            - name: notebook
              image: registry/notebook:latest
              securityContext: {privileged: false, allowPrivilegeEscalation: false, procMount: Default}
              volumeMounts:
                - {name: user-data, mountPath: /home/jovyan}
`
}

func TestLoadSandboxNotebookTemplateStrictValidation(t *testing.T) {
	if _, err := LoadSandboxNotebookTemplate(writeTemplateYAML(t, validTemplateYAML("cpu")), "cpu"); err != nil {
		t.Fatalf("valid template failed: %v", err)
	}
	cases := map[string]string{
		"wrong workload":  strings.Replace(validTemplateYAML("cpu"), "name: cpu", "name: gpu", 1),
		"unknown field":   strings.Replace(validTemplateYAML("cpu"), "spec:\n", "spec:\n  unexpected: true\n", 1),
		"wrong kind":      strings.Replace(validTemplateYAML("cpu"), "kind: SandboxNotebookTemplate", "kind: ConfigMap", 1),
		"missing primary": strings.Replace(validTemplateYAML("cpu"), "name: notebook", "name: other", 1),
		"unsafe":          strings.Replace(validTemplateYAML("cpu"), "privileged: false", "privileged: true", 1),
		"multiple docs":   validTemplateYAML("cpu") + "\n---\n{}\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSandboxNotebookTemplate(writeTemplateYAML(t, yaml), "cpu"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
