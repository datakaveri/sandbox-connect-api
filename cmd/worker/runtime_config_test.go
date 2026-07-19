package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRuntimeConfigTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	return path
}

func TestLoadRuntimeConfigAllowsProjectSpecificMountSets(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
defaults:
  externalPVCWaitTimeout: 5s
workloads:
  cpu:
    scheduling:
      nodeSelector:
        workload: cpu
    securityContext:
      runAsUser: 1000
      runAsGroup: 100
      runAsNonRoot: true
      fsGroup: 100
      supplementalGroups:
        - 4001
    pvcMounts:
      - name: datasets
        mountPath: /mnt/datasets
        required: false
        readOnly: true
        source:
          type: existing
          claimNameTemplate: datasets-{namespace}
`)

	cfg, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path})
	if err != nil {
		t.Fatalf("LoadRuntimeConfig returned error: %v", err)
	}
	if len(cfg.Workloads) != 1 || len(cfg.Workloads["cpu"].PVCMounts) != 1 {
		t.Fatalf("unexpected workload config: %#v", cfg.Workloads)
	}
	if cfg.Workloads["cpu"].PVCMounts[0].IsRequired() {
		t.Fatal("expected datasets mount to be optional")
	}
	securityContext := cfg.Workloads["cpu"].SecurityContext
	if securityContext.RunAsUser == nil || *securityContext.RunAsUser != 1000 {
		t.Fatalf("unexpected runAsUser: %#v", securityContext.RunAsUser)
	}
	if len(securityContext.SupplementalGroups) != 1 || securityContext.SupplementalGroups[0] != 4001 {
		t.Fatalf("unexpected supplemental groups: %#v", securityContext.SupplementalGroups)
	}
}

func TestLoadRuntimeConfigAllowsNoPVCMounts(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
workloads:
  cpu:
    scheduling: {}
    pvcMounts: []
`)
	if _, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path}); err != nil {
		t.Fatalf("zero PVC mounts should be valid: %v", err)
	}
}

func TestLoadRuntimeConfigRejectsUnknownFields(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
workloads:
  cpu:
    scheduling:
      nodeSeelctor: {}
`)
	_, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadRuntimeConfigRejectsNegativeSupplementalGroup(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
workloads:
  cpu:
    securityContext:
      supplementalGroups:
        - -1
`)
	_, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "supplementalGroups[0] must be non-negative") {
		t.Fatalf("expected supplemental group validation error, got %v", err)
	}
}

func TestLoadRuntimeConfigRejectsMultipleWorkspaces(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
workloads:
  cpu:
    pvcMounts:
      - name: first
        mountPath: /first
        workspace: true
        source:
          type: existing
          claimNameTemplate: first
      - name: second
        mountPath: /second
        workspace: true
        source:
          type: existing
          claimNameTemplate: second
`)
	_, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "more than one workspace") {
		t.Fatalf("expected workspace validation error, got %v", err)
	}
}

func TestLoadRuntimeConfigRejectsReadOnlyWorkspace(t *testing.T) {
	path := writeRuntimeConfigTestFile(t, `
apiVersion: sandbox-connect/v1alpha1
workloads:
  cpu:
    pvcMounts:
      - name: workspace
        mountPath: /workspace
        workspace: true
        readOnly: true
        source:
          type: existing
          claimNameTemplate: workspace
`)
	_, err := LoadRuntimeConfig(Env{RuntimeConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "workspace mount must be writable") {
		t.Fatalf("expected read-only workspace validation error, got %v", err)
	}
}

func TestLegacyRuntimeConfigPreservesCurrentPolicy(t *testing.T) {
	cfg, err := LoadRuntimeConfig(Env{
		CPU_NODE_INSTANCE_TYPES: "t3a.large,t3a.2xlarge",
		GPU_NODE_INSTANCE_TYPES: "g4dn.xlarge,p4d.24xlarge",
		STORAGE_CLASS_NAME:      "ebs-csi-storage-class",
	})
	if err != nil {
		t.Fatalf("legacy config returned error: %v", err)
	}
	if cfg.source != "legacy-environment" {
		t.Fatalf("unexpected source: %q", cfg.source)
	}
	for _, workload := range []string{"cpu", "gpu"} {
		policy, ok := cfg.Workloads[workload]
		if !ok || len(policy.PVCMounts) != 1 || !policy.PVCMounts[0].Workspace {
			t.Fatalf("legacy %s policy missing workspace mount: %#v", workload, policy)
		}
	}
}

func TestRenderTemplateUsesOnlyExplicitValues(t *testing.T) {
	got := renderTemplate("{namespace}/{notebookName}/{pvcName}/{storageSize}", templateValues{
		Namespace: "user", NotebookName: "nb", PVCName: "nb-pvc", StorageSize: "10Gi",
	})
	if got != "user/nb/nb-pvc/10Gi" {
		t.Fatalf("unexpected rendered template: %q", got)
	}
}
