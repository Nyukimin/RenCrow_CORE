package aiworkflow

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersAIWorkflowPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:                  statusHandler(http.StatusOK),
		Event:                   statusHandler(http.StatusCreated),
		ProjectMemory:           statusHandler(http.StatusAccepted),
		Worktree:                statusHandler(http.StatusNoContent),
		Command:                 statusHandler(http.StatusPartialContent),
		CommandRun:              statusHandler(http.StatusResetContent),
		ContextUsage:            statusHandler(http.StatusAlreadyReported),
		ContextBudget:           statusHandler(http.StatusIMUsed),
		ExternalControl:         statusHandler(http.StatusMultiStatus),
		HeavyWorker:             statusHandler(http.StatusBadRequest),
		HeavyRuntimeDiagnostics: statusHandler(http.StatusConflict),
		ProjectInit:             statusHandler(http.StatusForbidden),
		WorktreeCreate:          statusHandler(http.StatusGone),
		WorktreeClose:           statusHandler(http.StatusTeapot),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/ai-workflow", want: http.StatusOK},
		{path: "/viewer/ai-workflow/events", want: http.StatusCreated},
		{path: "/viewer/ai-workflow/project-memory", want: http.StatusAccepted},
		{path: "/viewer/ai-workflow/worktrees", want: http.StatusNoContent},
		{path: "/viewer/ai-workflow/commands", want: http.StatusPartialContent},
		{path: "/viewer/ai-workflow/commands/run", want: http.StatusResetContent},
		{path: "/viewer/ai-workflow/context-usages", want: http.StatusAlreadyReported},
		{path: "/viewer/ai-workflow/context-budget/check", want: http.StatusIMUsed},
		{path: "/viewer/ai-workflow/external-control/check", want: http.StatusMultiStatus},
		{path: "/viewer/ai-workflow/heavy-worker/evaluate", want: http.StatusBadRequest},
		{path: "/viewer/ai-workflow/heavy-worker/runtime-diagnostics", want: http.StatusConflict},
		{path: "/viewer/ai-workflow/project-init", want: http.StatusForbidden},
		{path: "/viewer/ai-workflow/worktrees/create", want: http.StatusGone},
		{path: "/viewer/ai-workflow/worktrees/close", want: http.StatusTeapot},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
