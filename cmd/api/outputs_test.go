package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
	"sandbox-backend-service/pkg/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

type fakeOutputStore struct {
	record             output.Record
	approval           output.Approval
	records            []output.Record
	createErr          error
	getErr             error
	approvalErr        error
	hold               bool
	markStopped        bool
	markFailed         bool
	approvalFound      bool
	lastCreateParams   output.CreateParams
	lastApprovalParams output.ApprovalParams
}

func (s *fakeOutputStore) Create(_ context.Context, params output.CreateParams) (output.Record, bool, error) {
	s.lastCreateParams = params
	return s.record, true, s.createErr
}
func (s *fakeOutputStore) MarkNotebookStopped(context.Context, string) error {
	s.markStopped = true
	return nil
}
func (s *fakeOutputStore) MarkStopFailed(context.Context, string, string, string) error {
	s.markFailed = true
	return nil
}
func (s *fakeOutputStore) GetOwned(context.Context, string, string) (output.Record, error) {
	return s.record, s.getErr
}
func (s *fakeOutputStore) GetByID(context.Context, string) (output.Record, error) {
	return s.record, s.getErr
}
func (s *fakeOutputStore) GetApproval(context.Context, string) (output.Approval, bool, error) {
	return s.approval, s.approvalFound, s.approvalErr
}
func (s *fakeOutputStore) ListPending(context.Context, int) ([]output.Record, error) {
	return s.records, nil
}
func (s *fakeOutputStore) HasActiveHold(context.Context, int64) (bool, error) {
	return s.hold, nil
}
func (s *fakeOutputStore) RequestApproval(_ context.Context, params output.ApprovalParams) (output.Approval, bool, error) {
	s.lastApprovalParams = params
	return s.approval, true, s.approvalErr
}

type fakeOutputFiles struct {
	previewKey string
	files      []filesconnect.File
}

func (f *fakeOutputFiles) ListWorkspace(context.Context, string) ([]filesconnect.File, error) {
	return f.files, nil
}
func (f *fakeOutputFiles) PreviewReviewFile(_ context.Context, key string) (filesconnect.Preview, error) {
	f.previewKey = key
	return filesconnect.Preview{Format: "csv", Content: []any{"value"}}, nil
}
func (f *fakeOutputFiles) PreviewWorkspaceFile(context.Context, string, string) (filesconnect.Preview, error) {
	return filesconnect.Preview{Format: "csv"}, nil
}
func (f *fakeOutputFiles) DownloadWorkspaceFile(context.Context, string, string) (filesconnect.Download, error) {
	return filesconnect.Download{URL: "https://download.example/file"}, nil
}

