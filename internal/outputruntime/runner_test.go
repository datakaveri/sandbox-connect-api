package outputruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func preparedRunner(t *testing.T) Runner {
	t.Helper()
	source := filepath.Join(t.TempDir(), "input.ipynb")
	if err := os.WriteFile(source, []byte(`{"nbformat":4,"cells":[],"metadata":{"language_info":{"name":"python"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := Runner{Workspace: filepath.Join(t.TempDir(), "scratch")}
	if err := r.Prepare(source, "run-123"); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPrepareRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.ipynb")
	os.WriteFile(target, []byte(`{}`), 0600)
	link := filepath.Join(root, "link.ipynb")
	os.Symlink(target, link)
	if err := (Runner{Workspace: t.TempDir()}).Prepare(link, "run"); err == nil {
		t.Fatal("accepted source symlink")
	}
	os.Symlink(root, filepath.Join(root, "dir"))
	if _, err := ReadBounded(filepath.Join(root, "dir", "real.ipynb"), 1024); err == nil {
		t.Fatal("accepted parent symlink")
	}
}
func TestConfigureExactReplacements(t *testing.T) {
	for _, tc := range []struct {
		name, mapping string
		fail          bool
	}{
		{"valid", `{"version":"v1","required":{"SANDBOX_URL":"PROD_URL"},"optional":{"ABSENT":"x"}}`, false},
		{"missing", `{"version":"v1","required":{"MISSING":"x"}}`, true},
		{"overlap", `{"version":"v1","required":{"SANDBOX_URL":"x","SANDBOX":"y"}}`, true},
		{"unresolved", `{"version":"v1","required":{"SANDBOX_URL":"SANDBOX_URL"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := preparedRunner(t)
			WriteFile(filepath.Join(r.Workspace, "notebook.py"), []byte("url = 'SANDBOX_URL'\n"))
			m := filepath.Join(t.TempDir(), "map.json")
			os.WriteFile(m, []byte(tc.mapping), 0600)
			err := r.Configure(m)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v", err)
			}
			if !tc.fail {
				b, _ := ReadBounded(filepath.Join(r.Workspace, "notebook.py"), 1024)
				if string(b) != "url = 'PROD_URL'\n" {
					t.Fatalf("script=%s", b)
				}
			}
		})
	}
}
func TestExecuteProducesCSVAndBoundsLogs(t *testing.T) {
	r := preparedRunner(t)
	t.Setenv("PRODUCTION_EXAMPLE", "approved")
	t.Setenv("FILE_SERVICE_TOKEN", "must-not-reach-script")
	code := "import os\nassert os.environ['PRODUCTION_EXAMPLE'] == 'approved'\nassert 'FILE_SERVICE_TOKEN' not in os.environ\nopen('result.csv','w').write('name,value\\na,1\\n')\nprint('x' * 2000000)\n"
	if err := WriteFile(filepath.Join(r.Workspace, "notebook.py"), []byte(code)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Execute(ctx, filepath.Join(r.Workspace, "output")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(r.Workspace, "execute.log"))
	if err != nil || info.Size() != MaxLogBytes {
		t.Fatalf("log size: %v %v", info, err)
	}
	b, err := os.ReadFile(filepath.Join(r.Workspace, "output", "result.csv"))
	if err != nil || !strings.Contains(string(b), "a,1") {
		t.Fatalf("CSV: %s %v", b, err)
	}
	s, err := r.loadSummary()
	if err != nil || !s.Succeeded || s.Stage != "execute" || len(s.SourceSHA256) != 64 {
		t.Fatalf("summary: %+v %v", s, err)
	}
}
func TestExecuteTimeoutAndSummaryTampering(t *testing.T) {
	for _, code := range []string{"import time; time.sleep(30)", "import os; os.unlink('../execution-summary.json'); os.symlink('/etc/passwd','../execution-summary.json')"} {
		r := preparedRunner(t)
		WriteFile(filepath.Join(r.Workspace, "notebook.py"), []byte(code))
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err := r.Execute(ctx, filepath.Join(r.Workspace, "output"))
		cancel()
		if err == nil {
			t.Fatal("expected timeout or unsafe summary failure")
		}
	}
}
func TestPrepareChecksumAndNoSupportFiles(t *testing.T) {
	r := preparedRunner(t)
	b, err := ReadBounded(filepath.Join(r.Workspace, "execution-summary.json"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	if err := json.Unmarshal(b, &summary); err != nil || len(summary.SourceSHA256) != 64 {
		t.Fatal("missing checksum")
	}
}

// Enable with OUTPUT_NBCONVERT_TEST=1 and a PATH containing the pinned Python environment.
func TestRealNotebookConversion(t *testing.T) {
	if os.Getenv("OUTPUT_NBCONVERT_TEST") != "1" {
		t.Skip("requires the runner Python dependencies")
	}
	r := preparedRunner(t)
	notebook := `{"nbformat":4,"nbformat_minor":5,"metadata":{"language_info":{"name":"python","file_extension":".py"}},"cells":[{"id":"output","cell_type":"code","metadata":{},"execution_count":null,"outputs":[],"source":["open('result.csv','w').write('name,value\\na,1\\n')"]}]}`
	if err := WriteFile(filepath.Join(r.Workspace, "notebook.ipynb"), []byte(notebook)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.Convert(ctx); err != nil {
		b, _ := os.ReadFile(filepath.Join(r.Workspace, "convert.log"))
		t.Fatalf("convert: %v: %s", err, b)
	}
	mapping := filepath.Join(t.TempDir(), "map.json")
	os.WriteFile(mapping, []byte(`{"version":"v1","required":{},"optional":{}}`), 0600)
	if err := r.Configure(mapping); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(ctx, filepath.Join(r.Workspace, "output")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.Workspace, "output", "result.csv"))
	if err != nil || string(b) != "name,value\na,1\n" {
		t.Fatalf("output: %s %v", b, err)
	}
}
