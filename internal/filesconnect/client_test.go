package filesconnect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sandbox-backend-service/internal/output"
)

func TestListWorkspaceDerivesOwnerPrefixAndHidesObjectKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-token" {
			t.Fatal("missing service authorization")
		}
		if r.Method != http.MethodGet || r.URL.Path != "/outputs/internal/workspaces/user-1/files" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Fatalf("GET request unexpectedly had a body: %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"files":[{"fileId":"` + strings.Repeat("a", 64) + `","name":"run-1/result.csv","size":12,"lastModified":"2026-09-07T00:00:00Z","contentType":"text/csv"}]}}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	files, err := client.ListWorkspace(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != strings.Repeat("a", 64) || files[0].Name != "run-1/result.csv" {
		t.Fatalf("unexpected files: %#v", files)
	}
	encoded, _ := json.Marshal(files)
	if strings.Contains(string(encoded), "user-workspaces/") {
		t.Fatalf("object key leaked in API representation: %s", encoded)
	}
}

func TestInternalPreviewAndDownloadRoutesUseOpaqueIdentities(t *testing.T) {
	fileID := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Fatalf("unexpected authentication or method")
		}
		switch r.URL.Path {
		case "/outputs/internal/workspaces/user-1/files/" + fileID + "/preview",
			"/outputs/internal/review/output-1/files/" + fileID + "/preview":
			_, _ = w.Write([]byte(`{"success":true,"data":{"content":[["a"],["1"]],"format":"csv","truncated":false}}`))
		case "/outputs/internal/workspaces/user-1/files/" + fileID + "/download":
			_, _ = w.Write([]byte(`{"success":true,"data":{"url":"https://download.example/file","expiresAt":"2026-09-07T00:05:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	preview, err := client.PreviewWorkspaceFile(context.Background(), "user-1", fileID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Format != "csv" {
		t.Fatalf("unexpected workspace preview=%#v", preview)
	}
	preview, err = client.PreviewReviewFile(context.Background(), "output-1", fileID)
	if err != nil || preview.Format != "csv" {
		t.Fatalf("unexpected review preview=%#v err=%v", preview, err)
	}
	download, err := client.DownloadWorkspaceFile(context.Background(), "user-1", fileID)
	if err != nil || download.URL != "https://download.example/file" {
		t.Fatalf("unexpected download=%#v err=%v", download, err)
	}
}

func TestPublishSendsValidatedCSVManifest(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/outputs/output-1/publish") {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"workspacePrefix":"user-workspaces/users/user-1/outputs/output-1/","workspaceManifestKey":"user-workspaces/users/user-1/outputs/output-1/manifest.json","approvedFileIds":["file-1"]}}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	manifest, _ := json.Marshal(output.Manifest{Files: []output.ManifestFile{{
		FileID: "file-1", Path: "result.csv",
		ObjectKey: "nha-review/users/user-1/sandboxes/n/outputs/output-1/result.csv",
		Size:      12, MediaType: "text/csv", SHA256: strings.Repeat("a", 64),
	}}})
	work := output.ApprovalWork{
		Output: output.Record{
			ID: "output-1", UserID: "user-1",
			ReviewPrefix:      "nha-review/users/user-1/sandboxes/n/outputs/output-1/",
			ReviewManifestKey: "nha-review/users/user-1/sandboxes/n/outputs/output-1/manifest.json",
			OutputManifest:    manifest,
		},
		Approval: output.Approval{
			WorkspacePrefix: "user-workspaces/users/user-1/outputs/output-1/",
		},
	}
	result, err := client.Publish(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceManifestKey == "" || request["ownerId"] != "user-1" {
		t.Fatalf("unexpected result=%#v request=%#v", result, request)
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL: baseURL, ServiceToken: "service-token",
		ReviewDatabankID: "review", WorkspaceDatabankID: "workspace",
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPublishUsesConfiguredLimits(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.invalid/v1", ServiceToken: "token", ReviewDatabankID: "review", WorkspaceDatabankID: "workspace", Timeout: time.Second, Limits: output.Limits{MaxManifestBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Publish(context.Background(), output.ApprovalWork{Output: output.Record{OutputManifest: json.RawMessage(`{"files":[]}`), ReviewPrefix: "review/run/"}})
	if err == nil || !strings.Contains(err.Error(), "exceeds 1 bytes") {
		t.Fatalf("expected configured manifest limit, got %v", err)
	}
}