func TestOutputRoutesAreIndependentOfBookingMode(t *testing.T) {
	for _, bookingsEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "bookings"}[bookingsEnabled], func(t *testing.T) {
			app := testBookingModeApp(bookingsEnabled)
			app.env.OutputConfig.Enabled = true
			app.outputStore = &fakeOutputStore{}
			rec := authenticatedRouterRequest(t, app, http.MethodPost,
				"/v1/notebooks/demo-notebook/outputs", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected routed request to validate Idempotency-Key, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSubmitOutputStopsNotebookAndHonorsBookingMode(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	for _, bookingsEnabled := range []bool{false, true} {
		notebook := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kubeflow.org/v1beta1", "kind": "Notebook",
			"metadata": map[string]any{"name": "demo-notebook", "namespace": userID},
		}}
		client := fake.NewSimpleDynamicClient(runtime.NewScheme(), notebook)
		store := &fakeOutputStore{record: output.Record{
			ID: "10000000-0000-0000-0000-000000000001", NotebookID: 42,
			UserID: userID, NotebookName: "demo-notebook", Namespace: userID,
			SourcePVC: "demo-notebook-pvc", Status: output.StatusStopping,
		}}
		app := &application{
			env: ApiEnv{BookingsEnabled: bookingsEnabled, OutputConfig: OutputConfig{
				Enabled: true, SourceNotebookPath: "nha_ps4_output_template.ipynb",
			}},
			k8sClient: &k8s.K8sClient{Dynamic: client}, outputStore: store,
		}
		req := requestWithIdentity(http.MethodPost, "/v1/notebooks/demo-notebook/outputs",
			UserInfo{Sub: userID})
		req.SetPathValue("notebook_name", "demo-notebook")
		req.Header.Set("Idempotency-Key", "submit-1")
		rec := httptest.NewRecorder()
		app.submitOutput(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
		}
		if !store.markStopped || store.markFailed {
			t.Fatalf("unexpected store transition: stopped=%v failed=%v", store.markStopped, store.markFailed)
		}
		if store.lastCreateParams.RequireActiveBooking != bookingsEnabled {
			t.Fatalf("booking requirement=%v, want %v", store.lastCreateParams.RequireActiveBooking, bookingsEnabled)
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
}

func TestGetOutputUsesOwnerScopedLookupAndDoesNotExposeManifest(t *testing.T) {
	manifest := validOutputManifest(t)
	app := &application{env: ApiEnv{OutputConfig: OutputConfig{Enabled: true}},
		outputStore: &fakeOutputStore{record: output.Record{
			ID:     "10000000-0000-0000-0000-000000000001",
			Status: output.StatusPendingApproval, OutputManifest: manifest,
		}}}
	req := requestWithIdentity(http.MethodGet,
		"/v1/outputs/10000000-0000-0000-0000-000000000001",
		UserInfo{Sub: "00000000-0000-0000-0000-000000000001"})
	req.SetPathValue("output_id", "10000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	app.getOutput(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "objectKey") {
		t.Fatalf("unexpected owner response %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminPreviewResolvesFileIDFromValidatedManifest(t *testing.T) {
	files := &fakeOutputFiles{}
	app := adminOutputApp(&fakeOutputStore{record: output.Record{
		ID:             "10000000-0000-0000-0000-000000000001",
		Status:         output.StatusPendingApproval,
		ReviewPrefix:   "nha-review/users/u/sandboxes/n/outputs/o/",
		OutputManifest: validOutputManifest(t),
	}}, files)
	req := requestWithIdentity(http.MethodGet, "/preview",
		UserInfo{Sub: "20000000-0000-0000-0000-000000000001", Roles: []string{"cos_admin"}})
	req.SetPathValue("output_id", "10000000-0000-0000-0000-000000000001")
	req.SetPathValue("file_id", "file-1")
	rec := httptest.NewRecorder()
	app.previewAdminOutputFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if files.previewKey != "nha-review/users/u/sandboxes/n/outputs/o/result.csv" {
		t.Fatalf("unexpected review key: %q", files.previewKey)
	}
}

func TestApproveOutputIsAdminOnlyAndNotOwnerScoped(t *testing.T) {
	store := &fakeOutputStore{approval: output.Approval{
		OutputID: "10000000-0000-0000-0000-000000000001",
		Status:   output.ApprovalRequested,
	}}
	app := adminOutputApp(store, &fakeOutputFiles{})
	req := requestWithIdentity(http.MethodPost, "/approve",
		UserInfo{Sub: "20000000-0000-0000-0000-000000000001", Roles: []string{"cos_admin"}})
	req.SetPathValue("output_id", "10000000-0000-0000-0000-000000000001")
	req.Header.Set("Idempotency-Key", "approve-1")
	rec := httptest.NewRecorder()
	app.approveOutput(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.lastApprovalParams.RequestedByAdmin != "20000000-0000-0000-0000-000000000001" {
		t.Fatalf("approval admin not recorded: %#v", store.lastApprovalParams)
	}

	req = requestWithIdentity(http.MethodPost, "/approve",
		UserInfo{Sub: "30000000-0000-0000-0000-000000000001", Roles: []string{"user"}})
	rec = httptest.NewRecorder()
	app.approveOutput(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec.Code)
	}
}

func TestEmptyAdminRoleConfigurationDeniesApproval(t *testing.T) {
	if hasConfiguredApproverRole([]string{"cos_admin"}, "") {
		t.Fatal("empty admin role configuration must deny approval")
	}
}

func adminOutputApp(store *fakeOutputStore, files *fakeOutputFiles) *application {
	return &application{
		env: ApiEnv{OutputConfig: OutputConfig{
			Enabled: true, ApproverRoles: "cos_admin",
			MaxManifestBytes: 262144, MaxManifestFiles: 1000, MaxFileBytes: 1024, MaxOutputBytes: 1024,
		}},
		outputStore: store, outputFiles: files,
	}
}

func validOutputManifest(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(output.Manifest{Files: []output.ManifestFile{{
		FileID: "file-1", Path: "result.csv",
		ObjectKey: "nha-review/users/u/sandboxes/n/outputs/o/result.csv",
		Size:      12, MediaType: "text/csv", SHA256: strings.Repeat("a", 64),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requestWithIdentity(method, target string, user UserInfo) *http.Request {
	req := requestWithUserContext(method, target, nil)
	return req.WithContext(context.WithValue(req.Context(), UserContextKey, user))
}
