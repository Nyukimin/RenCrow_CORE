package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsViewerSend(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Send: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) },
	}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/send", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusAccepted)
	}
}

func TestRegisterRoutesOwnsViewerSpeechCancel(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Send:         func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) },
		SpeechCancel: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNoContent)
	}
}

func TestRegisterRoutesOwnsViewerSpeechProgress(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Send:           func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) },
		SpeechCancel:   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		SpeechProgress: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/tts/progress", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNoContent)
	}
}
