package outputargo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"sandbox-backend-service/internal/output"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type publicationQueue struct {
	publishing bool
	completed  bool
	failed     bool
	result     output.PublicationResult
}

func (*publicationQueue) ClaimNextOutput(context.Context, string, time.Duration) (output.Record, bool, error) {
	return output.Record{}, false, nil
}
func (*publicationQueue) SetWorkflow(context.Context, string, string, string, string) error {
	return nil
}
func (*publicationQueue) SetPhase(context.Context, string, string, output.Status, time.Duration) error {
	return nil
}
func (*publicationQueue) CompleteOutput(context.Context, string, string, json.RawMessage) error {
	return nil
}
func (*publicationQueue) FailOutput(context.Context, string, string, string, string) error {
	return nil
}
func (*publicationQueue) ClaimNextApproval(context.Context, string, time.Duration) (output.ApprovalWork, bool, error) {
	return output.ApprovalWork{}, false, nil
}
func (q *publicationQueue) SetPublishing(context.Context, string, string, time.Duration) error {
	q.publishing = true
	return nil
}
func (q *publicationQueue) CompleteApproval(_ context.Context, _, _ string, result output.PublicationResult) error {
	q.completed = true
	q.result = result
	return nil
}
func (q *publicationQueue) FailApproval(context.Context, string, string, string, string) error {
	q.failed = true
	return nil
}

type publicationClient struct {
	result output.PublicationResult
	err    error
}

func (p publicationClient) Publish(context.Context, output.ApprovalWork) (output.PublicationResult, error) {
	return p.result, p.err
}

func TestReconcileApprovalPublishesThroughFilesService(t *testing.T) {
	queue := &publicationQueue{}
	result := output.PublicationResult{
		WorkspacePrefix:      "user-workspaces/users/user-1/outputs/output-1/",
		WorkspaceManifestKey: "user-workspaces/users/user-1/outputs/output-1/manifest.json",
		ApprovedFileIDs:      []string{"file-1"},
	}
	controller := &Controller{
		queue: queue, publisher: publicationClient{result: result},
		config: ControllerConfig{WorkerID: "worker-1", ClaimLease: time.Minute},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	work := output.ApprovalWork{
		Output:   output.Record{ID: "output-1"},
		Approval: output.Approval{WorkspacePrefix: result.WorkspacePrefix},
	}
	if err := controller.reconcileApproval(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if !queue.publishing || !queue.completed || queue.failed {
		t.Fatalf("unexpected queue transitions: %#v", queue)
	}
	if queue.result.WorkspaceManifestKey != result.WorkspaceManifestKey {
		t.Fatalf("publication result was not persisted: %#v", queue.result)
	}
}

func TestReconcileApprovalRejectsWrongWorkspacePrefix(t *testing.T) {
	queue := &publicationQueue{}
	controller := &Controller{
		queue: queue,
		publisher: publicationClient{result: output.PublicationResult{
			WorkspacePrefix: "user-workspaces/users/another-user/outputs/output-1/",
		}},
		config: ControllerConfig{WorkerID: "worker-1", ClaimLease: time.Minute},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	work := output.ApprovalWork{
		Output: output.Record{ID: "output-1"},
		Approval: output.Approval{
			WorkspacePrefix: "user-workspaces/users/user-1/outputs/output-1/",
		},
	}
	if err := controller.reconcileApproval(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if !queue.failed || queue.completed {
		t.Fatalf("mismatched publication was not rejected: %#v", queue)
	}
}

func TestWorkflowManifestFallsBackToArgoRootNodeOutputs(t *testing.T) {
	reviewPrefix := "nha-review/users/user-1/sandboxes/notebook-1/outputs/output-1/"
	raw := `{"files":[{"fileId":"file-1","path":"result.csv","objectKey":"` + reviewPrefix + `output/result.csv","size":20,"mediaType":"text/csv","sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "output-workflow"},
		"status": map[string]any{"nodes": map[string]any{
			"output-workflow": map[string]any{
				"name": "output-workflow",
				"outputs": map[string]any{"parameters": []any{
					map[string]any{"name": "manifest-json", "value": raw},
				}},
			},
		}},
	}}
	manifest, err := workflowManifest(workflow, 262144, 1000, 268435456, 1073741824, reviewPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifest) != raw {
		t.Fatalf("unexpected manifest: %s", manifest)
	}
}
