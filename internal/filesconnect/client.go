package filesconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sandbox-backend-service/internal/output"
)

const defaultMaxResponseBytes = int64(4 * 1024 * 1024)

type Config struct {
	Limits              output.Limits
	BaseURL             string
	ServiceToken        string
	ReviewDatabankID    string
	WorkspaceDatabankID string
	Timeout             time.Duration
	MaxResponseBytes    int64
}

type Client struct {
	limits              output.Limits
	baseURL             string
	serviceToken        string
	reviewDatabankID    string
	workspaceDatabankID string
	httpClient          *http.Client
	maxResponseBytes    int64
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("files connect returned HTTP %d (%s)", e.StatusCode, e.Code)
}

type File struct {
	ID           string    `json:"fileId"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
	ContentType  string    `json:"contentType,omitempty"`
}

type Preview struct {
	Content     any    `json:"content"`
	Format      string `json:"format"`
	Truncated   bool   `json:"truncated"`
	FirstNLines int    `json:"firstNLines,omitempty"`
	TotalLines  int    `json:"totalLines,omitempty"`
}

type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func NewClient(config Config) (*Client, error) { return newClient(config, true) }

// NewUploadClient uses output-scoped endpoints; databanks are derived by Files Connect.
func NewUploadClient(config Config) (*Client, error) { return newClient(config, false) }

func newClient(config Config, requireDatabanks bool) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("files connect base URL must be an absolute HTTP(S) URL without query or fragment")
	}
	if strings.TrimSpace(config.ServiceToken) == "" {
		return nil, fmt.Errorf("files connect service token is required")
	}
	if requireDatabanks && (strings.TrimSpace(config.ReviewDatabankID) == "" || strings.TrimSpace(config.WorkspaceDatabankID) == "") {
		return nil, fmt.Errorf("review and workspace databank IDs are required")
	}
	if config.Timeout <= 0 {
		return nil, fmt.Errorf("files connect timeout must be positive")
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultMaxResponseBytes
	}
	limits := config.Limits.WithDefaults()
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		limits:  limits,
		baseURL: baseURL, serviceToken: strings.TrimSpace(config.ServiceToken),
		reviewDatabankID: config.ReviewDatabankID, workspaceDatabankID: config.WorkspaceDatabankID,
		httpClient: &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, maxResponseBytes: config.MaxResponseBytes,
	}, nil
}

func (c *Client) Publish(ctx context.Context, work output.ApprovalWork) (output.PublicationResult, error) {
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			WorkspacePrefix      string   `json:"workspacePrefix"`
			WorkspaceManifestKey string   `json:"workspaceManifestKey"`
			ApprovedFileIDs      []string `json:"approvedFileIds"`
		} `json:"data"`
	}
	request := struct {
		OutputID            string                `json:"outputId"`
		OwnerID             string                `json:"ownerId"`
		ReviewDatabankID    string                `json:"reviewDatabankId"`
		WorkspaceDatabankID string                `json:"workspaceDatabankId"`
		ReviewPrefix        string                `json:"reviewPrefix"`
		ReviewManifestKey   string                `json:"reviewManifestKey"`
		WorkspacePrefix     string                `json:"workspacePrefix"`
		Files               []output.ManifestFile `json:"files"`
	}{
		OutputID: work.Output.ID, OwnerID: work.Output.UserID,
		ReviewDatabankID: c.reviewDatabankID, WorkspaceDatabankID: c.workspaceDatabankID,
		ReviewPrefix: work.Output.ReviewPrefix, ReviewManifestKey: work.Output.ReviewManifestKey,
		WorkspacePrefix: work.Approval.WorkspacePrefix,
	}
	manifest, err := c.limits.Parse(work.Output.OutputManifest, work.Output.ReviewPrefix)
	if err != nil {
		return output.PublicationResult{}, fmt.Errorf("validate stored output manifest: %w", err)
	}
	if err := output.ValidateManifestPrefix(manifest, work.Output.ReviewPrefix); err != nil {
		return output.PublicationResult{}, err
	}
	request.Files = manifest.Files
	endpoint := c.baseURL + "/outputs/" + url.PathEscape(work.Output.ID) + "/publish"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, request, &response); err != nil {
		return output.PublicationResult{}, err
	}
	if !response.Success || response.Data.WorkspacePrefix != work.Approval.WorkspacePrefix ||
		response.Data.WorkspaceManifestKey != work.Approval.WorkspacePrefix+"manifest.json" ||
		!sameFileIDs(manifest.Files, response.Data.ApprovedFileIDs) {
		return output.PublicationResult{}, fmt.Errorf("files connect returned an invalid publication result")
	}
	return output.PublicationResult{
		WorkspacePrefix:      response.Data.WorkspacePrefix,
		WorkspaceManifestKey: response.Data.WorkspaceManifestKey,
		ApprovedFileIDs:      response.Data.ApprovedFileIDs,
	}, nil
}

func (c *Client) ListWorkspace(ctx context.Context, userID string) ([]File, error) {
	if !safeSegment(userID) {
		return nil, fmt.Errorf("user identity is unsafe")
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Files []File `json:"files"`
		} `json:"data"`
	}
	endpoint := c.baseURL + "/outputs/internal/workspaces/" + url.PathEscape(userID) + "/files"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if !response.Success || response.Data.Files == nil {
		return nil, fmt.Errorf("files connect returned an invalid workspace response")
	}
	return response.Data.Files, nil
}

func (c *Client) PreviewReviewFile(ctx context.Context, outputID, fileID string) (Preview, error) {
	if !safeSegment(outputID) || !safeSegment(fileID) {
		return Preview{}, fmt.Errorf("output or file identity is unsafe")
	}
	endpoint := c.baseURL + "/outputs/internal/review/" + url.PathEscape(outputID) +
		"/files/" + url.PathEscape(fileID) + "/preview"
	return c.preview(ctx, endpoint)
}

func (c *Client) PreviewWorkspaceFile(ctx context.Context, userID, fileID string) (Preview, error) {
	if !safeSegment(userID) || !safeSegment(fileID) {
		return Preview{}, fmt.Errorf("user or file identity is unsafe")
	}
	endpoint := c.baseURL + "/outputs/internal/workspaces/" + url.PathEscape(userID) +
		"/files/" + url.PathEscape(fileID) + "/preview"
	return c.preview(ctx, endpoint)
}

func (c *Client) DownloadWorkspaceFile(ctx context.Context, userID, fileID string) (Download, error) {
	if !safeSegment(userID) || !safeSegment(fileID) {
		return Download{}, fmt.Errorf("user or file identity is unsafe")
	}
	var response struct {
		Success bool     `json:"success"`
		Data    Download `json:"data"`
	}
	endpoint := c.baseURL + "/outputs/internal/workspaces/" + url.PathEscape(userID) +
		"/files/" + url.PathEscape(fileID) + "/download"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return Download{}, err
	}
	if !response.Success || response.Data.URL == "" {
		return Download{}, fmt.Errorf("files connect returned an invalid download response")
	}
	return response.Data, nil
}

func (c *Client) preview(ctx context.Context, endpoint string) (Preview, error) {
	var response struct {
		Success bool    `json:"success"`
		Data    Preview `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return Preview{}, err
	}
	if !response.Success || response.Data.Format == "" {
		return Preview{}, fmt.Errorf("files connect returned an invalid preview response")
	}
	return response.Data, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, requestBody, responseBody any) error {
	var requestReader io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		requestReader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestReader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.serviceToken)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call files connect: %w", err)
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read files connect response: %w", err)
	}
	if int64(len(responseBytes)) > c.maxResponseBytes {
		return fmt.Errorf("files connect response exceeds %d bytes", c.maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(responseBytes, &payload)
		return &Error{StatusCode: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
	}
	if err := json.Unmarshal(responseBytes, responseBody); err != nil {
		return fmt.Errorf("decode files connect response: %w", err)
	}
	return nil
}

func sameFileIDs(files []output.ManifestFile, approved []string) bool {
	if len(files) != len(approved) {
		return false
	}
	expected := make(map[string]struct{}, len(files))
	for _, file := range files {
		expected[file.FileID] = struct{}{}
	}
	for _, fileID := range approved {
		if _, exists := expected[fileID]; !exists {
			return false
		}
		delete(expected, fileID)
	}
	return len(expected) == 0
}

func safeSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func IsNotFound(err error) bool {
	var connectErr *Error
	return errors.As(err, &connectErr) && connectErr.StatusCode == http.StatusNotFound
}
