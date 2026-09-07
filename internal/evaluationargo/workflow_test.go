package evaluationargo

import (
	"testing"

	"sandbox-backend-service/internal/evaluation"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func validWorkflowConfig() WorkflowConfig {
	return WorkflowConfig{
		RunnerImage:        "registry.example/evaluation-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		UploaderImage:      "registry.example/evaluation-uploader@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ServiceAccountName: "evaluation-runner", ScratchStorageClass: "gp3", ScratchStorageSize: "5Gi",
		ReplacementConfigMapName: "evaluation-replacements-v1",
		ProductionEnvSecretName:  "evaluation-production-env", FileServiceSecretName: "evaluation-file-service",
		ActiveDeadlineSeconds: 1800, TTLSecondsAfterFinished: 3600,
	}
}

func TestWorkflowImagesMustBePinned(t *testing.T) {
	cfg := validWorkflowConfig()
	cfg.RunnerImage = "registry.example/evaluation-runner:latest"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected mutable runner image to be rejected")
	}
}

func TestWorkflowStageOrderAndCredentialIsolation(t *testing.T) {
	record := evaluation.Record{
		ID: "10000000-0000-0000-0000-000000000001", Namespace: "user-ns",
		NotebookName: "demo-notebook", SourcePVC: "demo-notebook-pvc",
		SourceNotebookPath: "nha_ps4_evaluation_template.ipynb",
		OutputPrefix:       "/user/demo-notebook/evaluations/10000000-0000-0000-0000-000000000001/",
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

	mainSteps := templateByName["evaluation"]["steps"].([]any)
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
	record := evaluation.Record{
		ID: "10000000-0000-0000-0000-000000000001", Namespace: "user-ns",
		NotebookName: "demo-notebook", SourcePVC: "demo-notebook-pvc",
		SourceNotebookPath: "../nha_ps4_evaluation_template.ipynb",
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
