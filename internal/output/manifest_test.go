package output

import (
	"encoding/json"
	"strings"
	"testing"
)

func validManifest() json.RawMessage {
	raw, _ := json.Marshal(Manifest{Files: []ManifestFile{{
		FileID:    "file-1",
		Path:      "reports/result.csv",
		ObjectKey: "nha-review/users/u/sandboxes/n/outputs/o/reports/result.csv",
		Size:      12,
		MediaType: "text/csv",
		SHA256:    strings.Repeat("a", 64),
	}}})
	return raw
}

func TestParseManifestAcceptsCSVOutput(t *testing.T) {
	manifest, err := ParseManifest(validManifest(), 4096, 10, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].FileID != "file-1" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if err := ValidateManifestPrefix(manifest,
		"nha-review/users/u/sandboxes/n/outputs/o/"); err != nil {
		t.Fatal(err)
	}
}

func TestParseManifestRejectsUnsafeOrNonCSVFiles(t *testing.T) {
	tests := []ManifestFile{
		{FileID: "file-1", Path: "../result.csv", ObjectKey: "review/result.csv", Size: 1, MediaType: "text/csv", SHA256: strings.Repeat("a", 64)},
		{FileID: "file-1", Path: "result.json", ObjectKey: "review/result.json", Size: 1, MediaType: "application/json", SHA256: strings.Repeat("a", 64)},
		{FileID: "", Path: "result.csv", ObjectKey: "review/result.csv", Size: 1, MediaType: "text/csv", SHA256: strings.Repeat("a", 64)},
		{FileID: "file-1", Path: "result.csv", ObjectKey: "../result.csv", Size: 1, MediaType: "text/csv", SHA256: strings.Repeat("a", 64)},
	}
	for _, file := range tests {
		raw, _ := json.Marshal(Manifest{Files: []ManifestFile{file}})
		if _, err := ParseManifest(raw, 4096, 10, 1024, 1024); err == nil {
			t.Fatalf("expected invalid file to be rejected: %#v", file)
		}
	}
}

func TestParseManifestEnforcesCountsAndTotalSize(t *testing.T) {
	if _, err := ParseManifest(validManifest(), 4096, 0, 1024, 1024); err == nil {
		t.Fatal("expected file count limit")
	}
	if _, err := ParseManifest(validManifest(), 4096, 10, 11, 11); err == nil {
		t.Fatal("expected total byte limit")
	}
}

func TestValidateManifestPrefixRejectsAnotherOutput(t *testing.T) {
	manifest, err := ParseManifest(validManifest(), 4096, 10, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifestPrefix(manifest,
		"nha-review/users/other/sandboxes/n/outputs/o/"); err == nil {
		t.Fatal("expected object key outside prefix to be rejected")
	}
}
