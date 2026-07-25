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
