// Package outputupload validates CSV deliverables and uploads through Files Connect.
package outputupload

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
	"sandbox-backend-service/internal/outputruntime"
)

type Service interface {
	RequestUploads(context.Context, string, string, output.Manifest) ([]filesconnect.UploadTarget, error)
	CompleteOutput(context.Context, string, string, output.Manifest) (output.Manifest, error)
}
type Uploader struct {
	Service   Service
	HTTP      *http.Client
	Limits    output.Limits
	LogOutput io.Writer
}

// Inventory accepts only flat, regular CSV files; no directory or symlink is a deliverable.
func Inventory(directory, prefix string, limits output.Limits) (output.Manifest, error) {
	if err := limits.Validate(); err != nil {
		return output.Manifest{}, err
	}
	if !output.IsSafeRelativePrefix(prefix) {
		return output.Manifest{}, fmt.Errorf("unsafe review prefix")
	}
	// Open the directory without following symlinks in any component using a root handle.
	// OpenRegular below independently checks every component when reading each file.
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return output.Manifest{}, fmt.Errorf("output must be a directory")
	}
	dir, err := os.Open(directory)
	if err != nil {
		return output.Manifest{}, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(limits.MaxManifestFiles + 1)
	if err != nil && err != io.EOF {
		return output.Manifest{}, err
	}
	if len(entries) == 0 || len(entries) > limits.MaxManifestFiles {
		return output.Manifest{}, fmt.Errorf("invalid output file count")
	}
	manifest := output.Manifest{Files: make([]output.ManifestFile, 0, len(entries))}
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if !output.IsSafeRelativePath(name) || !strings.EqualFold(filepath.Ext(name), ".csv") || !entry.Type().IsRegular() {
			return output.Manifest{}, fmt.Errorf("only regular CSV deliverables are allowed")
		}
		f, err := outputruntime.OpenRegular(filepath.Join(directory, name), unix.O_RDONLY)
		if err != nil {
			return output.Manifest{}, err
		}
		meta, err := inspectCSV(f, limits.MaxFileBytes)
		f.Close()
		if err != nil {
			return output.Manifest{}, fmt.Errorf("invalid CSV deliverable: %w", err)
		}
		if meta.Size > limits.MaxOutputBytes-total {
			return output.Manifest{}, fmt.Errorf("total output size exceeds limit")
		}
		total += meta.Size
		meta.Path = name
		meta.ObjectKey = prefix + "output/" + name
		sum := sha256.Sum256([]byte(meta.ObjectKey))
		meta.FileID = hex.EncodeToString(sum[:])
		manifest.Files = append(manifest.Files, meta)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	raw, err := json.Marshal(manifest)
	if err != nil {
		return output.Manifest{}, err
	}
	return limits.Parse(raw, prefix)
}
func inspectCSV(f *os.File, max int64) (output.ManifestFile, error) {
	info, err := f.Stat()
	if err != nil {
		return output.ManifestFile{}, err
	}
	if info.Size() <= 0 || info.Size() > max {
		return output.ManifestFile{}, fmt.Errorf("invalid file size")
	}
	hash := sha256.New()
	limited := &io.LimitedReader{R: f, N: max + 1}
	reader := csv.NewReader(io.TeeReader(limited, hash))
	reader.ReuseRecord = true
	rows := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return output.ManifestFile{}, fmt.Errorf("CSV parsing failed")
		}
		rows++
		for _, field := range row {
			if !utf8.ValidString(field) || strings.ContainsFunc(field, func(r rune) bool { return r == 0 || (r < 32 && r != '\n' && r != '\r' && r != '\t') }) {
				return output.ManifestFile{}, fmt.Errorf("CSV contains non-text data")
			}
		}
	}
	size := max + 1 - limited.N
	if rows == 0 || size != info.Size() || size > max {
		return output.ManifestFile{}, fmt.Errorf("empty, changed or oversized CSV")
	}
	return output.ManifestFile{Size: size, MediaType: "text/csv", SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
func (u Uploader) Run(ctx context.Context, workspace, id, prefix, manifestPath string) error {
	started := time.Now()
	u.logf("[upload] Inspecting output directory")
	limits := u.Limits.WithDefaults()
	manifest, err := Inventory(filepath.Join(workspace, "output"), prefix, limits)
	if err != nil {
		return err
	}
	var totalBytes int64
	for _, file := range manifest.Files {
		totalBytes += file.Size
		u.logf("[upload] Validated %s: %d bytes", file.Path, file.Size)
	}
	u.logf("[upload] Inventory complete: %d CSV files, %d total bytes", len(manifest.Files), totalBytes)
	u.logf("[upload] Requesting signed upload targets")
	targets, err := u.Service.RequestUploads(ctx, id, prefix, manifest)
	if err != nil {
		return err
	}
	u.logf("[upload] Received %d upload targets", len(targets))
	if len(targets) != len(manifest.Files) {
		return fmt.Errorf("upload target count mismatch")
	}
	byID := map[string]filesconnect.UploadTarget{}
	for _, target := range targets {
		if _, exists := byID[target.FileID]; exists {
			return fmt.Errorf("duplicate upload target")
		}
		if err := validateTarget(target); err != nil {
			return err
		}
		byID[target.FileID] = target
	}
	for _, file := range manifest.Files {
		if _, ok := byID[file.FileID]; !ok {
			return fmt.Errorf("missing upload target")
		}
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	if u.HTTP != nil {
		*client = *u.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	for _, file := range manifest.Files {
		u.logf("[upload] Uploading %s (%d bytes)", file.Path, file.Size)
		if err := upload(ctx, client, byID[file.FileID], filepath.Join(workspace, "output", file.Path), file); err != nil {
			return err
		}
		u.logf("[upload] Uploaded %s", file.Path)
	}
	u.logf("[upload] Finalizing verified output manifest")
	verified, err := u.Service.CompleteOutput(ctx, id, prefix, manifest)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(verified)
	if err != nil {
		return err
	}
	if _, err := limits.Parse(raw, prefix); err != nil {
		return err
	}
	// Completion may reorder files, but may not change the locally verified inventory.
	expected := map[string]output.ManifestFile{}
	for _, f := range manifest.Files {
		expected[f.FileID] = f
	}
	if len(verified.Files) != len(expected) {
		return fmt.Errorf("completion manifest mismatch")
	}
	for _, f := range verified.Files {
		if !reflect.DeepEqual(expected[f.FileID], f) {
			return fmt.Errorf("completion manifest mismatch")
		}
	}
	if err := outputruntime.WriteFile(manifestPath, raw); err != nil {
		return err
	}
	u.logf("[upload] Manifest verified and written with %d files", len(verified.Files))
	u.logf("[upload] Completed in %s", time.Since(started).Round(time.Millisecond))
	return nil
}

func (u Uploader) logf(format string, args ...any) {
	if u.LogOutput == nil {
		return
	}
	_, _ = fmt.Fprintf(u.LogOutput, format+"\n", args...)
}

func validateTarget(target filesconnect.UploadTarget) error {
	parsed, err := url.Parse(target.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("upload target must be HTTPS")
	}
	for header := range target.Headers {
		lower := strings.ToLower(header)
		if lower != "content-type" && lower != "content-md5" && !strings.HasPrefix(lower, "x-amz-") {
			return fmt.Errorf("unsupported upload signing header")
		}
	}
	return nil
}
func upload(ctx context.Context, client *http.Client, target filesconnect.UploadTarget, name string, expected output.ManifestFile) error {
	f, err := outputruntime.OpenRegular(name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	current, err := inspectCSV(f, expected.Size)
	if err != nil || current.Size != expected.Size || current.SHA256 != expected.SHA256 {
		return fmt.Errorf("CSV changed after inventory")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.URL, io.TeeReader(io.LimitReader(f, expected.Size), hash))
	if err != nil {
		return fmt.Errorf("invalid upload request")
	}
	request.ContentLength = expected.Size
	request.Header.Set("Content-Type", "text/csv")
	for key, value := range target.Headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("presigned upload failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("presigned upload returned HTTP %d", response.StatusCode)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("CSV changed during upload")
	}
	return nil
}
