package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersSourcePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Registry:              statusHandler(http.StatusOK),
		DomainGraphAssertions: statusHandler(http.StatusCreated),
		MovieDomainGraphSync:  statusHandler(http.StatusAccepted),
		HobbyDomainGraphSync:  statusHandler(http.StatusNoContent),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/source-registry", want: http.StatusOK},
		{path: "/viewer/domain-graph/assertions", want: http.StatusCreated},
		{path: "/viewer/movie-catalog/domain-graph-sync", want: http.StatusAccepted},
		{path: "/viewer/hobby-graph/domain-graph-sync", want: http.StatusNoContent},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/source-registry", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
