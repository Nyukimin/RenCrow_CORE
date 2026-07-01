package aiworkflow

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups AI Workflow route handlers supplied by cmd/picoclaw.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Status                  http.HandlerFunc
	Event                   http.HandlerFunc
	ProjectMemory           http.HandlerFunc
	Worktree                http.HandlerFunc
	Command                 http.HandlerFunc
	CommandRun              http.HandlerFunc
	ContextUsage            http.HandlerFunc
	ContextBudget           http.HandlerFunc
	ExternalControl         http.HandlerFunc
	HeavyWorker             http.HandlerFunc
	HeavyRuntimeDiagnostics http.HandlerFunc
	ProjectInit             http.HandlerFunc
	WorktreeCreate          http.HandlerFunc
	WorktreeClose           http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/ai-workflow", routes.Status)
	registerRoute(mux, "/viewer/ai-workflow/events", routes.Event)
	registerRoute(mux, "/viewer/ai-workflow/project-memory", routes.ProjectMemory)
	registerRoute(mux, "/viewer/ai-workflow/worktrees", routes.Worktree)
	registerRoute(mux, "/viewer/ai-workflow/commands", routes.Command)
	registerRoute(mux, "/viewer/ai-workflow/commands/run", routes.CommandRun)
	registerRoute(mux, "/viewer/ai-workflow/context-usages", routes.ContextUsage)
	registerRoute(mux, "/viewer/ai-workflow/context-budget/check", routes.ContextBudget)
	registerRoute(mux, "/viewer/ai-workflow/external-control/check", routes.ExternalControl)
	registerRoute(mux, "/viewer/ai-workflow/heavy-worker/evaluate", routes.HeavyWorker)
	registerRoute(mux, "/viewer/ai-workflow/heavy-worker/runtime-diagnostics", routes.HeavyRuntimeDiagnostics)
	registerRoute(mux, "/viewer/ai-workflow/project-init", routes.ProjectInit)
	registerRoute(mux, "/viewer/ai-workflow/worktrees/create", routes.WorktreeCreate)
	registerRoute(mux, "/viewer/ai-workflow/worktrees/close", routes.WorktreeClose)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
