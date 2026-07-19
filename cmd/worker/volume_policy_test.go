package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"sandbox-backend-service/pkg/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func boolPointer(value bool) *bool { return &value }

func newVolumePolicyTestWorker(t *testing.T, cfg RuntimeConfig, objects ...runtime.Object) worker {
	t.Helper()
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	return worker{
		app: &application{
			runtimeConfig: cfg,
			k8sClient:     &k8s.K8sClient{Dynamic: client},
		},
		notebook: Notebook{
			ID: 1, Name: "demo", Namespace: "user-ns", PVCname: "demo-pvc", StorageSize: "10Gi",
		},
		logger: slog.Default(),
	}
}

func boundPVC(namespace, name string, accessModes ...string) *unstructured.Unstructured {
	modes := make([]any, len(accessModes))
	for i, mode := range accessModes {
		modes[i] = mode
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     map[string]any{"accessModes": modes, "volumeMode": "Filesystem"},
		"status":   map[string]any{"phase": "Bound"},
	}}
}

func TestPreparePVCMountsUsesOnlyConfiguredMounts(t *testing.T) {
	cfg := RuntimeConfig{
		APIVersion: runtimeConfigAPIVersion,
		Defaults:   RuntimeConfigDefaults{ExternalPVCWaitTimeout: "50ms"},
		Workloads: map[string]WorkloadPolicy{"cpu": {
			PVCMounts: []PVCMountConfig{{
				Name: "datasets", MountPath: "/mnt/datasets", ReadOnly: true,
				Source: PVCSourceConfig{Type: existingPVCSourceType, ClaimNameTemplate: "datasets", Expected: &PVCExpectationConfig{AccessModes: []string{"ReadWriteMany"}}},
			}},
		}},
	}
	w := newVolumePolicyTestWorker(t, cfg, boundPVC("user-ns", "datasets", "ReadWriteMany"))
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatalf("PreparePVCMounts returned error: %v", err)
	}
	if len(w.resolvedPVCMounts) != 1 || w.resolvedPVCMounts[0].Name != "datasets" {
		t.Fatalf("unexpected mounts: %#v", w.resolvedPVCMounts)
	}
}

func TestPreparePVCMountsSkipsMissingOptionalClaim(t *testing.T) {
	cfg := RuntimeConfig{
		APIVersion: runtimeConfigAPIVersion,
		Defaults:   RuntimeConfigDefaults{ExternalPVCWaitTimeout: "10ms"},
		Workloads: map[string]WorkloadPolicy{"cpu": {
			PVCMounts: []PVCMountConfig{{
				Name: "optional", MountPath: "/mnt/optional", Required: boolPointer(false),
				Source: PVCSourceConfig{Type: existingPVCSourceType, ClaimNameTemplate: "optional"},
			}},
		}},
	}
	w := newVolumePolicyTestWorker(t, cfg)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatalf("optional missing claim should not fail: %v", err)
	}
	if len(w.resolvedPVCMounts) != 0 {
		t.Fatalf("missing optional claim should be omitted: %#v", w.resolvedPVCMounts)
	}
}

func TestPreparePVCMountsCreatesManagedWorkspace(t *testing.T) {
	cfg := RuntimeConfig{
		APIVersion: runtimeConfigAPIVersion,
		Workloads: map[string]WorkloadPolicy{"cpu": {
			PVCMounts: []PVCMountConfig{{
				Name: "workspace", MountPath: "/home/jovyan", Workspace: true,
				Source: PVCSourceConfig{
					Type: managedPVCSourceType, ClaimNameTemplate: "{pvcName}", RetentionPolicy: retentionDeleteWithNotebook,
					Spec: map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "{storageSize}"}}},
				},
			}},
		}},
	}
	w := newVolumePolicyTestWorker(t, cfg)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatalf("PreparePVCMounts returned error: %v", err)
	}
	pvc, err := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace("user-ns").Get(context.Background(), "demo-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("managed PVC was not created: %v", err)
	}
	if pvc.GetLabels()[retentionLabelKey] != retentionDelete {
		t.Fatalf("managed PVC missing cleanup label: %#v", pvc.GetLabels())
	}
	storage, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	if storage != "10Gi" {
		t.Fatalf("storage template was not rendered: %q", storage)
	}
}

func TestApplySchedulingUsesConfiguredInstanceOverride(t *testing.T) {
	instanceType := "g4dn.xlarge"
	w := worker{
		notebook: Notebook{InstanceType: &instanceType},
		policy: WorkloadPolicy{Scheduling: SchedulingPolicy{
			NodeSelector:         map[string]string{"workload": "gpu"},
			Tolerations:          []map[string]any{{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"}},
			InstanceTypeOverride: InstanceTypeOverrideConfig{Enabled: true, Required: true, SelectorKey: "node.kubernetes.io/instance-type"},
		}},
	}
	spec := map[string]any{}
	if err := w.applyScheduling(spec); err != nil {
		t.Fatalf("applyScheduling returned error: %v", err)
	}
	selector := spec["nodeSelector"].(map[string]any)
	if selector["workload"] != "gpu" || selector["node.kubernetes.io/instance-type"] != instanceType {
		t.Fatalf("unexpected selector: %#v", selector)
	}
	if len(spec["tolerations"].([]any)) != 1 {
		t.Fatalf("tolerations were not applied: %#v", spec["tolerations"])
	}
}

func TestExternalPVCWaitTimeoutParses(t *testing.T) {
	cfg := RuntimeConfig{Defaults: RuntimeConfigDefaults{ExternalPVCWaitTimeout: "250ms"}}
	got, err := cfg.ExternalPVCWaitTimeout()
	if err != nil || got != 250*time.Millisecond {
		t.Fatalf("unexpected timeout: %v %v", got, err)
	}
}

func TestPreparePVCMountsIncludesDirectNFSWithoutPVC(t *testing.T) {
	cfg := RuntimeConfig{
		APIVersion: runtimeConfigAPIVersion,
		Workloads: map[string]WorkloadPolicy{"cpu": {
			PVCMounts: []PVCMountConfig{{
				Name: "cbr-sanscog", MountPath: "/mnt/cbr/SANSCOG", ReadOnly: true,
				Source: PVCSourceConfig{Type: nfsVolumeSourceType, NFS: &NFSVolumeConfig{Server: "10.0.0.91", Path: "/gpfs/data/tata"}},
			}},
		}},
	}
	w := newVolumePolicyTestWorker(t, cfg)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatalf("PreparePVCMounts returned error: %v", err)
	}
	if len(w.resolvedPVCMounts) != 1 {
		t.Fatalf("unexpected mounts: %#v", w.resolvedPVCMounts)
	}
	mount := w.resolvedPVCMounts[0]
	if mount.NFS == nil || mount.NFS.Server != "10.0.0.91" || mount.ClaimName != "" {
		t.Fatalf("unexpected NFS mount: %#v", mount)
	}
}
