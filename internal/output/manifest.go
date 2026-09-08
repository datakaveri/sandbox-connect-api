package output

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	DefaultMaxManifestFiles = 1000
	DefaultMaxFileBytes     = int64(256 * 1024 * 1024)
	DefaultMaxOutputBytes   = int64(1024 * 1024 * 1024)
)

type ManifestFile struct {
	FileID    string `json:"fileId"`
	Path      string `json:"path"`
	ObjectKey string `json:"objectKey"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
}

type Manifest struct {
	Files []ManifestFile `json:"files"`
}

func ParseManifest(raw json.RawMessage, maxBytes, maxFiles int, maxFileBytes, maxOutputBytes int64) (Manifest, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return Manifest{}, fmt.Errorf("manifest is empty or exceeds %d bytes", maxBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if len(manifest.Files) == 0 {
		return Manifest{}, fmt.Errorf("manifest contains no output files")
	}
	if len(manifest.Files) > maxFiles {
		return Manifest{}, fmt.Errorf("manifest exceeds %d files", maxFiles)
	}
	if maxFileBytes <= 0 || maxOutputBytes <= 0 || maxFileBytes > maxOutputBytes {
		return Manifest{}, fmt.Errorf("maximum file and output sizes must be positive and ordered")
	}

	seenIDs := make(map[string]struct{}, len(manifest.Files))
	seenPaths := make(map[string]struct{}, len(manifest.Files))
	seenKeys := make(map[string]struct{}, len(manifest.Files))
	var totalBytes int64
	for index, file := range manifest.Files {
		if file.FileID == "" || len(file.FileID) > 200 || strings.ContainsAny(file.FileID, "/\\\r\n\t ") {
			return Manifest{}, fmt.Errorf("file %d has invalid fileId", index)
		}
		if _, exists := seenIDs[file.FileID]; exists {
			return Manifest{}, fmt.Errorf("file %d duplicates fileId %q", index, file.FileID)
		}
		seenIDs[file.FileID] = struct{}{}

		clean := path.Clean(file.Path)
		if !IsSafeRelativePath(file.Path) || !strings.EqualFold(filepath.Ext(clean), ".csv") {
			return Manifest{}, fmt.Errorf("file %d must have a safe relative .csv path", index)
		}
		if _, exists := seenPaths[clean]; exists {
			return Manifest{}, fmt.Errorf("file %d duplicates path %q", index, clean)
		}
		seenPaths[clean] = struct{}{}

		cleanKey := path.Clean(file.ObjectKey)
		if !IsSafeRelativePath(file.ObjectKey) {
			return Manifest{}, fmt.Errorf("file %d has unsafe objectKey", index)
		}
		if _, exists := seenKeys[cleanKey]; exists {
			return Manifest{}, fmt.Errorf("file %d duplicates objectKey %q", index, cleanKey)
		}
		seenKeys[cleanKey] = struct{}{}

		if file.Size < 0 || file.Size > maxFileBytes ||
			(file.MediaType != "text/csv" && file.MediaType != "application/csv") {
			return Manifest{}, fmt.Errorf("file %d has invalid size or non-CSV media type", index)
		}
		if file.Size > maxOutputBytes-totalBytes {
			return Manifest{}, fmt.Errorf("output files exceed %d bytes", maxOutputBytes)
		}
		totalBytes += file.Size

		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 {
			return Manifest{}, fmt.Errorf("file %d has invalid SHA-256", index)
		}
	}
	return manifest, nil
}

func ValidateManifestPrefix(manifest Manifest, reviewPrefix string) error {
	if !IsSafeRelativePrefix(reviewPrefix) {
		return fmt.Errorf("review prefix is unsafe")
	}
	for index, file := range manifest.Files {
		if !strings.HasPrefix(file.ObjectKey, reviewPrefix) ||
			strings.TrimPrefix(file.ObjectKey, reviewPrefix) == "" {
			return fmt.Errorf("file %d objectKey is outside the output review prefix", index)
		}
	}
	return nil
}

func IsSafeRelativePath(value string) bool {
	clean := path.Clean(value)
	return !strings.Contains(value, "\\") && !strings.ContainsFunc(value, unicode.IsControl) && value != "" && clean != "." && clean == value && !path.IsAbs(clean) &&
		clean != ".." && !strings.HasPrefix(clean, "../")
}

func IsSafeRelativePrefix(value string) bool {
	return strings.HasSuffix(value, "/") && IsSafeRelativePath(strings.TrimSuffix(value, "/"))
}
