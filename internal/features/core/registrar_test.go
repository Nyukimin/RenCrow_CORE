package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsHealthPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Live:           statusHandler(http.StatusNoContent),
		Health:         statusHandler(http.StatusOK),
		Ready:          statusHandler(http.StatusAccepted),
		ModuleManifest: statusHandler(http.StatusCreated),
	}})

	for _, tt := range []struct {
		path string
		want int
	}{
		{path: "/health/live", want: http.StatusNoContent},
		{path: "/health", want: http.StatusOK},
		{path: "/ready", want: http.StatusAccepted},
		{path: "/viewer/modules/manifest", want: http.StatusCreated},
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
