package main

import (
	"net/http"
	"net/http/httptest"
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
