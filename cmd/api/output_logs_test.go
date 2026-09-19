package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sandbox-backend-service/internal/output"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type fakeOutputLogSource struct {
	pods       []corev1.Pod
	logs       map[string]string
	streamed   []string
	lastOpts   corev1.PodLogOptions
	lastNS     string
	selector   string
	listCalled bool
}

func (f *fakeOutputLogSource) ListPods(_ context.Context, namespace, selector string) ([]corev1.Pod, error) {
	f.listCalled = true
	f.lastNS = namespace
	f.selector = selector
	return f.pods, nil
}

func (f *fakeOutputLogSource) StreamPodLogs(
	_ context.Context,
	namespace string,
	podName string,
	options corev1.PodLogOptions,
) (io.ReadCloser, error) {
	f.lastNS = namespace
	f.streamed = append(f.streamed, podName)
	f.lastOpts = options
	return io.NopCloser(strings.NewReader(f.logs[podName])), nil
}

func TestStreamOutputLogsStreamsOnlyOwnedWorkflowPods(t *testing.T) {
	const (
		outputID     = "10000000-0000-0000-0000-000000000001"
		userID       = "00000000-0000-0000-0000-000000000001"
		namespace    = "user-namespace"
		workflowName = "output-10000000000000000000"
		workflowUID  = "workflow-uid"
	)
	trusted := outputLogTestPod("execute-pod", "execute", workflowName, workflowUID)
	spoofed := outputLogTestPod("spoofed-pod", "prepare", workflowName, "different-uid")
	unknownStage := outputLogTestPod("unknown-pod", "cleanup", workflowName, workflowUID)
	source := &fakeOutputLogSource{
		pods: []corev1.Pod{spoofed, unknownStage, trusted},
		logs: map[string]string{
			"execute-pod": "2026-09-19T07:00:01.123456789Z created result.csv\n",
		},
	}
	workflowNameValue, workflowUIDValue := workflowName, workflowUID
	app := &application{
		env: ApiEnv{OutputConfig: OutputConfig{Enabled: true}},
		outputStore: &fakeOutputStore{record: output.Record{
			ID: outputID, UserID: userID, Namespace: namespace,
			Status:       output.StatusPendingApproval,
			WorkflowName: &workflowNameValue, WorkflowUID: &workflowUIDValue,
		}},
		outputLogs: source,
	}
	req := requestWithIdentity(http.MethodGet,
		"/v1/outputs/"+outputID+"/logs?tailLines=25",
		UserInfo{Sub: userID, ExpiresAt: time.Now().Add(time.Minute)})
	req.SetPathValue("output_id", outputID)
	rec := httptest.NewRecorder()

	app.streamOutputLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("unexpected content type %q", contentType)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"event: status",
		`"status":"pending_approval"`,
		"event: log",
		`"stage":"execute"`,
		`"timestamp":"2026-09-19T07:00:01.123456789Z"`,
		`"message":"created result.csv"`,
		"event: complete",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body does not contain %q:\n%s", expected, body)
		}
	}
	if len(source.streamed) != 1 || source.streamed[0] != "execute-pod" {
		t.Fatalf("streamed untrusted pods: %#v", source.streamed)
	}
	if source.lastNS != namespace {
		t.Fatalf("namespace=%q, want %q", source.lastNS, namespace)
	}
	if source.selector != "workflows.argoproj.io/workflow="+workflowName {
		t.Fatalf("unexpected selector %q", source.selector)
	}
	if source.lastOpts.Container != "main" || !source.lastOpts.Follow || !source.lastOpts.Timestamps {
		t.Fatalf("unexpected pod log options: %#v", source.lastOpts)
	}
	if source.lastOpts.TailLines == nil || *source.lastOpts.TailLines != 25 {
		t.Fatalf("unexpected tail lines: %#v", source.lastOpts.TailLines)
	}
}

func TestStreamOutputLogsReportsUnavailableAfterPodCleanup(t *testing.T) {
	const outputID = "10000000-0000-0000-0000-000000000001"
	workflowName, workflowUID := "output-10000000000000000000", "workflow-uid"
	app := &application{
		env: ApiEnv{OutputConfig: OutputConfig{Enabled: true}},
		outputStore: &fakeOutputStore{record: output.Record{
			ID: outputID, Namespace: "user-namespace", Status: output.StatusFailed,
			WorkflowName: &workflowName, WorkflowUID: &workflowUID,
		}},
		outputLogs: &fakeOutputLogSource{},
	}
	req := requestWithIdentity(http.MethodGet, "/v1/outputs/"+outputID+"/logs",
		UserInfo{Sub: "00000000-0000-0000-0000-000000000001", ExpiresAt: time.Now().Add(time.Minute)})
	req.SetPathValue("output_id", outputID)
	rec := httptest.NewRecorder()

	app.streamOutputLogs(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: unavailable") || !strings.Contains(body, "event: complete") {
		t.Fatalf("expected unavailable and complete events, got:\n%s", body)
	}
}

func TestStreamOutputLogsValidatesBeforeStartingStream(t *testing.T) {
	const outputID = "10000000-0000-0000-0000-000000000001"
	tests := []struct {
		name     string
		target   string
		store    *fakeOutputStore
		wantCode int
	}{
		{
			name: "invalid tail lines", target: "/v1/outputs/" + outputID + "/logs?tailLines=1001",
			store: &fakeOutputStore{}, wantCode: http.StatusBadRequest,
		},
		{
			name: "not owned", target: "/v1/outputs/" + outputID + "/logs",
			store: &fakeOutputStore{getErr: output.ErrOutputNotFound}, wantCode: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeOutputLogSource{}
			app := &application{
				env:         ApiEnv{OutputConfig: OutputConfig{Enabled: true}},
				outputStore: test.store, outputLogs: source,
			}
			req := requestWithIdentity(http.MethodGet, test.target,
				UserInfo{Sub: "00000000-0000-0000-0000-000000000001", ExpiresAt: time.Now().Add(time.Minute)})
			req.SetPathValue("output_id", outputID)
			rec := httptest.NewRecorder()

			app.streamOutputLogs(rec, req)

			if rec.Code != test.wantCode {
				t.Fatalf("expected %d, got %d: %s", test.wantCode, rec.Code, rec.Body.String())
			}
			if source.listCalled {
				t.Fatal("Kubernetes must not be queried when request validation fails")
			}
		})
	}
}

func TestContextTimeoutExemptsOnlyOutputLogGET(t *testing.T) {
	app := &application{env: ApiEnv{TimeoutInSecs: 1}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline := r.Context().Deadline()
		if hasDeadline {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := app.contextTimeout(next)

	logRequest := httptest.NewRequest(http.MethodGet,
		"/v1/outputs/10000000-0000-0000-0000-000000000001/logs", nil)
	logResponse := httptest.NewRecorder()
	handler.ServeHTTP(logResponse, logRequest)
	if logResponse.Code != http.StatusNoContent {
		t.Fatalf("log stream should not receive API timeout, got %d", logResponse.Code)
	}

	statusRequest := httptest.NewRequest(http.MethodGet,
		"/v1/outputs/10000000-0000-0000-0000-000000000001", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusAccepted {
		t.Fatalf("normal API request should receive timeout, got %d", statusResponse.Code)
	}
}

func outputLogTestPod(name, stage, workflowName, workflowUID string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"workflows.argoproj.io/workflow": workflowName,
			"output-stage":                   stage,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Workflow",
			Name:       workflowName,
			UID:        types.UID(workflowUID),
		}},
	}}
}
