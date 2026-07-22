package main

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func newBuilderTestWorker(workload string, nb Notebook) worker {
	template := testSandboxTemplate(workload)
	return worker{
		app:               &application{notebookTemplates: map[string]*SandboxNotebookTemplate{workload: template}},
		template:          template,
		notebook:          nb,
		logger:            slog.Default(),
		resolvedPVCMounts: []ResolvedPVCMount{{Name: "user-data", ClaimName: nb.PVCname, MountPath: "/home/jovyan", Workspace: true, Managed: true}},
	}
}

func primaryContainer(t *testing.T, notebook *unstructured.Unstructured) map[string]any {
	t.Helper()
	podSpec, _, _ := unstructured.NestedMap(notebook.Object, "spec", "template", "spec")
	containers, err := templateNamedItems(podSpec, "containers")
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range containers {
		if container["name"] == notebook.GetName() {
			return container
		}
	}
	t.Fatal("primary container not found")
	return nil
}

func TestBuildNotebookSelectsTemplateAndMergesResources(t *testing.T) {
	gpuType := "nvidia.com/gpu"
	gpuRequest, gpuLimit := 1, 2
	customImage := "registry/custom:latest"
	instance := "g4dn.xlarge"
	nb := Notebook{Name: "demo", Namespace: "user-ns", PVCname: "demo-pvc", StorageSize: "20Gi", CPURequest: 1.25, CPULimit: 2.5, MemoryRequest: "2Gi", MemoryLimit: "4Gi", GPUType: &gpuType, GPURequest: &gpuRequest, GPULimit: &gpuLimit, ImageName: &customImage, InstanceType: &instance}
	w := newBuilderTestWorker("gpu", nb)

	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryContainer(t, built)
	if primary["image"] != customImage {
		t.Fatalf("custom image not applied: %#v", primary["image"])
	}
	requests, _, _ := unstructured.NestedMap(primary, "resources", "requests")
	limits, _, _ := unstructured.NestedMap(primary, "resources", "limits")
	if requests["ephemeral-storage"] != "1Gi" || limits["ephemeral-storage"] != "10Gi" {
		t.Fatalf("static resources were not preserved: requests=%#v limits=%#v", requests, limits)
	}
	if requests[gpuType] != int64(1) || limits[gpuType] != int64(2) || requests["memory"] != "2Gi" {
		t.Fatalf("dynamic resources incorrect: requests=%#v limits=%#v", requests, limits)
	}
	podSpec, _, _ := unstructured.NestedMap(built.Object, "spec", "template", "spec")
	selector, _, _ := unstructured.NestedStringMap(podSpec, "nodeSelector")
	if selector["node.kubernetes.io/instance-type"] != instance {
		t.Fatalf("instance override missing: %#v", selector)
	}
	volumes, _ := templateNamedItems(podSpec, "volumes")
	workspace, _ := findNamedItem(volumes, "user-data")
	claim, _, _ := unstructured.NestedString(workspace, "persistentVolumeClaim", "claimName")
	if claim != "demo-pvc" {
		t.Fatalf("PVC token not rendered: %q", claim)
	}
}

