package evaluation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseManifestValidatesOutputMetadata(t *testing.T) {
	digest := strings.Repeat("a", 64)
	raw, err := json.Marshal(Manifest{Files: []ManifestFile{{
		Path: "reports/result.csv", Size: 12, MediaType: "text/csv", SHA256: digest,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(raw, 4096, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "reports/result.csv" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestParseManifestRejectsUnsafeOrMalformedFiles(t *testing.T) {
	digest := strings.Repeat("b", 64)
	tests := []struct {
		name     string
		manifest Manifest
		maxFiles int
	}{
		{name: "traversal", manifest: Manifest{Files: []ManifestFile{{Path: "../secret", MediaType: "text/plain", SHA256: digest}}}, maxFiles: 10},
		{name: "bad digest", manifest: Manifest{Files: []ManifestFile{{Path: "result.txt", MediaType: "text/plain", SHA256: "short"}}}, maxFiles: 10},
		{name: "too many", manifest: Manifest{Files: []ManifestFile{
			{Path: "one", MediaType: "text/plain", SHA256: digest},
			{Path: "two", MediaType: "text/plain", SHA256: digest},
		}}, maxFiles: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseManifest(raw, 4096, test.maxFiles); err == nil {
				t.Fatal("expected manifest to be rejected")
			}
		})
	}
}
