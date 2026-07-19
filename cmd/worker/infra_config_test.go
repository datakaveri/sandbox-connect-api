package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestCheckedInWorkerRuntimeConfig(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "infra", "worker", "configmap.yaml")
	manifest, err := os.Open(manifestPath)
	if err != nil {
		t.Fatalf("open worker configmap: %v", err)
	}
	defer manifest.Close()

	decoder := k8syaml.NewYAMLOrJSONDecoder(manifest, 4096)
	var runtimeYAML string
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode worker configmap manifest: %v", err)
		}
		if object == nil {
			continue
		}
		metadata, _, _ := unstructured.NestedStringMap(object, "metadata")
		if metadata["name"] != "sandbox-worker-runtime-config" {
			continue
		}
		runtimeYAML, _, err = unstructured.NestedString(object, "data", "runtime-config.yaml")
		if err != nil {
			t.Fatalf("read embedded runtime config: %v", err)
		}
	}
	if runtimeYAML == "" {
		t.Fatal("sandbox-worker-runtime-config does not contain runtime-config.yaml")
	}

	configPath := filepath.Join(t.TempDir(), "runtime-config.yaml")
	if err := os.WriteFile(configPath, []byte(runtimeYAML), 0o600); err != nil {
		t.Fatalf("write extracted runtime config: %v", err)
	}
	cfg, err := LoadRuntimeConfig(Env{RuntimeConfigPath: configPath})
	if err != nil {
		t.Fatalf("load checked-in runtime config: %v", err)
	}
	for _, workload := range []string{"cpu", "gpu"} {
		policy, ok := cfg.Workloads[workload]
		if !ok {
			t.Errorf("checked-in runtime config is missing %s policy", workload)
			continue
		}
		if len(policy.PVCMounts) != 2 {
			t.Errorf("checked-in %s policy has %d PVC mounts, want 2", workload, len(policy.PVCMounts))
		}
		groups := policy.SecurityContext.SupplementalGroups
		if len(groups) != 1 || groups[0] != 4001 {
			t.Errorf("checked-in %s policy supplementalGroups = %#v, want [4001]", workload, groups)
		}
	}
}
