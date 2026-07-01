package main

import (
	"context"
	"net/http"
	"time"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduleworker "github.com/Nyukimin/picoclaw_multiLLM/modules/worker"
)

func handleModuleWorkerDiagnostics(executor moduleworker.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		if executor == nil {
			http.Error(w, moduleworker.UnavailableExecutorMessage, http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC()
		_ = modulecore.WriteJSON(w, moduleworker.BuildDiagnosticsSnapshot(r.Context(), executor, now))
	}
}

type nilWorkerExecutor struct{}

func (nilWorkerExecutor) Health(ctx context.Context) modulecore.HealthReport {
	return moduleworker.UnavailableExecutor{}.Health(ctx)
}

func (nilWorkerExecutor) Execute(ctx context.Context, action moduleworker.Action) (moduleworker.Result, error) {
	return moduleworker.UnavailableExecutor{}.Execute(ctx, action)
}
