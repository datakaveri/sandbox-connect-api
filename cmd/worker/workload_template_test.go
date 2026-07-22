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
		pvcTemplates: map[string]map[string]any{"user-data": {
			"apiVersion": "v1", "kind": "PersistentVolumeClaim",
			"metadata": map[string]any{"name": "user-data", "labels": map[string]any{"example.com/notebook": "{notebookName}"}, "annotations": map[string]any{"example.com/namespace": "{namespace}"}},
			"spec":     map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "{storageSize}"}}},
		}},
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
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: user-data
  labels:
    example.com/notebook: "{notebookName}"
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: "{storageSize}"}}
`
}

func TestLoadSandboxNotebookTemplateStrictValidation(t *testing.T) {
	valid := validTemplateYAML("cpu")
	if _, err := LoadSandboxNotebookTemplate(writeTemplateYAML(t, valid), "cpu"); err != nil {
		t.Fatalf("valid template failed: %v", err)
	}
	documents := strings.SplitN(valid, "\n---\n", 2)
	cases := map[string]string{
		"wrong workload":    strings.Replace(validTemplateYAML("cpu"), "name: cpu", "name: gpu", 1),
		"unknown field":     strings.Replace(validTemplateYAML("cpu"), "spec:\n", "spec:\n  unexpected: true\n", 1),
		"wrong kind":        strings.Replace(validTemplateYAML("cpu"), "kind: SandboxNotebookTemplate", "kind: ConfigMap", 1),
		"missing primary":   strings.Replace(validTemplateYAML("cpu"), "name: notebook", "name: other", 1),
		"unsafe":            strings.Replace(validTemplateYAML("cpu"), "privileged: false", "privileged: true", 1),
		"invalid extra doc": valid + "\n---\n{}\n",
		"missing PVC doc":   documents[0],
		"duplicate PVC doc": valid + "\n---\n" + documents[1],
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSandboxNotebookTemplate(writeTemplateYAML(t, yaml), "cpu"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadSandboxNotebookTemplateAcceptsMultiplePVCDocuments(t *testing.T) {
	bundle := strings.Replace(validTemplateYAML("cpu"), "          retentionPolicy: DeleteWithNotebook\n", "          retentionPolicy: DeleteWithNotebook\n      - name: cache\n        managed:\n          retentionPolicy: Retain\n", 1)
	bundle = strings.Replace(bundle, "            - name: user-data\n              persistentVolumeClaim: {claimName: \"{pvcName}\"}\n", "            - name: user-data\n              persistentVolumeClaim: {claimName: \"{pvcName}\"}\n            - name: cache\n              persistentVolumeClaim: {claimName: \"{notebookName}-cache\"}\n", 1)
	bundle += `---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: cache
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 1Gi}}
`

	template, err := LoadSandboxNotebookTemplate(writeTemplateYAML(t, bundle), "cpu")
	if err != nil {
		t.Fatalf("multi-PVC template failed: %v", err)
	}
	if len(template.pvcTemplates) != 2 {
		t.Fatalf("got %d PVC templates, want 2", len(template.pvcTemplates))
	}
	if _, exists := template.PVCTemplate("cache"); !exists {
		t.Fatal("cache PVC template is missing")
	}
}

func TestValidateManagedPVCTemplateRejectsInvalidObjects(t *testing.T) {
	valid := testSandboxTemplate("cpu").pvcTemplates["user-data"]
	cases := map[string]func(map[string]any){
		"wrong kind": func(template map[string]any) {
			template["kind"] = "ConfigMap"
		},
		"mismatched name": func(template map[string]any) {
			metadata := template["metadata"].(map[string]any)
			metadata["name"] = "other-volume"
		},
		"status": func(template map[string]any) {
			template["status"] = map[string]any{"phase": "Bound"}
		},
		"missing spec": func(template map[string]any) {
			delete(template, "spec")
		},
		"non-string label": func(template map[string]any) {
			metadata := template["metadata"].(map[string]any)
			metadata["labels"] = map[string]any{"example.com/invalid": true}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := runtimeJSONDeepCopy(valid)
			mutate(candidate)
			if err := validateManagedPVCTemplate(candidate, "user-data"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
