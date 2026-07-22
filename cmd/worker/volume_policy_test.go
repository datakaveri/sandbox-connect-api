package main

import (
	"context"
	"log/slog"
	"testing"

	"sandbox-backend-service/pkg/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func newVolumePolicyTestWorker(t *testing.T, template *SandboxNotebookTemplate, objects ...runtime.Object) worker {
	t.Helper()
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	return worker{
		app:      &application{notebookTemplates: map[string]*SandboxNotebookTemplate{"cpu": template}, k8sClient: &k8s.K8sClient{Dynamic: client}},
		notebook: Notebook{ID: 1, Name: "demo", Namespace: "user-ns", PVCname: "demo-pvc", StorageSize: "10Gi"},
		logger:   slog.Default(),
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

func TestPreparePVCMountsCreatesManagedWorkspace(t *testing.T) {
	template := testSandboxTemplate("cpu")
	w := newVolumePolicyTestWorker(t, template)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatal(err)
	}
	pvc, err := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace("user-ns").Get(context.Background(), "demo-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("managed PVC was not created: %v", err)
	}
	if pvc.GetLabels()[retentionLabelKey] != retentionDelete {
		t.Fatalf("managed PVC missing cleanup label: %#v", pvc.GetLabels())
	}
	if pvc.GetLabels()["example.com/notebook"] != "demo" {
		t.Fatalf("managed PVC template label was not preserved or rendered: %#v", pvc.GetLabels())
	}
	if pvc.GetAnnotations()["example.com/namespace"] != "user-ns" {
		t.Fatalf("managed PVC template annotation was not preserved or rendered: %#v", pvc.GetAnnotations())
	}
	if pvc.GetName() != "demo-pvc" || pvc.GetNamespace() != "user-ns" {
		t.Fatalf("worker-owned PVC identity was not applied: %s/%s", pvc.GetNamespace(), pvc.GetName())
	}
	storage, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	if storage != "10Gi" {
		t.Fatalf("managed spec token not rendered: %q", storage)
	}
}

func TestPreparePVCMountsOmitsUnavailableOptionalExistingClaim(t *testing.T) {
	template := testSandboxTemplate("cpu")
	template.Spec.Lifecycle.VolumePolicies = append(template.Spec.Lifecycle.VolumePolicies, VolumeLifecyclePolicy{Name: "optional", Required: boolPointer(false), Existing: &ExistingVolumePolicy{}})
	podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volumes = append(volumes, map[string]any{"name": "optional", "persistentVolumeClaim": map[string]any{"claimName": "optional-claim"}})
	containers, _ := templateNamedItems(podSpec, "containers")
	mounts, _ := nestedSliceOrEmpty(containers[0], "volumeMounts")
	containers[0]["volumeMounts"] = append(mounts, map[string]any{"name": "optional", "mountPath": "/optional"})
	podSpec["volumes"], podSpec["containers"] = mapsToAny(volumes), mapsToAny(containers)
	_ = unstructured.SetNestedMap(template.Spec.Notebook, podSpec, "spec", "template", "spec")

	w := newVolumePolicyTestWorker(t, template)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatalf("optional missing claim should not fail: %v", err)
	}
	if _, omitted := w.omittedVolumes["optional"]; !omitted {
		t.Fatal("optional unavailable volume was not marked for omission")
	}
	if len(w.resolvedPVCMounts) != 1 || w.resolvedPVCMounts[0].Name != "user-data" {
		t.Fatalf("unexpected available PVCs: %#v", w.resolvedPVCMounts)
	}
}

func TestPreparePVCMountsValidatesExistingClaimCompatibility(t *testing.T) {
	template := testSandboxTemplate("cpu")
	template.Spec.Lifecycle.VolumePolicies = []VolumeLifecyclePolicy{{Name: "user-data", Existing: &ExistingVolumePolicy{Expected: &PVCExpectationConfig{AccessModes: []string{"ReadWriteMany"}}}}}
	w := newVolumePolicyTestWorker(t, template, boundPVC("user-ns", "demo-pvc", "ReadWriteOnce"))
	if err := w.PreparePVCMounts(); err == nil {
		t.Fatal("expected incompatible required PVC to fail")
	}
}
