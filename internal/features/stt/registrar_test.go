package stt

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersSTTPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		ClientLog:    statusHandler(http.StatusOK),
		WAV:          statusHandler(http.StatusCreated),
		RawWAV:       statusHandler(http.StatusAccepted),
		AutoTest:     statusHandler(http.StatusNoContent),
		AdminRestart: statusHandler(http.StatusResetContent),
		Health:       statusHandler(http.StatusPartialContent),
		File:         statusHandler(http.StatusAlreadyReported),
		ChatInput:    statusHandler(http.StatusMultiStatus),
		WebSocket:    http.HandlerFunc(statusHandler(http.StatusIMUsed)),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/stt/log", want: http.StatusOK},
		{path: "/viewer/stt/wav", want: http.StatusCreated},
		{path: "/viewer/stt/wav/raw", want: http.StatusAccepted},
		{path: "/viewer/stt/autotest", want: http.StatusNoContent},
		{path: "/viewer/stt/admin/restart", want: http.StatusResetContent},
		{path: "/stt/health", want: http.StatusPartialContent},
		{path: "/stt/file", want: http.StatusAlreadyReported},
		{path: "/stt/chat-input", want: http.StatusMultiStatus},
		{path: "/stt", want: http.StatusIMUsed},
		{path: "/stt-ws", want: http.StatusIMUsed},
		{path: "/ws", want: http.StatusIMUsed},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d", rec.Code, tt.want)
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stt/chat-input", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
