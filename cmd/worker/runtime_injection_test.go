package main

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func workerStringPtr(value string) *string {
	return &value
}

func TestRuntimeAssetNames(t *testing.T) {
	if got := runtimeAssetFileName("https://example.com/path/data.csv?download=1"); got != "data.csv" {
		t.Fatalf("expected data.csv, got %q", got)
	}
	if got := runtimeAssetFileName("https://example.com"); got != "downloaded-file" {
		t.Fatalf("expected fallback filename, got %q", got)
	}
	if got := runtimeAssetRepoDir("https://github.com/datakaveri/example.git"); got != "example" {
		t.Fatalf("expected example repo dir, got %q", got)
	}
}

func TestBuildRuntimeInjectionPod(t *testing.T) {
	fileURL := "https://example.com/files/input.csv"
	gitURL := "https://github.com/datakaveri/private-repo.git"
	secretName := "github-token"
	w := worker{
		app:      &application{},
		template: testSandboxTemplate("cpu"),
		notebook: Notebook{
			Name:               "demo-notebook",
			Namespace:          "user-ns",
			PVCname:            "demo-notebook-pvc",
			FileURL:            &fileURL,
			GitURL:             &gitURL,
			GitTokenSecretName: &secretName,
		},
		resolvedPVCMounts: []ResolvedPVCMount{{
			Name: "workspace", ClaimName: "demo-notebook-pvc", Workspace: true, SubPath: "notebooks/demo",
		}},
	}

	pod, err := w.buildRuntimeInjectionPod("demo-inject-12345678")
	if err != nil {
		t.Fatalf("buildRuntimeInjectionPod returned error: %v", err)
	}
	if got := pod.GetName(); got != "demo-inject-12345678" {
		t.Fatalf("unexpected pod name: %q", got)
	}
	if got := pod.GetNamespace(); got != "user-ns" {
		t.Fatalf("unexpected namespace: %q", got)
	}
	volumes, found, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil || !found || len(volumes) != 1 {
		t.Fatalf("expected one volume, found=%v err=%v len=%d", found, err, len(volumes))
	}
	volume := volumes[0].(map[string]any)
	claim := volume["persistentVolumeClaim"].(map[string]any)
	if claim["claimName"] != "demo-notebook-pvc" {
		t.Fatalf("unexpected PVC claim name: %#v", claim["claimName"])
	}

	containers, found, err := unstructured.NestedSlice(pod.Object, "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("expected one container, found=%v err=%v len=%d", found, err, len(containers))
	}
	container := containers[0].(map[string]any)
	if container["image"] != "alpine/git:2.45.2" {
		t.Fatalf("unexpected injector image: %#v", container["image"])
	}
	volumeMount := container["volumeMounts"].([]any)[0].(map[string]any)
	if volumeMount["subPath"] != "notebooks/demo" {
		t.Fatalf("workspace subPath was not preserved: %#v", volumeMount)
	}
	command := container["command"].([]any)
	script := command[2].(string)
	for _, want := range []string{"wget -O", "git clone --depth 1", "GIT_TOKEN"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}

	env := container["env"].([]any)
	values := map[string]any{}
	for _, item := range env {
		entry := item.(map[string]any)
		values[entry["name"].(string)] = entry
	}
	if values["FILE_URL"].(map[string]any)["value"] != fileURL {
		t.Fatalf("FILE_URL env missing or incorrect: %#v", values["FILE_URL"])
	}
	if values["FILE_NAME"].(map[string]any)["value"] != "input.csv" {
		t.Fatalf("FILE_NAME env missing or incorrect: %#v", values["FILE_NAME"])
	}
	if values["GIT_CLONE_DIR"].(map[string]any)["value"] != "private-repo" {
		t.Fatalf("GIT_CLONE_DIR env missing or incorrect: %#v", values["GIT_CLONE_DIR"])
	}
	secretRef := values["GIT_TOKEN"].(map[string]any)["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
	if secretRef["name"] != secretName || secretRef["key"] != "token" {
		t.Fatalf("unexpected token secret ref: %#v", secretRef)
	}
}

func TestRuntimeInjectionPodNameIsDNSLabelLength(t *testing.T) {
	name := newRuntimeInjectionPodName(strings.Repeat("a", 80))
	if len(name) > 63 {
		t.Fatalf("pod name too long: %d", len(name))
	}
	if strings.HasSuffix(name, "-") {
		t.Fatalf("pod name should not end with hyphen: %q", name)
	}
}

func TestNotebookHasRuntimeAssets(t *testing.T) {
	if (Notebook{}).HasRuntimeAssets() {
		t.Fatal("empty notebook should not have runtime assets")
	}
	nb := Notebook{GitURL: workerStringPtr(" https://github.com/datakaveri/repo.git ")}
	if !nb.HasRuntimeAssets() {
		t.Fatal("notebook with gitUrl should have runtime assets")
	}
}
