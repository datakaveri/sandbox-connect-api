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

func addSharedWorkspaceMount(t *testing.T, template *SandboxNotebookTemplate) {
	t.Helper()
	template.Spec.Lifecycle.VolumePolicies = append(template.Spec.Lifecycle.VolumePolicies, VolumeLifecyclePolicy{
		Name: sharedWorkspaceVolumeName,
		Existing: &ExistingVolumePolicy{Expected: &PVCExpectationConfig{
			AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem",
		}},
	})
	podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volumes = append(volumes, map[string]any{"name": sharedWorkspaceVolumeName, "persistentVolumeClaim": map[string]any{"claimName": "workspace", "readOnly": true}})
	containers, _ := templateNamedItems(podSpec, "containers")
	mounts, _ := nestedSliceOrEmpty(containers[0], "volumeMounts")
	containers[0]["volumeMounts"] = append(mounts, map[string]any{"name": sharedWorkspaceVolumeName, "mountPath": "/home/jovyan/workspace", "readOnly": true})
	podSpec["volumes"], podSpec["containers"] = mapsToAny(volumes), mapsToAny(containers)
	_ = unstructured.SetNestedMap(template.Spec.Notebook, podSpec, "spec", "template", "spec")
}

func addEvaluationWorkspaceMount(t *testing.T, template *SandboxNotebookTemplate) {
	t.Helper()
	template.Spec.Lifecycle.VolumePolicies = append(template.Spec.Lifecycle.VolumePolicies, VolumeLifecyclePolicy{
		Name: evaluationWorkspaceVolumeName,
		Existing: &ExistingVolumePolicy{Expected: &PVCExpectationConfig{
			AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem",
		}},
	})
	podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volumes = append(volumes, map[string]any{"name": evaluationWorkspaceVolumeName, "persistentVolumeClaim": map[string]any{"claimName": "{evaluationWorkspacePVCName}", "readOnly": true}})
	containers, _ := templateNamedItems(podSpec, "containers")
	mounts, _ := nestedSliceOrEmpty(containers[0], "volumeMounts")
	containers[0]["volumeMounts"] = append(mounts, map[string]any{"name": evaluationWorkspaceVolumeName, "mountPath": "{evaluationWorkspaceMountPath}", "readOnly": true})
	podSpec["volumes"], podSpec["containers"] = mapsToAny(volumes), mapsToAny(containers)
	_ = unstructured.SetNestedMap(template.Spec.Notebook, podSpec, "spec", "template", "spec")
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

func TestWorkspaceFlagDisabledOmitsSharedWorkspace(t *testing.T) {
	template := testSandboxTemplate("cpu")
	addSharedWorkspaceMount(t, template)
	w := newVolumePolicyTestWorker(t, template)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatal(err)
	}
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ := unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	if _, ok := findNamedItem(volumes, sharedWorkspaceVolumeName); ok {
		t.Fatal("disabled workspace volume was rendered")
	}
	containers, _ := templateNamedItems(podSpec, "containers")
	primary, _ := findNamedItem(containers, "demo")
	mounts, _ := containerVolumeMounts(primary)
	if _, ok := findMountByName(mounts, sharedWorkspaceVolumeName); ok {
		t.Fatal("disabled workspace mount was rendered")
	}
}

func TestWorkspaceFlagEnabledMountsSharedWorkspaceReadOnly(t *testing.T) {
	template := testSandboxTemplate("cpu")
	addSharedWorkspaceMount(t, template)
	w := newVolumePolicyTestWorker(t, template, boundPVC("user-ns", "workspace", "ReadWriteMany"))
	w.app.env.WorkspaceEnabled = true
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatal(err)
	}
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ := unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	workspaceVolume, ok := findNamedItem(volumes, sharedWorkspaceVolumeName)
	if !ok {
		t.Fatal("enabled workspace volume was omitted")
	}
	claim, _, _ := unstructured.NestedString(workspaceVolume, "persistentVolumeClaim", "claimName")
	volumeReadOnly, _, _ := unstructured.NestedBool(workspaceVolume, "persistentVolumeClaim", "readOnly")
	if claim != "workspace" || !volumeReadOnly {
		t.Fatalf("workspace PVC source is not read-only: %#v", workspaceVolume)
	}
	containers, _ := templateNamedItems(podSpec, "containers")
	primary, _ := findNamedItem(containers, "demo")
	mounts, _ := containerVolumeMounts(primary)
	workspaceMount, ok := findMountByName(mounts, sharedWorkspaceVolumeName)
	if !ok || workspaceMount["mountPath"] != "/home/jovyan/workspace" || workspaceMount["readOnly"] != true {
		t.Fatalf("workspace mount is wrong: %#v", workspaceMount)
	}
}

