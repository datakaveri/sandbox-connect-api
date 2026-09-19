package outputargo

import (
	"testing"

	"sandbox-backend-service/internal/output"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func validWorkflowConfig() WorkflowConfig {
	return WorkflowConfig{
		RunnerImage:        "registry.example/output-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		UploaderImage:      "registry.example/output-uploader@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ServiceAccountName: "output-runner", ScratchStorageClass: "gp3", ScratchStorageSize: "5Gi",
		ReplacementConfigMapName: "output-replacements-v1",
		ProductionEnvSecretName:  "output-production-env", FileServiceSecretName: "output-file-service",
		ActiveDeadlineSeconds: 1800, TTLSecondsAfterFinished: 3600,
	}
}

func TestWorkflowImagesMustBePinned(t *testing.T) {
	cfg := validWorkflowConfig()
	cfg.RunnerImage = "registry.example/output-runner:latest"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected mutable runner image to be rejected")
	}
}

func TestWorkflowStageOrderAndCredentialIsolation(t *testing.T) {
	record := output.Record{
		ID: "10000000-0000-0000-0000-000000000001", Namespace: "user-ns",
		NotebookName: "demo-notebook", SourcePVC: "demo-notebook-pvc",
		SourceNotebookPath: "nha_ps4_output_template.ipynb",
		ReviewPrefix:       "nha-review/users/user/sandboxes/demo-notebook/outputs/10000000-0000-0000-0000-000000000001/",
	}
	workflow, err := BuildWorkflow(record, validWorkflowConfig())
	if err != nil {
		t.Fatal(err)
	}
	templates, found, err := unstructured.NestedSlice(workflow.Object, "spec", "templates")
	if err != nil || !found {
		t.Fatalf("workflow templates unavailable: found=%v err=%v", found, err)
	}
	templateByName := map[string]map[string]any{}
	for _, item := range templates {
		template := item.(map[string]any)
		templateByName[template["name"].(string)] = template
	}
	if _, found, err := unstructured.NestedSlice(workflow.Object, "spec", "volumes"); err != nil || found {
		t.Fatalf("sensitive volumes must not be workflow-wide: found=%v err=%v", found, err)
	}
	if !hasDeclaredVolume(templateByName["prepare"], "participant") {
		t.Fatal("participant PVC must be declared only by prepare")
	}
	if !hasDeclaredVolume(templateByName["configure"], "replacement-map") {
		t.Fatal("replacement map must be declared only by configure")
	}

	mainSteps := templateByName["output"]["steps"].([]any)
	wantOrder := []string{"prepare", "convert", "configure", "execute", "upload"}
	for index, wanted := range wantOrder {
		group := mainSteps[index].([]any)
		step := group[0].(map[string]any)
		if step["name"] != wanted {
			t.Fatalf("stage %d is %v, expected %s", index, step["name"], wanted)
		}
	}

	prepare := templateByName["prepare"]["container"].(map[string]any)
	if !hasReadOnlyMount(prepare["volumeMounts"].([]any), "participant") {
		t.Fatal("prepare must mount participant PVC read-only")
	}
	for _, stage := range []string{"convert", "configure", "execute", "upload"} {
		container := templateByName[stage]["container"].(map[string]any)
		if hasMount(container["volumeMounts"].([]any), "participant") {
			t.Fatalf("%s must not receive the participant PVC", stage)
		}
	}
	for _, stage := range []string{"convert", "execute"} {
		args := templateByName[stage]["container"].(map[string]any)["args"].([]any)
		if !containsString(args, "--stream-logs") {
			t.Fatalf("%s must stream its bounded command log", stage)
		}
	}
	if _, found := templateByName["execute"]["container"].(map[string]any)["envFrom"]; !found {
		t.Fatal("execute must receive the approved production environment")
	}
	if _, found := prepare["envFrom"]; found {
		t.Fatal("prepare must not receive credentials")
	}
	upload := templateByName["upload"]["container"].(map[string]any)
	if _, found := upload["envFrom"]; !found {
		t.Fatal("upload must receive file-service credentials")
	}
}

func containsString(values []any, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasMount(mounts []any, name string) bool {
	for _, item := range mounts {
		if item.(map[string]any)["name"] == name {
			return true
		}
	}
	return false
}

func hasReadOnlyMount(mounts []any, name string) bool {
	for _, item := range mounts {
		mount := item.(map[string]any)
		if mount["name"] == name && mount["readOnly"] == true {
			return true
		}
	}
	return false
}

func TestWorkflowRejectsSourcePathTraversal(t *testing.T) {
	record := output.Record{
		ID: "10000000-0000-0000-0000-000000000001", Namespace: "user-ns",
		NotebookName: "demo-notebook", SourcePVC: "demo-notebook-pvc",
		SourceNotebookPath: "../nha_ps4_output_template.ipynb",
	}
	if _, err := BuildWorkflow(record, validWorkflowConfig()); err == nil {
		t.Fatal("expected unsafe source path to be rejected")
	}
}

func hasDeclaredVolume(template map[string]any, name string) bool {
	volumes, ok := template["volumes"].([]any)
	if !ok {
		return false
	}
	for _, item := range volumes {
		if item.(map[string]any)["name"] == name {
			return true
		}
	}
	return false
}

func TestWorkflowUploaderIdentityLimitsAndExecutorIsolation(t *testing.T) {
	cfg := validWorkflowConfig()
	cfg.Limits = output.Limits{MaxManifestBytes: 8192, MaxManifestFiles: 3, MaxFileBytes: 1234, MaxOutputBytes: 2345}
	workflow, err := BuildWorkflow(output.Record{ID: "run", SourceNotebookPath: "input.ipynb", ReviewPrefix: "review/run/"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mounted, found, err := unstructured.NestedBool(workflow.Object, "spec", "automountServiceAccountToken")
	if err != nil || !found || mounted {
		t.Fatal("stage containers must not receive Kubernetes credentials")
	}
	account, _, _ := unstructured.NestedString(workflow.Object, "spec", "executor", "serviceAccountName")
	if account != cfg.ServiceAccountName {
		t.Fatal("missing executor identity")
	}
	templates, _, _ := unstructured.NestedSlice(workflow.Object, "spec", "templates")
	for _, item := range templates {
		template := item.(map[string]any)
		if template["name"] != "upload" {
			continue
		}
		args, _, _ := unstructured.NestedSlice(template, "container", "args")
		values := map[string]string{}
		for i := 0; i < len(args)-1; i++ {
			if key, ok := args[i].(string); ok {
				values[key], _ = args[i+1].(string)
			}
		}
		for flag, value := range map[string]string{"--output-id": "{{workflow.parameters.output-id}}", "--max-files": "3", "--max-manifest-bytes": "8192", "--max-file-bytes": "1234", "--max-output-bytes": "2345"} {
			if values[flag] != value {
				t.Fatalf("%s=%s, expected %s", flag, values[flag], value)
			}
		}
	}
}
