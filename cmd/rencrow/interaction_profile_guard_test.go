package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInteractionProfileGuardEnforcesKnownClientCapabilities(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withInteractionProfileGuard(next)

	tests := []struct {
		name    string
		client  string
		profile string
		method  string
		path    string
		want    int
	}{
		{
			name:   "known client requires profile",
			client: "RenCrow_PORTAL",
			method: http.MethodGet,
			path:   "/viewer/events",
			want:   http.StatusForbidden,
		},
		{
			name:    "portal chat can send",
			client:  "RenCrow_PORTAL",
			profile: "portal-chat",
			method:  http.MethodPost,
			path:    "/viewer/send",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal idlechat can read",
			client:  "RenCrow_PORTAL",
			profile: "portal-idlechat",
			method:  http.MethodGet,
			path:    "/viewer/idlechat/status",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal idlechat cannot send",
			client:  "RenCrow_PORTAL",
			profile: "portal-idlechat",
			method:  http.MethodPost,
			path:    "/viewer/send",
			want:    http.StatusForbidden,
		},
		{
			name:    "cmd chat can stream",
			client:  "RenCrow_CMD",
			profile: "cmd-chat",
			method:  http.MethodGet,
			path:    "/viewer/events",
			want:    http.StatusNoContent,
		},
		{
			name:    "cmd idlechat can stop",
			client:  "RenCrow_CMD",
			profile: "cmd-idlechat",
			method:  http.MethodPost,
			path:    "/viewer/idlechat/stop",
			want:    http.StatusNoContent,
		},
		{
			name:    "client profile pair must match",
			client:  "RenCrow_CMD",
			profile: "portal-chat",
			method:  http.MethodPost,
			path:    "/viewer/send",
			want:    http.StatusForbidden,
		},
		{
			name:    "assistant core profile can send",
			client:  "RenCrow_ASSISTANT",
			profile: "assistant-core",
			method:  http.MethodPost,
			path:    "/viewer/send",
			want:    http.StatusNoContent,
		},
		{
			name:   "core debug viewer remains internal",
			method: http.MethodPost,
			path:   "/viewer/send",
			want:   http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.client != "" {
				req.Header.Set("X-RenCrow-Client", tt.client)
			}
			if tt.profile != "" {
				req.Header.Set("X-RenCrow-Interaction-Profile", tt.profile)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
