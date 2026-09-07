package evaluation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

const DefaultMaxManifestFiles = 1000

type ManifestFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
}

type Manifest struct {
	Files []ManifestFile `json:"files"`
}

func ParseManifest(raw json.RawMessage, maxBytes, maxFiles int) (Manifest, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return Manifest{}, fmt.Errorf("manifest is empty or exceeds %d bytes", maxBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if len(manifest.Files) > maxFiles {
		return Manifest{}, fmt.Errorf("manifest exceeds %d files", maxFiles)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for index, file := range manifest.Files {
		clean := path.Clean(file.Path)
		if !IsSafeRelativePath(file.Path) {
			return Manifest{}, fmt.Errorf("file %d has unsafe path", index)
		}
		if _, exists := seen[clean]; exists {
			return Manifest{}, fmt.Errorf("file %d duplicates path %q", index, clean)
		}
		seen[clean] = struct{}{}
		if file.Size < 0 || file.MediaType == "" {
			return Manifest{}, fmt.Errorf("file %d has invalid size or media type", index)
		}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 {
			return Manifest{}, fmt.Errorf("file %d has invalid SHA-256", index)
		}
	}
	return manifest, nil
}

func IsSafeRelativePath(value string) bool {
	clean := path.Clean(value)
	return value != "" && clean != "." && clean == value && !path.IsAbs(clean) &&
		clean != ".." && !strings.HasPrefix(clean, "../")
}
