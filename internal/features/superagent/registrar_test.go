package superagent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersSuperAgentPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:           statusHandler(http.StatusOK),
		Run:              statusHandler(http.StatusCreated),
		RunPause:         statusHandler(http.StatusAccepted),
		RunResume:        statusHandler(http.StatusNoContent),
		RunQueue:         statusHandler(http.StatusPartialContent),
		RunQueueClaim:    statusHandler(http.StatusResetContent),
		RunQueueComplete: statusHandler(http.StatusAlreadyReported),
		SubagentTask:     statusHandler(http.StatusIMUsed),
		ContextPack:      statusHandler(http.StatusMultiStatus),
		MessageChannel:   statusHandler(http.StatusBadRequest),
		TraceEvent:       statusHandler(http.StatusConflict),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/superagent", want: http.StatusOK},
		{path: "/viewer/superagent/runs", want: http.StatusCreated},
		{path: "/viewer/superagent/runs/pause", want: http.StatusAccepted},
		{path: "/viewer/superagent/runs/resume", want: http.StatusNoContent},
		{path: "/viewer/superagent/run-queue", want: http.StatusPartialContent},
		{path: "/viewer/superagent/run-queue/claim", want: http.StatusResetContent},
		{path: "/viewer/superagent/run-queue/complete", want: http.StatusAlreadyReported},
		{path: "/viewer/superagent/subagent-tasks", want: http.StatusIMUsed},
		{path: "/viewer/superagent/context-packs", want: http.StatusMultiStatus},
		{path: "/viewer/superagent/message-channels", want: http.StatusBadRequest},
		{path: "/viewer/superagent/trace-events", want: http.StatusConflict},
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
