package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTailscaleViewerOnlyGuardAllowsViewerRoutes(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/viewer", "/viewer/runtime-config", "/viewer/assets/js/viewer.js", "/audio-router/events", "/stt", "/voice-chat", "/voice-chat-ws"} {
		req := httptest.NewRequest(http.MethodGet, "https://fujitsu-ubunts.tailb07d8d.ts.net"+path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: expected allowed route, got status %d", path, rec.Code)
		}
	}
}

func TestTailscaleViewerOnlyGuardBlocksPublicFunnelViewer(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "https://fujitsu-ubunts.tailb07d8d.ts.net/viewer", nil)
	req.Header.Set("Tailscale-Funnel-Request", "true")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected Funnel Viewer request to be blocked, got status %d", rec.Code)
	}
}

func TestTailscaleViewerOnlyGuardAllowsCanonicalLineWebhookPost(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "https://fujitsu-ubunts.tailb07d8d.ts.net/webhook/line", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected canonical LINE webhook POST to pass through, got status %d", rec.Code)
	}
}

func TestTailscaleViewerOnlyGuardBlocksNonViewerRoutesOnTailscaleHost(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/"},
		{method: http.MethodGet, path: "/health"},
		{method: http.MethodGet, path: "/ready"},
		{method: http.MethodGet, path: "/webhook/line"},
		{method: http.MethodGet, path: "/stt/health"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "https://fujitsu-ubunts.tailb07d8d.ts.net"+tt.path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: expected blocked route, got status %d", tt.method, tt.path, rec.Code)
		}
	}
}

func TestTailscaleViewerOnlyGuardAllowsCMDInteractionProfiles(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withTailscaleViewerOnlyGuard(withInteractionProfileGuard(next))
	tests := []struct {
		name    string
		method  string
		path    string
		profile string
	}{
		{name: "cmd diagnostics health", method: http.MethodGet, path: "/health", profile: "cmd-diagnostics"},
		{name: "cmd chat send", method: http.MethodPost, path: "/viewer/send", profile: "cmd-chat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "https://fujitsu-ubunts.tailb07d8d.ts.net"+tt.path, nil)
			req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
			req.Header.Set(interactionProfileHeader, tt.profile)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s %s: expected allowed CMD profile route, got status %d", tt.method, tt.path, rec.Code)
			}
		})
	}
}

func TestTailscaleViewerOnlyGuardBlocksInvalidCMDInteractionProfiles(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	tests := []struct {
		name    string
		method  string
		path    string
		client  string
		profile string
		funnel  bool
	}{
		{name: "missing client", method: http.MethodGet, path: "/health", profile: "cmd-diagnostics"},
		{name: "missing profile", method: http.MethodGet, path: "/health", client: "RenCrow_CMD"},
		{name: "mismatched client", method: http.MethodGet, path: "/health", client: "RenCrow_PORTAL", profile: "cmd-diagnostics"},
		{name: "unknown profile", method: http.MethodGet, path: "/health", client: "RenCrow_CMD", profile: "cmd-unknown"},
		{name: "mismatched viewer profile", method: http.MethodPost, path: "/viewer/send", client: "RenCrow_CMD", profile: "cmd-diagnostics"},
		{name: "missing viewer profile", method: http.MethodPost, path: "/viewer/send", client: "RenCrow_CMD"},
		{name: "wrong method for profile", method: http.MethodPost, path: "/health", client: "RenCrow_CMD", profile: "cmd-diagnostics"},
		{name: "non viewer arbitrary path", method: http.MethodGet, path: "/v1/not-allowlisted", client: "RenCrow_CMD", profile: "cmd-diagnostics"},
		{name: "funnel diagnostics", method: http.MethodGet, path: "/health", client: "RenCrow_CMD", profile: "cmd-diagnostics", funnel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "https://fujitsu-ubunts.tailb07d8d.ts.net"+tt.path, nil)
			if tt.client != "" {
				req.Header.Set("X-RenCrow-Client", tt.client)
			}
			if tt.profile != "" {
				req.Header.Set(interactionProfileHeader, tt.profile)
			}
			if tt.funnel {
				req.Header.Set("Tailscale-Funnel-Request", "true")
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s: expected blocked route, got status %d", tt.method, tt.path, rec.Code)
			}
		})
	}
}

func TestTailscaleViewerOnlyGuardAllowsOnlyAuthenticatedPortalSTTFileRoute(t *testing.T) {
	var calls int
	handler := withTailscaleViewerOnlyGuard(withInteractionProfileGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})))

	validActor := "tailscale-sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name, profile, actor string
		want                 int
	}{
		{name: "authenticated portal chat", profile: "portal-chat", actor: validActor, want: http.StatusNoContent},
		{name: "missing actor", profile: "portal-chat", want: http.StatusNotFound},
		{name: "invalid actor", profile: "portal-chat", actor: "tailscale-sha256:not-a-digest", want: http.StatusNotFound},
		{name: "idlechat", profile: "portal-idlechat", actor: validActor, want: http.StatusNotFound},
		{name: "games", profile: "portal-games", actor: validActor, want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.ts.net/stt/chat-input", nil)
			request.Header.Set("X-Forwarded-Host", "example.ts.net")
			request.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
			request.Header.Set(interactionProfileHeader, test.profile)
			if test.actor != "" {
				request.Header.Set("X-RenCrow-Authenticated-Actor", test.actor)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d want=%d", recorder.Code, test.want)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("downstream calls=%d want=1", calls)
	}
}

func TestTailscaleViewerOnlyGuardKeepsLANRoutes(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.204:18790/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected LAN route to pass through, got status %d", rec.Code)
	}
}

func TestTailscaleViewerOnlyGuardUsesForwardedHost(t *testing.T) {
	handler := withTailscaleViewerOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/health", nil)
	req.Header.Set("X-Forwarded-Host", "fujitsu-ubunts.tailb07d8d.ts.net")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected forwarded Tailscale host to be blocked, got status %d", rec.Code)
	}
}
