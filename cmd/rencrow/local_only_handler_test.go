package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOnlyHandlerRejectsRemoteAndAllowsLoopback(t *testing.T) {
	handler := localOnlyHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	remote := httptest.NewRequest(http.MethodPost, "http://192.0.2.10/internal/assistant/notifications/line", nil)
	remote.RemoteAddr = "192.0.2.10:4567"
	remoteRecorder := httptest.NewRecorder()
	handler.ServeHTTP(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusNotFound {
		t.Fatalf("remote status=%d want=%d", remoteRecorder.Code, http.StatusNotFound)
	}

	loopback := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/assistant/notifications/line", nil)
	loopback.RemoteAddr = "127.0.0.1:4567"
	loopbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loopbackRecorder, loopback)
	if loopbackRecorder.Code != http.StatusNoContent {
		t.Fatalf("loopback status=%d want=%d", loopbackRecorder.Code, http.StatusNoContent)
	}

	ipv6 := httptest.NewRequest(http.MethodPost, "http://[::1]/internal/assistant/notifications/line", nil)
	ipv6.RemoteAddr = "[::1]:4567"
	ipv6Recorder := httptest.NewRecorder()
	handler.ServeHTTP(ipv6Recorder, ipv6)
	if ipv6Recorder.Code != http.StatusNoContent {
		t.Fatalf("ipv6 loopback status=%d want=%d", ipv6Recorder.Code, http.StatusNoContent)
	}

	forwarded := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/assistant/notifications/line", nil)
	forwarded.RemoteAddr = "127.0.0.1:4567"
	forwarded.Header.Set("X-Forwarded-For", "192.0.2.10")
	forwardedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(forwardedRecorder, forwarded)
	if forwardedRecorder.Code != http.StatusNotFound {
		t.Fatalf("forwarded loopback status=%d want=%d", forwardedRecorder.Code, http.StatusNotFound)
	}

	tailscale := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/assistant/notifications/line", nil)
	tailscale.RemoteAddr = "127.0.0.1:4567"
	tailscale.Header.Set("Tailscale-User-Login", "user@example.com")
	tailscaleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(tailscaleRecorder, tailscale)
	if tailscaleRecorder.Code != http.StatusNotFound {
		t.Fatalf("tailscale loopback status=%d want=%d", tailscaleRecorder.Code, http.StatusNotFound)
	}
}
