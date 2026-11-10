package main

import (
	"strings"
	"testing"
)

func stringPtr(value string) *string {
	return &value
}

func TestNormalizeAndValidateRuntimeAssets(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateBookingRequest
		wantErr string
	}{
		{
			name: "empty runtime fields are allowed",
			req:  CreateBookingRequest{},
		},
		{
			name: "valid file and public github repo",
			req: CreateBookingRequest{
				FileURL: stringPtr(" https://example.com/data/input.csv "),
				GitURL:  stringPtr("https://github.com/datakaveri/example.git"),
			},
		},
		{
			name: "valid github repo with access token",
			req: CreateBookingRequest{
				GitURL:         stringPtr("https://github.com/datakaveri/private-repo"),
				GitAccessToken: stringPtr("github_pat_test"),
			},
		},
		{
			name: "valid github repo with token secret",
			req: CreateBookingRequest{
				GitURL:             stringPtr("https://github.com/datakaveri/private-repo"),
				GitTokenSecretName: stringPtr("github-token"),
			},
		},
		{
			name: "file url must use https",
			req: CreateBookingRequest{
				FileURL: stringPtr("http://example.com/file.csv"),
			},
			wantErr: "fileUrl must use https",
		},
		{
			name: "file url must not include credentials",
			req: CreateBookingRequest{
				FileURL: stringPtr("https://user:pass@example.com/file.csv"),
			},
			wantErr: "fileUrl must not include credentials",
		},
		{
			name: "git url must be github",
			req: CreateBookingRequest{
				GitURL: stringPtr("https://gitlab.com/datakaveri/example.git"),
			},
			wantErr: "gitUrl must be a GitHub HTTPS URL",
		},
		{
			name: "access token requires git url",
			req: CreateBookingRequest{
				GitAccessToken: stringPtr("github_pat_test"),
			},
			wantErr: "gitAccessToken requires gitUrl",
		},
		{
			name: "token secret requires git url",
			req: CreateBookingRequest{
				GitTokenSecretName: stringPtr("github-token"),
			},
			wantErr: "gitTokenSecretName requires gitUrl",
		},
		{
			name: "access token and token secret are mutually exclusive",
			req: CreateBookingRequest{
				GitURL:             stringPtr("https://github.com/datakaveri/private-repo"),
				GitAccessToken:     stringPtr("github_pat_test"),
				GitTokenSecretName: stringPtr("github-token"),
			},
			wantErr: "use either gitAccessToken or gitTokenSecretName, not both",
		},
		{
			name: "token secret must be dns label",
			req: CreateBookingRequest{
				GitURL:             stringPtr("https://github.com/datakaveri/private-repo"),
				GitTokenSecretName: stringPtr("Bad_Secret"),
			},
			wantErr: "gitTokenSecretName must be a valid Kubernetes DNS label",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndValidateRuntimeAssets(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestNormalizeAndValidateNotebookRuntimeAssets(t *testing.T) {
	tests := []struct {
		name    string
		req     NotebookRequest
		wantErr string
	}{
		{
			name: "valid file and public github repo",
			req: NotebookRequest{
				FileURL: stringPtr(" https://example.com/data/input.csv "),
				GitURL:  stringPtr("https://github.com/datakaveri/example.git"),
			},
		},
		{
			name: "access token requires git url",
			req: NotebookRequest{
				GitAccessToken: stringPtr("github_pat_test"),
			},
			wantErr: "gitAccessToken requires gitUrl",
		},
		{
			name: "access token and token secret are mutually exclusive",
			req: NotebookRequest{
				GitURL:             stringPtr("https://github.com/datakaveri/private-repo"),
				GitAccessToken:     stringPtr("github_pat_test"),
				GitTokenSecretName: stringPtr("github-token"),
			},
			wantErr: "use either gitAccessToken or gitTokenSecretName, not both",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndValidateNotebookRuntimeAssets(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestNormalizeAndValidateNotebookRuntimeAssetsTrimsValues(t *testing.T) {
	req := NotebookRequest{
		FileURL:        stringPtr(" https://example.com/input.csv "),
		GitURL:         stringPtr(" https://github.com/datakaveri/repo.git "),
		GitAccessToken: stringPtr(" github_pat_test "),
	}

	if err := normalizeAndValidateNotebookRuntimeAssets(&req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := *req.FileURL; got != "https://example.com/input.csv" {
		t.Fatalf("fileUrl was not trimmed: %q", got)
	}
	if got := *req.GitURL; got != "https://github.com/datakaveri/repo.git" {
		t.Fatalf("gitUrl was not trimmed: %q", got)
	}
	if got := *req.GitAccessToken; got != "github_pat_test" {
		t.Fatalf("gitAccessToken was not trimmed: %q", got)
	}
}

func TestNormalizeAndValidateRuntimeAssetsTrimsValues(t *testing.T) {
	req := CreateBookingRequest{
		FileURL:        stringPtr(" https://example.com/input.csv "),
		GitURL:         stringPtr(" https://github.com/datakaveri/repo.git "),
		GitAccessToken: stringPtr(" github_pat_test "),
	}

	if err := normalizeAndValidateRuntimeAssets(&req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := *req.FileURL; got != "https://example.com/input.csv" {
		t.Fatalf("fileUrl was not trimmed: %q", got)
	}
	if got := *req.GitURL; got != "https://github.com/datakaveri/repo.git" {
		t.Fatalf("gitUrl was not trimmed: %q", got)
	}
	if got := *req.GitAccessToken; got != "github_pat_test" {
		t.Fatalf("gitAccessToken was not trimmed: %q", got)
	}
}
