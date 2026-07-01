package viewer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterBaseRoutesRegistersViewerBasePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBaseRoutes(mux, Dependencies{Base: BaseRoutes{
		Page:          statusHandler(http.StatusOK),
		Asset:         statusHandler(http.StatusCreated),
		RuntimeConfig: statusHandler(http.StatusAccepted),
		Events:        statusHandler(http.StatusNoContent),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer", want: http.StatusOK},
		{path: "/viewer/assets/js/viewer.js", want: http.StatusCreated},
		{path: "/viewer/runtime-config", want: http.StatusAccepted},
		{path: "/viewer/events", want: http.StatusNoContent},
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

func TestRegisterBaseRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBaseRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
