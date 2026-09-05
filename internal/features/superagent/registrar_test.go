package superagent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersSuperAgentPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:         statusHandler(http.StatusOK),
		RunPause:       statusHandler(http.StatusAccepted),
		RunResume:      statusHandler(http.StatusNoContent),
		MessageChannel: statusHandler(http.StatusBadRequest),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/superagent", want: http.StatusOK},
		{path: "/viewer/superagent/runs/pause", want: http.StatusAccepted},
		{path: "/viewer/superagent/runs/resume", want: http.StatusNoContent},
		{path: "/viewer/superagent/message-channels", want: http.StatusBadRequest},
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

func TestRegisterRoutesOmitsProjectionMutationRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:         statusHandler(http.StatusOK),
		RunPause:       statusHandler(http.StatusAccepted),
		RunResume:      statusHandler(http.StatusNoContent),
		MessageChannel: statusHandler(http.StatusBadRequest),
	}})

	for _, path := range []string{
		"/viewer/superagent/runs",
		"/viewer/superagent/run-queue",
		"/viewer/superagent/run-queue/claim",
		"/viewer/superagent/run-queue/complete",
		"/viewer/superagent/subagent-tasks",
		"/viewer/superagent/context-packs",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/superagent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