func TestEvaluationWorkspaceFlagDisabledOmitsDedicatedWorkspace(t *testing.T) {
	template := testSandboxTemplate("cpu")
	addEvaluationWorkspaceMount(t, template)
	w := newVolumePolicyTestWorker(t, template)
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatal(err)
	}
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ := unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	if _, ok := findNamedItem(volumes, evaluationWorkspaceVolumeName); ok {
		t.Fatal("disabled evaluation workspace volume was rendered")
	}
	containers, _ := templateNamedItems(podSpec, "containers")
	primary, _ := findNamedItem(containers, "demo")
	mounts, _ := containerVolumeMounts(primary)
	if _, ok := findMountByName(mounts, evaluationWorkspaceVolumeName); ok {
		t.Fatal("disabled evaluation workspace mount was rendered")
	}
}

func TestEvaluationWorkspaceFlagEnabledUsesConfiguredClaimReadOnly(t *testing.T) {
	template := testSandboxTemplate("cpu")
	addEvaluationWorkspaceMount(t, template)
	w := newVolumePolicyTestWorker(t, template,
		boundPVC("user-ns", "custom-evaluation-workspace", "ReadWriteMany"))
	w.app.env.EvaluationWorkspaceEnabled = true
	w.app.env.EvaluationWorkspacePVCName = "custom-evaluation-workspace"
	w.app.env.EvaluationWorkspaceMountPath = "/mnt/evaluation"
	if err := w.PreparePVCMounts(); err != nil {
		t.Fatal(err)
	}
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ := unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volume, ok := findNamedItem(volumes, evaluationWorkspaceVolumeName)
	if !ok {
		t.Fatal("enabled evaluation workspace volume was omitted")
	}
	claim, _, _ := unstructured.NestedString(volume, "persistentVolumeClaim", "claimName")
	readOnly, _, _ := unstructured.NestedBool(volume, "persistentVolumeClaim", "readOnly")
	if claim != "custom-evaluation-workspace" || !readOnly {
		t.Fatalf("evaluation workspace PVC source is wrong: %#v", volume)
	}
	containers, _ := templateNamedItems(podSpec, "containers")
	primary, _ := findNamedItem(containers, "demo")
	mounts, _ := containerVolumeMounts(primary)
	mount, ok := findMountByName(mounts, evaluationWorkspaceVolumeName)
	if !ok || mount["mountPath"] != "/mnt/evaluation" || mount["readOnly"] != true {
		t.Fatalf("evaluation workspace mount is wrong: %#v", mount)
	}
}

func TestWorkerEvaluationWorkspaceConfigurationValidation(t *testing.T) {
	valid := Env{
		EvaluationWorkspaceEnabled:   true,
		EvaluationWorkspacePVCName:   "evaluation-workspace",
		EvaluationWorkspaceMountPath: "/home/jovyan/evaluation-workspace",
	}
	if err := valid.ValidateEvaluationWorkspace(); err != nil {
		t.Fatalf("valid workspace configuration was rejected: %v", err)
	}
	invalidClaim := valid
	invalidClaim.EvaluationWorkspacePVCName = "Invalid_Name"
	if err := invalidClaim.ValidateEvaluationWorkspace(); err == nil {
		t.Fatal("expected invalid PVC name to be rejected")
	}
	invalidPath := valid
	invalidPath.EvaluationWorkspaceMountPath = "relative/path"
	if err := invalidPath.ValidateEvaluationWorkspace(); err == nil {
		t.Fatal("expected relative mount path to be rejected")
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
