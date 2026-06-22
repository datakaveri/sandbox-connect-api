package main

import "testing"

func TestGetAuditAction(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   string
	}{
		{
			name:   "create booking",
			method: "POST",
			path:   "/v1/bookings",
			want:   "Create",
		},
		{
			name:   "create notebook token session",
			method: "POST",
			path:   "/v1/bookings/123/notebook-token-session",
			want:   "CreateNotebookTokenSession",
		},
		{
			name:   "rotate notebook token session",
			method: "PUT",
			path:   "/v1/bookings/123/notebook-token-session",
			want:   "RotateNotebookTokenSession",
		},
		{
			name:   "cancel booking",
			method: "PATCH",
			path:   "/v1/bookings/123/cancel",
			want:   "Cancel",
		},
		{
			name:   "extend booking",
			method: "PATCH",
			path:   "/v1/bookings/123/extend",
			want:   "Extend",
		},
		{
			name:   "reset booking",
			method: "PATCH",
			path:   "/v1/bookings/123/reset",
			want:   "Reset",
		},
		{
			name:   "terminate booking",
			method: "PATCH",
			path:   "/v1/bookings/123/terminate",
			want:   "Terminate",
		},
		{
			name:   "unknown booking action",
			method: "PATCH",
			path:   "/v1/bookings/123/archive",
			want:   "",
		},
		{
			name:   "notebook lifecycle writes are not audited",
			method: "PATCH",
			path:   "/v1/notebook/stop",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getAuditAction(tt.method, tt.path); got != tt.want {
				t.Fatalf("getAuditAction(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}