func TestBuildNotebookDefaultImagesAndTemplateImmutability(t *testing.T) {
	for _, workload := range []string{"cpu", "gpu"} {
		t.Run(workload, func(t *testing.T) {
			template := testSandboxTemplate(workload)
			original := fmt.Sprintf("%#v", template.Spec.Notebook)
			app := &application{notebookTemplates: map[string]*SandboxNotebookTemplate{workload: template}}
			var wg sync.WaitGroup
			errs := make(chan error, 16)
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					w := worker{app: app, template: template, notebook: Notebook{Name: fmt.Sprintf("demo-%d", i), Namespace: "user-ns", PVCname: fmt.Sprintf("pvc-%d", i), CPURequest: 1, CPULimit: 2, MemoryRequest: "1Gi", MemoryLimit: "2Gi"}, logger: slog.Default()}
					if _, err := w.BuildNotebook(); err != nil {
						errs <- err
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			if got := fmt.Sprintf("%#v", template.Spec.Notebook); got != original {
				t.Fatal("shared template was mutated")
			}
		})
	}
}

func TestBuildNotebookRemovesOptionalVolumeEverywhere(t *testing.T) {
	template := testSandboxTemplate("cpu")
	podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volumes = append(volumes, map[string]any{"name": "optional", "persistentVolumeClaim": map[string]any{"claimName": "optional"}})
	containers, _ := templateNamedItems(podSpec, "containers")
	for _, container := range containers {
		mounts, _ := nestedSliceOrEmpty(container, "volumeMounts")
		container["volumeMounts"] = append(mounts, map[string]any{"name": "optional", "mountPath": "/optional"})
	}
	podSpec["volumes"], podSpec["containers"] = mapsToAny(volumes), mapsToAny(containers)
	_ = unstructured.SetNestedMap(template.Spec.Notebook, podSpec, "spec", "template", "spec")
	nb := Notebook{Name: "demo", Namespace: "user-ns", PVCname: "demo-pvc", CPURequest: 1, CPULimit: 2, MemoryRequest: "1Gi", MemoryLimit: "2Gi"}
	w := newBuilderTestWorker("cpu", nb)
	w.template = template
	w.omittedVolumes = map[string]struct{}{"optional": {}}
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ = unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ = templateNamedItems(podSpec, "volumes")
	if _, found := findNamedItem(volumes, "optional"); found {
		t.Fatal("optional volume was preserved")
	}
	primary := primaryContainer(t, built)
	mounts, _ := containerVolumeMounts(primary)
	if _, found := findMountByName(mounts, "optional"); found {
		t.Fatal("optional mount was preserved")
	}
}

func TestBuildNotebookPatchesTemplateOwnedPlatformTokenResources(t *testing.T) {
	template := testSandboxTemplate("cpu")
	template.Spec.Lifecycle.PlatformToken = &PlatformTokenTemplateConfig{SessionAPIBaseURL: "https://sandbox.example.com/api"}
	podSpec, _, _ := unstructured.NestedMap(template.Spec.Notebook, "spec", "template", "spec")
	volumes, _ := templateNamedItems(podSpec, "volumes")
	volumes = append(volumes,
		map[string]any{"name": platformRefreshTokenVolumeName, "secret": map[string]any{"secretName": "", "optional": true}},
		map[string]any{"name": platformTokenCacheVolumeName, "emptyDir": map[string]any{"medium": "Memory"}},
	)
	containers, _ := templateNamedItems(podSpec, "containers")
	primary := containers[0]
	primary["env"] = []any{map[string]any{"name": "MAHAAGX_TOKEN_FILE", "value": platformAccessTokenFile}, map[string]any{"name": "MAHAAGX_FILE_API_BASE_URL", "value": "https://files.example.com"}}
	mounts, _ := nestedSliceOrEmpty(primary, "volumeMounts")
	primary["volumeMounts"] = append(mounts, map[string]any{"name": platformTokenCacheVolumeName, "mountPath": platformTokenCacheMountPath, "readOnly": true})
	containers = append(containers, map[string]any{
		"name": platformTokenSidecarName, "image": "token-sidecar:latest",
		"securityContext": map[string]any{"privileged": false, "allowPrivilegeEscalation": false, "procMount": "Default"},
		"env": []any{
			map[string]any{"name": "BOOTSTRAP_TOKEN_FILE", "value": "/refresh/bootstrap.json"},
			map[string]any{"name": "READY_ADDRESS", "value": "0.0.0.0:8081"},
			map[string]any{"name": "TOKEN_SESSION_URL", "value": ""},
			map[string]any{"name": "EXPECTED_USER_ID", "value": ""},
		},
		"volumeMounts": []any{map[string]any{"name": platformRefreshTokenVolumeName, "mountPath": platformRefreshTokenMountPath, "readOnly": true}, map[string]any{"name": platformTokenCacheVolumeName, "mountPath": platformTokenCacheMountPath}},
	})
	podSpec["volumes"], podSpec["containers"] = mapsToAny(volumes), mapsToAny(containers)
	_ = unstructured.SetNestedMap(template.Spec.Notebook, podSpec, "spec", "template", "spec")
	bookingID := int64(42)
	nb := Notebook{Name: "demo", Namespace: "user-ns", PVCname: "demo-pvc", CPURequest: 1, CPULimit: 2, MemoryRequest: "1Gi", MemoryLimit: "2Gi", BookingID: &bookingID}
	w := newBuilderTestWorker("cpu", nb)
	w.template = template
	built, err := w.BuildNotebook()
	if err != nil {
		t.Fatal(err)
	}
	podSpec, _, _ = unstructured.NestedMap(built.Object, "spec", "template", "spec")
	volumes, _ = templateNamedItems(podSpec, "volumes")
	refresh, _ := findNamedItem(volumes, platformRefreshTokenVolumeName)
	secretName, _, _ := unstructured.NestedString(refresh, "secret", "secretName")
	if secretName != "demo-plt-token" {
		t.Fatalf("token Secret name not patched: %q", secretName)
	}
	containers, _ = templateNamedItems(podSpec, "containers")
	sidecar, _ := findNamedItem(containers, platformTokenSidecarName)
	env, _ := nestedSliceOrEmpty(sidecar, "env")
	values := map[string]string{}
	for _, item := range env {
		entry := item.(map[string]any)
		values[entry["name"].(string)], _ = entry["value"].(string)
	}
	if values["TOKEN_SESSION_URL"] != "https://sandbox.example.com/api/v1/bookings/42/notebook-token-session" || values["EXPECTED_USER_ID"] != "user-ns" {
		t.Fatalf("dynamic token env not patched: %#v", values)
	}
	podLabels, found, err := unstructured.NestedStringMap(built.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found || podLabels[platformTokenNotebookLabel] != "demo" {
		t.Fatalf("platform token pod label missing: found=%v err=%v labels=%#v", found, err, podLabels)
	}
}
