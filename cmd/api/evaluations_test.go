package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"sandbox-backend-service/internal/evaluation"
	"sandbox-backend-service/pkg/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

type fakeEvaluationStore struct {
	record           evaluation.Record
	approval         evaluation.Approval
	createErr        error
	getErr           error
	approvalErr      error
	hold             bool
	markStopped      bool
	markFailed       bool
	lastCreateParams evaluation.CreateParams
}

func (s *fakeEvaluationStore) Create(_ context.Context, params evaluation.CreateParams) (evaluation.Record, bool, error) {
	s.lastCreateParams = params
	return s.record, true, s.createErr
}
func (s *fakeEvaluationStore) MarkNotebookStopped(_ context.Context, _ string) error {
	s.markStopped = true
	return nil
}
func (s *fakeEvaluationStore) MarkStopFailed(_ context.Context, _, _, _ string) error {
	s.markFailed = true
	return nil
}
func (s *fakeEvaluationStore) GetOwned(context.Context, string, string) (evaluation.Record, error) {
	return s.record, s.getErr
}
func (s *fakeEvaluationStore) HasActiveHold(context.Context, int64) (bool, error) {
	return s.hold, nil
}
func (s *fakeEvaluationStore) RequestApproval(context.Context, evaluation.ApprovalParams) (evaluation.Approval, bool, error) {
	return s.approval, true, s.approvalErr
}

