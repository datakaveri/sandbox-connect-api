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
		if template.Spec.Lifecycle.WorkspaceVolumeName != "user-data" || len(template.Spec.Lifecycle.VolumePolicies) != 1 {
			t.Fatalf("unexpected %s lifecycle: %#v", workload, template.Spec.Lifecycle)
		}
		podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
		if podSpec["serviceAccountName"] != "default-editor" || podSpec["automountServiceAccountToken"] != false {
			t.Fatalf("unexpected %s pod settings", workload)
		}
		volumes, _ := templateNamedItems(podSpec, "volumes")
		if len(volumes) != 5 {
			t.Fatalf("%s template has %d volumes, want 5", workload, len(volumes))
		}
	}
}
