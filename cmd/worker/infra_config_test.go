package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestCheckedInWorkerNotebookTemplates(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "infra", "worker", "configmap.yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "runtime-config.yaml") || strings.Contains(string(raw), "sandbox-worker-config") {
		t.Fatal("obsolete runtime or environment ConfigMap remains")
	}
	manifest, err := os.Open(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	decoder := k8syaml.NewYAMLOrJSONDecoder(manifest, 4096)
	data := map[string]string{}
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		metadata, _, _ := unstructured.NestedStringMap(object, "metadata")
		if metadata["name"] == "sandbox-worker-notebook-templates" {
			data, _, _ = unstructured.NestedStringMap(object, "data")
		}
	}
	for _, workload := range []string{"cpu", "gpu"} {
		key := workload + "-notebook-template.yaml"
		body := data[key]
		if body == "" {
			t.Fatalf("missing %s", key)
		}
		path := writeTemplateYAML(t, body)
		template, err := LoadSandboxNotebookTemplate(path, workload)
		if err != nil {
			t.Fatalf("load checked-in %s template: %v", workload, err)
		}
		if template.Spec.Lifecycle.WorkspaceVolumeName != "user-data" || len(template.Spec.Lifecycle.VolumePolicies) != 2 {
			t.Fatalf("unexpected %s lifecycle: %#v", workload, template.Spec.Lifecycle)
		}
		if pvcTemplate, exists := template.PVCTemplate("user-data"); !exists || pvcTemplate["kind"] != "PersistentVolumeClaim" {
			t.Fatalf("%s template is missing the user-data PVC document", workload)
		}
		podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
		if podSpec["serviceAccountName"] != "default-editor" || podSpec["automountServiceAccountToken"] != false {
			t.Fatalf("unexpected %s pod settings", workload)
		}
		volumes, _ := templateNamedItems(podSpec, "volumes")
		if len(volumes) != 6 {
			t.Fatalf("%s template has %d volumes, want 6", workload, len(volumes))
		}
		workspaceVol, ok := findNamedItem(volumes, sharedWorkspaceVolumeName)
		if !ok {
			t.Fatalf("%s template is missing the shared workspace volume", workload)
		}
		claimName, _, _ := unstructured.NestedString(workspaceVol, "persistentVolumeClaim", "claimName")
		volumeReadOnly, _, _ := unstructured.NestedBool(workspaceVol, "persistentVolumeClaim", "readOnly")
		if claimName != "workspace" || !volumeReadOnly {
			t.Fatalf("%s shared workspace PVC volume is wrong: %#v", workload, workspaceVol)
		}
		inits, _ := templateNamedItems(podSpec, "initContainers")
		if _, ok := findNamedItem(inits, "ensure-artifacts-dir"); ok {
			t.Fatalf("%s template still contains the obsolete NFS init container", workload)
		}
		containers, _ := templateNamedItems(podSpec, "containers")
		primary, _ := findNamedItem(containers, "notebook")
		mounts, _ := containerVolumeMounts(primary)
		workspaceMount, ok := findMountByName(mounts, sharedWorkspaceVolumeName)
		if !ok || workspaceMount["readOnly"] != true || workspaceMount["mountPath"] != "/home/jovyan/workspace" {
			t.Fatalf("%s notebook must mount the profile workspace read-only: %#v", workload, workspaceMount)
		}
		if _, hasSubPath := workspaceMount["subPath"]; hasSubPath {
			t.Fatalf("%s shared workspace mount must use the PVC root: %#v", workload, workspaceMount)
		}
		sidecar, ok := findNamedItem(containers, platformTokenSidecarName)
		if !ok {
			t.Fatalf("%s template is missing the platform token sidecar", workload)
		}
		requests, _, _ := unstructured.NestedStringMap(sidecar, "resources", "requests")
		limits, _, _ := unstructured.NestedStringMap(sidecar, "resources", "limits")
		if requests["cpu"] == "" || requests["memory"] == "" || limits["cpu"] == "" || limits["memory"] == "" {
			t.Fatalf("%s platform token sidecar must define CPU and memory requests and limits: %#v", workload, sidecar["resources"])
		}
	}
}
