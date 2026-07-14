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

// Routes groups Ops, job, backlog, scheduler, workstream, and revenue handlers
// supplied by cmd/rencrow. Handler implementations remain in legacy packages
// during Ver0.80 migration; this registrar owns only route registration.
type Routes struct {
	Status                 http.HandlerFunc
	Agents                 http.HandlerFunc
	AgentDetail            http.HandlerFunc
	Jobs                   http.HandlerFunc
	ParallelJobs           http.HandlerFunc
	ParallelJobDetail      http.HandlerFunc
	JobNotifications       http.HandlerFunc
	Logs                   http.HandlerFunc
	AuditSummary           http.HandlerFunc
	JobDetail              http.HandlerFunc
	RepairRun              http.HandlerFunc
	Backlog                http.HandlerFunc
	Scheduler              http.HandlerFunc
	Workstreams            http.HandlerFunc
	WorkstreamGoals        http.HandlerFunc
	WorkstreamArtifacts    http.HandlerFunc
	WorkstreamAnnotations  http.HandlerFunc
	WorkstreamSteering     http.HandlerFunc
	WorkstreamHeartbeats   http.HandlerFunc
	WorkstreamVaultUpdates http.HandlerFunc
	WorkstreamVaultReview  http.HandlerFunc
	WorkstreamVaultPreview http.HandlerFunc
	Revenue                http.HandlerFunc
	RevenueMarketResearch  http.HandlerFunc
	RevenueSNSPosts        http.HandlerFunc
	RevenueProducts        http.HandlerFunc
	RevenueCustomerVoices  http.HandlerFunc
	RevenueEvents          http.HandlerFunc
	RevenueDecisionGate    http.HandlerFunc
	RevenueDecisionReview  http.HandlerFunc
	RevenueDailyRoutine    http.HandlerFunc
	RevenueChannelDrafts   http.HandlerFunc
	RevenueExternalSend    http.HandlerFunc
	Advisors               http.HandlerFunc
	AdvisorRuns            http.HandlerFunc
	AdvisorScores          http.HandlerFunc
	AgentProfiles          http.HandlerFunc
	AgentPolicyDecisions   http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/status", routes.Status)
	registerRoute(mux, "/viewer/agents", routes.Agents)
	registerRoute(mux, "/viewer/agent/detail", routes.AgentDetail)
	registerRoute(mux, "/viewer/jobs", routes.Jobs)
	registerRoute(mux, "/viewer/parallel-jobs", routes.ParallelJobs)
	registerRoute(mux, "/viewer/parallel-job/detail", routes.ParallelJobDetail)
	registerRoute(mux, "/viewer/job-notifications", routes.JobNotifications)
	registerRoute(mux, "/viewer/logs", routes.Logs)
	registerRoute(mux, "/viewer/audit/summary", routes.AuditSummary)
	registerRoute(mux, "/viewer/job/detail", routes.JobDetail)
	registerRoute(mux, "/viewer/repair/run", routes.RepairRun)
	registerRoute(mux, "/viewer/backlog", routes.Backlog)
	registerRoute(mux, "/viewer/scheduler", routes.Scheduler)
	registerRoute(mux, "/viewer/workstreams", routes.Workstreams)
	registerRoute(mux, "/viewer/workstreams/goals", routes.WorkstreamGoals)
	registerRoute(mux, "/viewer/workstreams/artifacts", routes.WorkstreamArtifacts)
	registerRoute(mux, "/viewer/workstreams/annotations", routes.WorkstreamAnnotations)
	registerRoute(mux, "/viewer/workstreams/steering", routes.WorkstreamSteering)
	registerRoute(mux, "/viewer/workstreams/heartbeats", routes.WorkstreamHeartbeats)
	registerRoute(mux, "/viewer/workstreams/vault-updates", routes.WorkstreamVaultUpdates)
	registerRoute(mux, "/viewer/workstreams/vault-updates/review", routes.WorkstreamVaultReview)
	registerRoute(mux, "/viewer/workstreams/vault-updates/preview", routes.WorkstreamVaultPreview)
	registerRoute(mux, "/viewer/revenue", routes.Revenue)
	registerRoute(mux, "/viewer/revenue/market-research", routes.RevenueMarketResearch)
	registerRoute(mux, "/viewer/revenue/sns-posts", routes.RevenueSNSPosts)
	registerRoute(mux, "/viewer/revenue/products", routes.RevenueProducts)
	registerRoute(mux, "/viewer/revenue/customer-voices", routes.RevenueCustomerVoices)
	registerRoute(mux, "/viewer/revenue/events", routes.RevenueEvents)
	registerRoute(mux, "/viewer/revenue/human-decision-gate", routes.RevenueDecisionGate)
	registerRoute(mux, "/viewer/revenue/human-decision-gate/review", routes.RevenueDecisionReview)
	registerRoute(mux, "/viewer/revenue/daily-routine", routes.RevenueDailyRoutine)
	registerRoute(mux, "/viewer/revenue/channel-drafts", routes.RevenueChannelDrafts)
	registerRoute(mux, "/viewer/revenue/channel-drafts/external-send-apply", routes.RevenueExternalSend)
	registerRoute(mux, "/viewer/advisors", routes.Advisors)
	registerRoute(mux, "/viewer/advisors/runs", routes.AdvisorRuns)
	registerRoute(mux, "/viewer/advisors/scores", routes.AdvisorScores)
	registerRoute(mux, "/viewer/agents/profiles", routes.AgentProfiles)
	registerRoute(mux, "/viewer/agents/policy-decisions", routes.AgentPolicyDecisions)
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