func TestEvaluationRoutesAreIndependentOfBookingMode(t *testing.T) {
	for _, bookingsEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "bookings"}[bookingsEnabled], func(t *testing.T) {
			app := testBookingModeApp(bookingsEnabled)
			app.env.EvaluationConfig.Enabled = true
			app.evaluationStore = &fakeEvaluationStore{}
			rec := authenticatedRouterRequest(t, app, http.MethodPost,
				"/v1/notebooks/demo-notebook/evaluations", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected routed request to validate Idempotency-Key, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSubmitEvaluationStopsNotebookAndQueuesPVCWait(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	notebook := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubeflow.org/v1beta1", "kind": "Notebook",
		"metadata": map[string]any{"name": "demo-notebook", "namespace": userID},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), notebook)
	store := &fakeEvaluationStore{record: evaluation.Record{
		ID: "10000000-0000-0000-0000-000000000001", NotebookID: 42,
		UserID: userID, NotebookName: "demo-notebook", Namespace: userID,
		SourcePVC: "demo-notebook-pvc", Status: evaluation.StatusStopping,
	}}
	app := &application{
		env: ApiEnv{EvaluationConfig: EvaluationConfig{Enabled: true,
			SourceNotebookPath: "nha_ps4_evaluation_template.ipynb"}},
		k8sClient: &k8s.K8sClient{Dynamic: client}, evaluationStore: store,
	}
	req := requestWithUserContext(http.MethodPost, "/v1/notebooks/demo-notebook/evaluations", nil)
	req.SetPathValue("notebook_name", "demo-notebook")
	req.Header.Set("Idempotency-Key", "submit-1")
	rec := httptest.NewRecorder()
	app.submitEvaluation(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !store.markStopped || store.markFailed {
		t.Fatalf("unexpected store transition: stopped=%v failed=%v", store.markStopped, store.markFailed)
	}
	if store.lastCreateParams.RequireActiveBooking {
		t.Fatal("direct mode must not require a linked active booking")
	}
	gvr := schema.GroupVersionResource{
		Group: "kubeflow.org", Version: "v1beta1", Resource: "notebooks"}
	updated, err := client.Resource(gvr).Namespace(userID).
		Get(context.Background(), "demo-notebook", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetAnnotations()["kubeflow-resource-stopped"] == "" {
		t.Fatal("notebook stop annotation was not applied")
	}
}

func TestListEvaluationOutputsDoesNotExposeOtherOwners(t *testing.T) {
	app := &application{env: ApiEnv{EvaluationConfig: EvaluationConfig{Enabled: true}},
		evaluationStore: &fakeEvaluationStore{getErr: evaluation.ErrEvaluationNotFound}}
	req := requestWithUserContext(http.MethodGet, "/v1/evaluations/10000000-0000-0000-0000-000000000001/outputs", nil)
	req.SetPathValue("evaluation_id", "10000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	app.listEvaluationOutputs(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected non-disclosing 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListEvaluationOutputsReturnsCompletedManifest(t *testing.T) {
	manifest, _ := json.Marshal(map[string]any{"files": []map[string]any{{
		"path": "result.csv", "size": 12, "mediaType": "text/csv",
		"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}})
	app := &application{env: ApiEnv{EvaluationConfig: EvaluationConfig{
		Enabled: true, MaxManifestBytes: 262144, MaxManifestFiles: 1000,
	}},
		evaluationStore: &fakeEvaluationStore{record: evaluation.Record{
			ID: "10000000-0000-0000-0000-000000000001", Status: evaluation.StatusSucceeded,
			OutputManifest: manifest,
		}}}
	req := requestWithUserContext(http.MethodGet, "/v1/evaluations/10000000-0000-0000-0000-000000000001/outputs", nil)
	req.SetPathValue("evaluation_id", "10000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	app.listEvaluationOutputs(rec, req)
	if rec.Code != http.StatusOK || !containsBody(rec.Body.String(), "result.csv") {
		t.Fatalf("expected manifest output, got %d: %s", rec.Code, rec.Body.String())
	}
}

func containsBody(body, value string) bool {
	for i := 0; i+len(value) <= len(body); i++ {
		if body[i:i+len(value)] == value {
			return true
		}
	}
	return false
}

func TestConfiguredApproverRoles(t *testing.T) {
	if !hasConfiguredApproverRole([]string{"sandbox-approver"}, "admin, sandbox-approver") {
		t.Fatal("configured role should permit approval")
	}
	if hasConfiguredApproverRole([]string{"user"}, "sandbox-approver") {
		t.Fatal("unconfigured role should not permit approval")
	}
	if !hasConfiguredApproverRole(nil, "") {
		t.Fatal("empty role policy should permit the owning user")
	}
}

func TestApproveEvaluationRequiresSharedWorkspace(t *testing.T) {
	app := &application{
		env:             ApiEnv{EvaluationConfig: EvaluationConfig{Enabled: true}, EvaluationWorkspaceConfig: EvaluationWorkspaceConfig{Enabled: false}},
		evaluationStore: &fakeEvaluationStore{},
	}
	req := requestWithUserContext(http.MethodPost,
		"/v1/evaluations/10000000-0000-0000-0000-000000000001/approve", nil)
	req.SetPathValue("evaluation_id", "10000000-0000-0000-0000-000000000001")
	req.Header.Set("Idempotency-Key", "approve-1")
	rec := httptest.NewRecorder()
	app.approveEvaluation(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected disabled evaluation workspace to return 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitEvaluationRejectsNotebookThatIsNotReady(t *testing.T) {
	app := &application{
		env:             ApiEnv{BookingsEnabled: true, EvaluationConfig: EvaluationConfig{Enabled: true, SourceNotebookPath: "nha_ps4_evaluation_template.ipynb"}},
		evaluationStore: &fakeEvaluationStore{createErr: evaluation.ErrNotebookNotReady},
	}
	req := requestWithUserContext(http.MethodPost, "/v1/notebooks/demo-notebook/evaluations", nil)
	req.SetPathValue("notebook_name", "demo-notebook")
	req.Header.Set("Idempotency-Key", "submit-not-ready")
	rec := httptest.NewRecorder()
	app.submitEvaluation(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !app.evaluationStore.(*fakeEvaluationStore).lastCreateParams.RequireActiveBooking {
		t.Fatal("booking mode must require a linked active booking")
	}
}
