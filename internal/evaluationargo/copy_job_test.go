package evaluationargo

import (
	"strings"
	"testing"

	"sandbox-backend-service/internal/evaluation"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func validCopyJobConfig() CopyJobConfig {
	return CopyJobConfig{
		CopierImage:        "registry.example/evaluation-copier@sha256:" + strings.Repeat("c", 64),
		ServiceAccountName: "evaluation-runner", WorkspacePVCName: "workspace",
		UserWorkspacePath: "/home/jovyan/workspace", WorkspaceMountPath: "/workspace",
		FileServiceSecretName: "evaluation-file-service",
		ActiveDeadlineSeconds: 900, TTLSecondsAfterFinish: 3600,
	}
}

func approvalWork(destination string, attempt int) evaluation.ApprovalWork {
	return evaluation.ApprovalWork{
		Evaluation: evaluation.Record{
			ID: "10000000-0000-0000-0000-000000000001", Namespace: "user-ns",
			ManifestKey: "/user/demo/evaluations/run/manifest.json",
		},
		Approval: evaluation.Approval{Destination: destination, AttemptCount: attempt},
	}
}

func TestBuildCopyJobMapsNotebookPathAndUsesAttemptIdentity(t *testing.T) {
	job, err := BuildCopyJob(approvalWork("/home/jovyan/workspace/evaluations/demo/run", 2), validCopyJobConfig())
	if err != nil {
		t.Fatal(err)
	}
	if job.GetName() != "evaluation-copy-10000000000000000000-a2" {
		t.Fatalf("unexpected job name: %s", job.GetName())
	}
	containers, found, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("copy container unavailable: found=%v err=%v", found, err)
	}
	container := containers[0].(map[string]any)
	args := container["args"].([]any)
	if args[3] != "/workspace/evaluations/demo/run" {
		t.Fatalf("user path was not mapped to the mounted PVC: %#v", args)
	}
}

func TestBuildCopyJobRejectsDestinationOutsideWorkspace(t *testing.T) {
	if _, err := BuildCopyJob(approvalWork("/etc/evaluation-output", 1), validCopyJobConfig()); err == nil {
		t.Fatal("expected destination outside the workspace to be rejected")
	}
}

func TestVerifyCopyJobIdentityIncludesApprovalAttempt(t *testing.T) {
	work := approvalWork("/home/jovyan/workspace/evaluations/demo/run", 2)
	job, err := BuildCopyJob(work, validCopyJobConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCopyJobIdentity(job, work); err != nil {
		t.Fatalf("expected matching identity: %v", err)
	}
	labels := job.GetLabels()
	labels["sandbox-connect.tgdex.io/approval-attempt"] = "1"
	job.SetLabels(labels)
	if err := verifyCopyJobIdentity(job, work); err == nil {
		t.Fatal("expected a mismatched approval attempt to be rejected")
	}
}

func TestImageDigestValidationRequiresFullDigest(t *testing.T) {
	cfg := validWorkflowConfig()
	cfg.RunnerImage = "registry.example/runner@sha256:short"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected a short image digest to be rejected")
	}
}
