package workstream

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsWorkstreamPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status: statusHandler(http.StatusOK), Goals: statusHandler(http.StatusCreated),
		VaultPreview: statusHandler(http.StatusAccepted),
	}})
	for _, tt := range []struct {
		path string
		want int
	}{
		{"/viewer/workstreams", http.StatusOK},
		{"/viewer/workstreams/goals", http.StatusCreated},
		{"/viewer/workstreams/vault-updates/preview", http.StatusAccepted},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.want {
			t.Fatalf("%s status=%d want=%d", tt.path, rec.Code, tt.want)
		}
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
}
