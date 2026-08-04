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
			name:    "portal idlechat can report surface presence",
			client:  "RenCrow_PORTAL",
			profile: "portal-idlechat",
			method:  http.MethodPost,
			path:    "/viewer/surface-presence",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal chat can report surface presence",
			client:  "RenCrow_PORTAL",
			profile: "portal-chat",
			method:  http.MethodPost,
			path:    "/viewer/surface-presence",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal chat cannot manually start idlechat",
			client:  "RenCrow_PORTAL",
			profile: "portal-chat",
			method:  http.MethodPost,
			path:    "/viewer/idlechat/start",
			want:    http.StatusForbidden,
		},
		{
			name:    "portal games can launch agent session",
			client:  "RenCrow_PORTAL",
			profile: "portal-games",
			method:  http.MethodPost,
			path:    "/viewer/games/launch",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal games can read observer frames",
			client:  "RenCrow_PORTAL",
			profile: "portal-games",
			method:  http.MethodGet,
			path:    "/viewer/games/observer-api/games/sessions/nh-1/frames",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal games can retry session",
			client:  "RenCrow_PORTAL",
			profile: "portal-games",
			method:  http.MethodPost,
			path:    "/viewer/games/observer-api/games/sessions/nh-1/retry",
			want:    http.StatusNoContent,
		},
		{
			name:    "portal games cannot submit agent decision",
			client:  "RenCrow_PORTAL",
			profile: "portal-games",
			method:  http.MethodPost,
			path:    "/viewer/games/decision",
			want:    http.StatusForbidden,
		},
		{
			name:    "portal games cannot write observer summary",
			client:  "RenCrow_PORTAL",
			profile: "portal-games",
			method:  http.MethodPost,
			path:    "/viewer/games/observer-api/games/sessions/nh-1/summary",
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
			name:    "cmd chat can transcribe audio through CORE",
			client:  "RenCrow_CMD",
			profile: "cmd-chat",
			method:  http.MethodPost,
			path:    "/stt/chat-input",
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
			name:    "assistant core profile can deliver LINE notification",
			client:  "RenCrow_ASSISTANT",
			profile: "assistant-core",
			method:  http.MethodPost,
			path:    "/internal/assistant/notifications/line",
			want:    http.StatusNoContent,
		},
		{
			name:   "assistant LINE notification requires profile headers",
			method: http.MethodPost,
			path:   "/internal/assistant/notifications/line",
			want:   http.StatusForbidden,
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
