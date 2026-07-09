package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func recordLeadAgentRunStarted(ctx context.Context, recorder SuperAgentRuntimeRecorder, req ProcessMessageRequest, jobID task.JobID, route routing.Route) (time.Time, error) {
	startedAt := time.Now().UTC()
	if recorder == nil {
		return startedAt, nil
	}
	run := domainsuperagent.AgentRun{
		RunID:        leadAgentRunID(jobID),
		WorkstreamID: req.SessionID,
		AgentType:    "LeadAgent",
		Goal:         req.UserMessage,
		Status:       "running",
		StartedAt:    startedAt,
		Summary:      fmt.Sprintf("route=%s", route),
	}
	if err := recorder.SaveAgentRun(ctx, run); err != nil {
		return startedAt, fmt.Errorf("failed to save lead agent run start: %w", err)
	}
	trace := domainsuperagent.TraceEvent{
		EventID:        leadAgentTraceEventID("started", jobID, startedAt),
		RunID:          run.RunID,
		EventType:      "lead_agent_started",
		Actor:          "LeadAgent",
		PayloadSummary: fmt.Sprintf("route=%s", route),
		Status:         "running",
		CreatedAt:      startedAt,
	}
	if err := recorder.SaveTraceEvent(ctx, trace); err != nil {
		return startedAt, fmt.Errorf("failed to save lead agent trace start: %w", err)
	}
	pack := domainsuperagent.ContextPack{
		ContextPackID:   leadAgentContextPackID(jobID),
		RunID:           run.RunID,
		WorkstreamID:    req.SessionID,
		Summary:         fmt.Sprintf("route=%s channel=%s chat_id=%s user_message=%s", route, req.Channel, req.ChatID, req.UserMessage),
		IncludedSources: []string{"session:" + req.SessionID, "channel:" + req.Channel, "route:" + string(route)},
		TokenEstimate:   estimateRuntimeContextTokens(req.UserMessage),
		CreatedAt:       startedAt,
	}
	if err := recorder.SaveContextPack(ctx, pack); err != nil {
		return startedAt, fmt.Errorf("failed to save lead agent context pack: %w", err)
	}
	return startedAt, nil
}

func recordLeadAgentRunFinished(ctx context.Context, recorder SuperAgentRuntimeRecorder, req ProcessMessageRequest, jobID task.JobID, route routing.Route, startedAt time.Time, status string, summary string) error {
	if recorder == nil {
		return nil
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	completedAt := time.Now().UTC()
	run := domainsuperagent.AgentRun{
		RunID:        leadAgentRunID(jobID),
		WorkstreamID: req.SessionID,
		AgentType:    "LeadAgent",
		Goal:         req.UserMessage,
		Status:       status,
		StartedAt:    startedAt,
		CompletedAt:  completedAt,
		Summary:      summary,
	}
	if err := recorder.SaveAgentRun(ctx, run); err != nil {
		return fmt.Errorf("failed to save lead agent run %s: %w", status, err)
	}
	traceStatus := status
	if traceStatus == "completed" {
		traceStatus = "completed"
	}
	trace := domainsuperagent.TraceEvent{
		EventID:        leadAgentTraceEventID(status, jobID, completedAt),
		RunID:          run.RunID,
		EventType:      "lead_agent_" + status,
		Actor:          "LeadAgent",
		PayloadSummary: fmt.Sprintf("route=%s %s", route, summary),
		Status:         traceStatus,
		CreatedAt:      completedAt,
	}
	if err := recorder.SaveTraceEvent(ctx, trace); err != nil {
		return fmt.Errorf("failed to save lead agent trace %s: %w", status, err)
	}
	return nil
}

func leadAgentRunID(jobID task.JobID) string {
	return "run_lead_" + jobID.String()
}

func leadAgentTraceEventID(status string, jobID task.JobID, at time.Time) string {
	return fmt.Sprintf("evt_lead_%s_%s_%d", status, jobID.String(), at.UnixNano())
}

func leadAgentContextPackID(jobID task.JobID) string {
	return "ctx_lead_" + jobID.String()
}

func estimateRuntimeContextTokens(text string) int {
	if text == "" {
		return 1
	}
	estimate := len([]rune(text)) / 4
	if estimate < 1 {
		return 1
	}
	return estimate
}
