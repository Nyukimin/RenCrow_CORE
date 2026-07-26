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

	remote := httptest.NewRequest(http.MethodPost, "/internal/assistant/notifications/line", nil)
	remote.RemoteAddr = "192.0.2.10:4567"
	remoteRecorder := httptest.NewRecorder()
	handler.ServeHTTP(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusNotFound {
		t.Fatalf("remote status=%d want=%d", remoteRecorder.Code, http.StatusNotFound)
	}

	loopback := httptest.NewRequest(http.MethodPost, "/internal/assistant/notifications/line", nil)
	loopback.RemoteAddr = "127.0.0.1:4567"
	loopbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loopbackRecorder, loopback)
	if loopbackRecorder.Code != http.StatusNoContent {
		t.Fatalf("loopback status=%d want=%d", loopbackRecorder.Code, http.StatusNoContent)
	}
}
