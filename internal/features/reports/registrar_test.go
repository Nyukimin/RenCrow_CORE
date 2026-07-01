package reports

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersReportPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		EvidenceRecent:      statusHandler(http.StatusOK),
		EvidenceDetail:      statusHandler(http.StatusCreated),
		EvidenceSummary:     statusHandler(http.StatusAccepted),
		VerificationRecent:  statusHandler(http.StatusNoContent),
		VerificationDetail:  statusHandler(http.StatusPartialContent),
		VerificationSummary: statusHandler(http.StatusResetContent),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/evidence/recent", want: http.StatusOK},
		{path: "/viewer/evidence/detail", want: http.StatusCreated},
		{path: "/viewer/evidence/summary", want: http.StatusAccepted},
		{path: "/viewer/verification/recent", want: http.StatusNoContent},
		{path: "/viewer/verification/detail", want: http.StatusPartialContent},
		{path: "/viewer/verification/summary", want: http.StatusResetContent},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
