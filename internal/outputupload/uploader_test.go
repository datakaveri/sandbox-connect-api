package outputupload

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
)

func csvWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "output", "result.csv"), []byte("name,value\na,1\n"), 0600)
	return dir
}
func TestInventoryRejectsInvalidDeliverables(t *testing.T) {
	for _, kind := range []string{"symlink", "parent-symlink", "directory", "non-csv", "invalid-csv", "binary", "empty", "size", "count", "total", "manifest", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			w := csvWorkspace(t)
			dir := filepath.Join(w, "output")
			file := filepath.Join(dir, "result.csv")
			limits := output.Limits{}.WithDefaults()
			switch kind {
			case "symlink":
				os.Remove(file)
				os.Symlink("/etc/passwd", file)
			case "parent-symlink":
				os.Rename(dir, filepath.Join(w, "real"))
				os.Symlink(filepath.Join(w, "real"), dir)
			case "hardlink":
				os.Link(file, filepath.Join(dir, "linked.csv"))
			case "directory":
				os.Mkdir(filepath.Join(dir, "nested"), 0700)
			case "non-csv":
				os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0600)
			case "invalid-csv":
				os.WriteFile(file, []byte("a,b\n1\n"), 0600)
			case "binary":
				os.WriteFile(file, []byte("a,b\n\x00,1\n"), 0600)
			case "empty":
				os.WriteFile(file, nil, 0600)
			case "size":
				limits.MaxFileBytes = 2
			case "count":
				limits.MaxManifestFiles = 1
				os.WriteFile(filepath.Join(dir, "other.csv"), []byte("a\n"), 0600)
			case "total":
				limits.MaxFileBytes = 15
				limits.MaxOutputBytes = 15
				os.WriteFile(filepath.Join(dir, "other.csv"), []byte("name,value\na,1\n"), 0600)
			case "manifest":
				limits.MaxManifestBytes = 5
			}
			if _, err := Inventory(dir, "review/run/", limits); err == nil {
				t.Fatal("accepted invalid output")
			}
		})
	}
}
func TestFilesConnectUploadCompleteAndPublish(t *testing.T) {
	for _, scenario := range []string{"success", "put-failure", "redirect", "bad-completion", "credential-header", "wrong-target", "change-after-inventory"} {
		t.Run(scenario, func(t *testing.T) {
			w := csvWorkspace(t)
			prefix := "review/run/"
			manifestPath := filepath.Join(w, "manifest.json")
			var expected output.Manifest
			var uploaded []byte
			complete := false
			storage := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("service credentials reached storage")
				}
				if r.Method != "PUT" {
					t.Error("expected PUT")
				}
				uploaded, _ = io.ReadAll(r.Body)
				switch scenario {
				case "put-failure":
					rw.WriteHeader(500)
				case "redirect":
					rw.Header().Set("Location", "https://example.invalid")
					rw.WriteHeader(307)
				default:
					rw.WriteHeader(200)
				}
			}))
			defer storage.Close()
			service := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer service-secret" {
					t.Error("missing service authentication")
				}
				rw.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/outputs/run/uploads":
					var body struct {
						Files []output.ManifestFile `json:"files"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					expected = output.Manifest{Files: body.Files}
					target := filesconnect.UploadTarget{FileID: body.Files[0].FileID, URL: storage.URL + "/object?signature=private"}
					if scenario == "credential-header" {
						target.Headers = map[string]string{"Authorization": "Bearer service-secret"}
					}
					if scenario == "wrong-target" {
						target.FileID = "wrong"
					}
					if scenario == "change-after-inventory" {
						os.WriteFile(filepath.Join(w, "output", "result.csv"), []byte("name,value\nb,2\n"), 0600)
					}
					json.NewEncoder(rw).Encode(map[string]any{"success": true, "data": map[string]any{"uploads": []filesconnect.UploadTarget{target}}})
				case "/v1/outputs/run/complete":
					complete = true
					if scenario == "bad-completion" {
						expected.Files[0].SHA256 = strings.Repeat("a", 64)
					}
					json.NewEncoder(rw).Encode(map[string]any{"success": true, "data": map[string]any{"manifest": expected, "reviewManifestKey": prefix + "manifest.json"}})
				case "/v1/outputs/run/publish":
					json.NewEncoder(rw).Encode(map[string]any{"success": true, "data": map[string]any{"workspacePrefix": "workspace/run/", "workspaceManifestKey": "workspace/run/manifest.json", "approvedFileIds": []string{expected.Files[0].FileID}}})
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					rw.WriteHeader(404)
				}
			}))
			defer service.Close()
			client, err := filesconnect.NewClient(filesconnect.Config{BaseURL: service.URL + "/v1", ServiceToken: "service-secret", ReviewDatabankID: "review", WorkspaceDatabankID: "workspace", Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var uploadLogs strings.Builder
			err = (Uploader{Service: client, HTTP: storage.Client(), LogOutput: &uploadLogs}).Run(context.Background(), w, "run", prefix, manifestPath)
			if strings.Contains(uploadLogs.String(), "service-secret") || strings.Contains(uploadLogs.String(), "signature=private") {
				t.Fatal("upload logs exposed credentials or a signed URL")
			}
			if scenario != "success" {
				if err == nil {
					t.Fatal("expected rejection")
				}
				if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
					t.Fatal("failed upload wrote manifest")
				}
				if complete && scenario != "bad-completion" {
					t.Fatal("completed failed upload")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(uploadLogs.String(), "[upload] Uploading result.csv") || !strings.Contains(uploadLogs.String(), "[upload] Manifest verified") {
				t.Fatalf("missing detailed upload logs: %s", uploadLogs.String())
			}
			if !complete || string(uploaded) != "name,value\na,1\n" {
				t.Fatal("upload did not complete")
			}
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Publish(context.Background(), output.ApprovalWork{Output: output.Record{ID: "run", UserID: "user", ReviewPrefix: prefix, ReviewManifestKey: prefix + "manifest.json", OutputManifest: raw}, Approval: output.Approval{WorkspacePrefix: "workspace/run/"}})
			if err != nil || len(result.ApprovedFileIDs) != 1 {
				t.Fatalf("publish: %+v %v", result, err)
			}
		})
	}
}
