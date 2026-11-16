package outputruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const MaxSourceBytes = int64(32 * 1024 * 1024)
const MaxLogBytes = int64(5 * 1024 * 1024)
const MaxAllowedLogBytes = int64(32 * 1024 * 1024)

type Summary struct {
	OutputID       string `json:"outputId"`
	SourceSHA256   string `json:"sourceSha256"`
	MappingVersion string `json:"mappingVersion,omitempty"`
	Stage          string `json:"stage"`
	Succeeded      bool   `json:"succeeded"`
}

type Runner struct {
	Workspace    string
	StreamOutput io.Writer
	MaxLogBytes  int64
}

func (r Runner) Prepare(source, outputID string) error {
	started := time.Now()
	r.logf("[prepare] Validating source notebook %s", filepath.Base(source))
	if outputID == "" {
		return fmt.Errorf("output ID is required")
	}
	if !strings.EqualFold(filepath.Ext(source), ".ipynb") {
		return fmt.Errorf("source must be a notebook")
	}
	data, err := ReadBounded(source, MaxSourceBytes)
	if err != nil {
		return err
	}
	var nb struct {
		NBFormat int               `json:"nbformat"`
		Cells    []json.RawMessage `json:"cells"`
		Metadata struct {
			LanguageInfo struct {
				Name string `json:"name"`
			} `json:"language_info"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &nb) != nil || nb.NBFormat != 4 || nb.Cells == nil || nb.Metadata.LanguageInfo.Name != "python" {
		return fmt.Errorf("expected a v4 Python notebook")
	}
	r.logf("[prepare] Validated Python notebook: %d bytes, %d cells", len(data), len(nb.Cells))
	r.logf("[prepare] Creating isolated workspace directories")
	for _, name := range []string{"", "home", "tmp", "output"} {
		if err := os.MkdirAll(filepath.Join(r.Workspace, name), 0700); err != nil {
			return err
		}
	}
	if err := WriteFile(filepath.Join(r.Workspace, "notebook.ipynb"), data); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if err := r.summary(Summary{OutputID: outputID, SourceSHA256: hex.EncodeToString(sum[:]), Stage: "prepare", Succeeded: true}); err != nil {
		return err
	}
	r.logf("[prepare] Source notebook copied and checksum recorded")
	r.logf("[prepare] Completed in %s", elapsed(started))
	return nil
}

func (r Runner) Convert(ctx context.Context) error {
	// Disable user configuration and Python import lookup in participant-controlled directories.
	return r.run(ctx, "convert", "jupyter", []string{"nbconvert", "--to", "script", "--output", "notebook", "--output-dir", r.Workspace, filepath.Join(r.Workspace, "notebook.ipynb")}, false)
}

func (r Runner) Configure(mapPath string) error {
	started := time.Now()
	r.logf("[configure] Loading approved replacement map")
	// ConfigMap projected files are symlinks owned by Kubernetes, so read this trusted input normally.
	f, err := os.Open(mapPath)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return fmt.Errorf("invalid replacement map size")
	}
	var mapping struct {
		Version  string            `json:"version"`
		Required map[string]string `json:"required"`
		Optional map[string]string `json:"optional"`
	}
	if err := json.Unmarshal(data, &mapping); err != nil || mapping.Version == "" {
		return fmt.Errorf("invalid replacement map")
	}
	r.logf("[configure] Loaded map version %s with %d required and %d optional replacements", mapping.Version, len(mapping.Required), len(mapping.Optional))
	script, err := ReadBounded(filepath.Join(r.Workspace, "notebook.py"), MaxSourceBytes)
	if err != nil {
		return err
	}
	original := string(script)
	all := map[string]string{}
	for key, value := range mapping.Required {
		if key == "" || !strings.Contains(original, key) {
			return fmt.Errorf("required replacement is missing")
		}
		all[key] = value
	}
	for key, value := range mapping.Optional {
		if key == "" {
			return fmt.Errorf("empty replacement is forbidden")
		}
		if _, exists := all[key]; exists {
			return fmt.Errorf("duplicate replacement")
		}
		all[key] = value
	}
	keys := make([]string, 0, len(all))
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys)*2)
	for i, key := range keys {
		for j, other := range keys {
			if i != j && strings.Contains(key, other) {
				return fmt.Errorf("overlapping replacements are forbidden")
			}
		}
		pairs = append(pairs, key, all[key])
	}
	// Bound replacement expansion before allocating the resulting script.
	size := int64(len(original))
	for _, key := range keys {
		growth := int64(len(all[key])-len(key)) * int64(strings.Count(original, key))
		if growth > 0 && growth > MaxSourceBytes-size {
			return fmt.Errorf("configured script exceeds size limit")
		}
		if growth > 0 {
			size += growth
		}
	}
	result := strings.NewReplacer(pairs...).Replace(original)
	for key := range mapping.Required {
		if strings.Contains(result, key) {
			return fmt.Errorf("required replacement remains unresolved")
		}
	}
	if int64(len(result)) > MaxSourceBytes {
		return fmt.Errorf("configured script exceeds size limit")
	}
	if err := WriteFile(filepath.Join(r.Workspace, "notebook.py"), []byte(result)); err != nil {
		return err
	}
	r.logf("[configure] Applied %d approved replacement rules", len(keys))
	r.logf("[configure] Configured script size: %d bytes", len(result))
	summary, err := r.loadSummary()
	if err != nil {
		return err
	}
	summary.MappingVersion = mapping.Version
	summary.Stage = "configure"
	summary.Succeeded = true
	if err := r.summary(summary); err != nil {
		return err
	}
	r.logf("[configure] Completed in %s", elapsed(started))
	return nil
}

func (r Runner) Execute(ctx context.Context, outputDir string) error {
	if filepath.Clean(outputDir) != filepath.Join(r.Workspace, "output") {
		return fmt.Errorf("output directory must be workspace/output")
	}
	if _, err := ReadBounded(filepath.Join(r.Workspace, "notebook.py"), MaxSourceBytes); err != nil {
		return err
	}
	if tokenFile := strings.TrimSpace(os.Getenv("NHA_TOKEN_FILE")); tokenFile != "" {
		r.logf("[execute] Waiting for delegated platform token")
		if err := waitForTokenFile(ctx, tokenFile); err != nil {
			return fmt.Errorf("delegated platform token unavailable: %w", err)
		}
		r.logf("[execute] Delegated platform token is ready")
	}
	if err := r.run(ctx, "execute", "python3", []string{"-I", "-u", filepath.Join(r.Workspace, "notebook.py")}, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return err
	}
	r.logf("[execute] Produced %d output entries", len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			r.logf("[execute] Output file %s: %d bytes", entry.Name(), info.Size())
		}
	}
	return nil
}

func waitForTokenFile(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	check := func() bool {
		data, err := os.ReadFile(path)
		return err == nil && len(strings.TrimSpace(string(data))) > 0
	}
	if check() {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if check() {
				return nil
			}
		}
	}
}

func (r Runner) run(ctx context.Context, stage, executable string, args []string, production bool) error {
	started := time.Now()
	r.logf("[%s] Starting stage process", stage)
	summary, err := r.loadSummary()
	if err != nil {
		return err
	}
	log, err := OpenRegular(filepath.Join(r.Workspace, stage+".log"), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = r.Workspace
	if production {
		cmd.Dir = filepath.Join(r.Workspace, "output")
	}
	cmd.Env = []string{"PATH=/opt/conda/bin:/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Join(r.Workspace, "home"), "TMPDIR=" + filepath.Join(r.Workspace, "tmp"), "JUPYTER_NO_CONFIG=1", "IPYTHONDIR=" + filepath.Join(r.Workspace, "home"), "OUTPUT_DIR=" + filepath.Join(r.Workspace, "output")}
	if production {
		for _, item := range os.Environ() {
			key, _, _ := strings.Cut(item, "=")
			if key == "PATH" || key == "HOME" || key == "TMPDIR" || key == "OUTPUT_DIR" || key == "IPYTHONDIR" || strings.HasPrefix(key, "JUPYTER_") || strings.HasPrefix(key, "PYTHON") || strings.HasPrefix(key, "FILE_SERVICE_") || strings.HasPrefix(key, "FILES_CONNECT_") {
				continue
			}
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	bounded := &boundedLog{writer: log, stream: r.StreamOutput, remaining: r.maxLogBytes()}
	cmd.Stdout = bounded
	cmd.Stderr = bounded
	runErr := cmd.Start()
	if runErr == nil {
		r.logf("[%s] Process started; streaming stdout and stderr", stage)
		runErr = cmd.Wait()
	}
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	summary.Stage = stage
	summary.Succeeded = runErr == nil
	if err := r.summary(summary); err != nil {
		return err
	}
	if bounded.truncated {
		r.logf("[%s] Raw process output reached the %d-byte limit and was truncated", stage, r.maxLogBytes())
	}
	if runErr != nil {
		r.logf("[%s] Failed after %s", stage, elapsed(started))
		return fmt.Errorf("%s failed (see bounded stage log)", stage)
	}
	if stage == "convert" {
		_, err = ReadBounded(filepath.Join(r.Workspace, "notebook.py"), MaxSourceBytes)
	}
	if err != nil {
		return err
	}
	r.logf("[%s] Completed in %s", stage, elapsed(started))
	return nil
}

func (r Runner) maxLogBytes() int64 {
	if r.MaxLogBytes > 0 {
		return r.MaxLogBytes
	}
	return MaxLogBytes
}

func (r Runner) logf(format string, args ...any) {
	if r.StreamOutput == nil {
		return
	}
	_, _ = fmt.Fprintf(r.StreamOutput, format+"\n", args...)
}

func elapsed(started time.Time) time.Duration {
	return time.Since(started).Round(time.Millisecond)
}
func (r Runner) loadSummary() (Summary, error) {
	var s Summary
	b, err := ReadBounded(filepath.Join(r.Workspace, "execution-summary.json"), 16384)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func (r Runner) summary(s Summary) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return WriteFile(filepath.Join(r.Workspace, "execution-summary.json"), b)
}

type boundedLog struct {
	mu        sync.Mutex
	writer    io.Writer
	stream    io.Writer
	remaining int64
	truncated bool
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	requested := len(p)
	if int64(len(p)) > b.remaining {
		b.truncated = true
		p = p[:b.remaining]
	}
	if len(p) == 0 {
		return requested, nil
	}
	written, err := b.writer.Write(p)
	b.remaining -= int64(written)
	if written > 0 && b.stream != nil {
		// Container-log delivery is best effort and must not fail notebook execution.
		_, _ = b.stream.Write(p[:written])
	}
	if err != nil {
		return written, err
	}
	if written != len(p) {
		return written, io.ErrShortWrite
	}
	// Report the full input as consumed when the configured log cap truncates it.
	return requested, nil
}
