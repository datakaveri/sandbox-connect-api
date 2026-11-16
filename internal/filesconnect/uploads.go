package filesconnect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"sandbox-backend-service/internal/output"
)

// UploadTarget is returned by Files Connect after authorizing the trusted output job.
// Headers contain storage signing headers only; never service authentication.
type UploadTarget struct {
	FileID  string            `json:"fileId"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (c *Client) RequestUploads(ctx context.Context, id, prefix string, manifest output.Manifest) ([]UploadTarget, error) {
	if !safeSegment(id) {
		return nil, fmt.Errorf("invalid output ID")
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Uploads []UploadTarget `json:"uploads"`
		} `json:"data"`
	}
	body := map[string]any{"reviewPrefix": prefix, "files": manifest.Files}
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/outputs/"+url.PathEscape(id)+"/uploads", body, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, fmt.Errorf("upload authorization failed")
	}
	return response.Data.Uploads, nil
}
func (c *Client) CompleteOutput(ctx context.Context, id, prefix string, manifest output.Manifest) (output.Manifest, error) {
	if !safeSegment(id) {
		return output.Manifest{}, fmt.Errorf("invalid output ID")
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Manifest          output.Manifest `json:"manifest"`
			ReviewManifestKey string          `json:"reviewManifestKey"`
		} `json:"data"`
	}
	body := map[string]any{"reviewPrefix": prefix, "files": manifest.Files}
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/outputs/"+url.PathEscape(id)+"/complete", body, &response); err != nil {
		return output.Manifest{}, err
	}
	if !response.Success || response.Data.ReviewManifestKey != prefix+"manifest.json" {
		return output.Manifest{}, fmt.Errorf("invalid completion result")
	}
	return response.Data.Manifest, nil
}
