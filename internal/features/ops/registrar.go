package ops

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups cross-cutting Ops, task, backlog, and scheduler handlers.
// The handlers are supplied by cmd/rencrow.
type Routes struct {
	Status            http.HandlerFunc
	Agents            http.HandlerFunc
	AgentDetail       http.HandlerFunc
	Jobs              http.HandlerFunc
	Tasks             http.HandlerFunc
	TaskDetail        http.HandlerFunc
	TaskNotifications http.HandlerFunc
	Logs              http.HandlerFunc
	PromptDebug       http.HandlerFunc
	AuditSummary      http.HandlerFunc
	JobDetail         http.HandlerFunc
	RepairRun         http.HandlerFunc
	Backlog           http.HandlerFunc
	Scheduler         http.HandlerFunc

	// CMD が CORE Public API 経由で診断するための読み取り専用 route。
	// 送信やフェッチなど副作用のある操作は含めない。
	ChannelsList    http.HandlerFunc
	ChannelsProbe   http.HandlerFunc
	WebGatherDoctor http.HandlerFunc
	AgentOps        http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/status", routes.Status)
	registerRoute(mux, "/viewer/agents", routes.Agents)
	registerRoute(mux, "/viewer/agent/detail", routes.AgentDetail)
	registerRoute(mux, "/viewer/jobs", routes.Jobs)
	registerRoute(mux, "/viewer/tasks", routes.Tasks)
	registerRoute(mux, "/viewer/task/detail", routes.TaskDetail)
	registerRoute(mux, "/viewer/task-notifications", routes.TaskNotifications)
	registerRoute(mux, "/viewer/logs", routes.Logs)
	registerRoute(mux, "/viewer/prompt-debug", routes.PromptDebug)
	registerRoute(mux, "/viewer/audit/summary", routes.AuditSummary)
	registerRoute(mux, "/viewer/job/detail", routes.JobDetail)
	registerRoute(mux, "/viewer/repair/run", routes.RepairRun)
	registerRoute(mux, "/viewer/backlog", routes.Backlog)
	registerRoute(mux, "/viewer/scheduler", routes.Scheduler)
	registerRoute(mux, "/viewer/channels", routes.ChannelsList)
	registerRoute(mux, "/viewer/channels/probe", routes.ChannelsProbe)
	registerRoute(mux, "/viewer/web-gather/doctor", routes.WebGatherDoctor)
	registerRoute(mux, "/v1/agent/ops", routes.AgentOps)
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
