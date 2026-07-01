package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersWebBrowserPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		BrowserTraceAPIStatus:          statusHandler(http.StatusOK),
		BrowserTraceAPIDiscover:        statusHandler(http.StatusCreated),
		BrowserTraceAPIValidation:      statusHandler(http.StatusAccepted),
		BrowserTraceAPIFetcherProposal: statusHandler(http.StatusNoContent),
		ComplexityHotspotStatus:        statusHandler(http.StatusPartialContent),
		ComplexityHotspotScan:          statusHandler(http.StatusResetContent),
		ComplexityHotspotProposal:      statusHandler(http.StatusAlreadyReported),
		ComplexityHotspotConcreteDiff:  statusHandler(http.StatusIMUsed),
		ComplexityHotspotCoderDiff:     statusHandler(http.StatusMultiStatus),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/browser-trace-api", want: http.StatusOK},
		{path: "/viewer/browser-trace-api/discover", want: http.StatusCreated},
		{path: "/viewer/browser-trace-api/validations", want: http.StatusAccepted},
		{path: "/viewer/browser-trace-api/fetcher-proposals", want: http.StatusNoContent},
		{path: "/viewer/complexity-hotspots", want: http.StatusPartialContent},
		{path: "/viewer/complexity-hotspots/scan", want: http.StatusResetContent},
		{path: "/viewer/complexity-hotspots/proposals", want: http.StatusAlreadyReported},
		{path: "/viewer/complexity-hotspots/concrete-diffs", want: http.StatusIMUsed},
		{path: "/viewer/complexity-hotspots/coder-diffs", want: http.StatusMultiStatus},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/browser-trace-api", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
