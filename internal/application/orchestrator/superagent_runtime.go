package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type leadAgentRunRecord struct {
	StartedAt      time.Time
	TraceID        modulecore.TraceID
	StartedEventID modulecore.EventID
}

func resolveLeadAgentRun(ctx context.Context, lifecycle *taskLifecycle, taskID modulecore.TaskID, recorder SuperAgentRuntimeRecorder, controller SuperAgentRunController) (modulecore.RunID, error) {
	if lifecycle == nil {
		if recorder != nil || controller != nil {
			return "", fmt.Errorf("active run for task %s is unavailable", taskID)
		}
		return "", nil
	}
	run, err := lifecycle.activeRunForTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("resolve active run for lead agent: %w", err)
	}
	return run.RunID, nil
}

func validateLeadAgentIdentity(taskID modulecore.TaskID, runID modulecore.RunID, actor string) (string, error) {
	if err := taskID.Validate(); err != nil {
		return "", fmt.Errorf("lead agent task identity is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return "", fmt.Errorf("lead agent run identity is invalid: %w", err)
	}
	actorID, err := canonicalCoreActor(actor)
	if err != nil {
		return "", fmt.Errorf("lead agent actor identity is invalid: %w", err)
	}
	if err := domainsuperagent.ValidateActorID(actor); err != nil {
		return "", fmt.Errorf("lead agent actor identity is invalid: %w", err)
	}
	return actorID, nil
}

func recordLeadAgentRunStarted(ctx context.Context, recorder SuperAgentRuntimeRecorder, req ProcessMessageRequest, taskID modulecore.TaskID, runID modulecore.RunID, actor string, route routing.Route) (leadAgentRunRecord, error) {
	startedAt := time.Now().UTC()
	if recorder == nil {
		return leadAgentRunRecord{StartedAt: startedAt}, nil
	}
	actorID, err := validateLeadAgentIdentity(taskID, runID, actor)
	if err != nil {
		return leadAgentRunRecord{}, err
	}
	traceID := modulecore.TraceID(req.TraceID)
	if err := traceID.Validate(); err != nil {
		return leadAgentRunRecord{}, fmt.Errorf("lead agent trace identity is invalid: %w", err)
	}
	checkpointRevision, checkpointSummary, nextAction, checkpointAt := resumeCheckpoint(req, route, startedAt)
	run := domainsuperagent.AgentRun{
		RunID:              runID,
		TaskID:             taskID,
		WorkstreamID:       req.SessionID,
		ActorID:            actorID,
		Goal:               req.UserMessage,
		Status:             "running",
		StartedAt:          startedAt,
		Summary:            fmt.Sprintf("route=%s", route),
		ResumePolicy:       "checkpoint",
		CheckpointRevision: checkpointRevision,
		CheckpointSummary:  checkpointSummary,
		NextAction:         nextAction,
		LastCheckpointAt:   checkpointAt,
	}
	if err := recorder.SaveAgentRun(ctx, run); err != nil {
		return leadAgentRunRecord{}, fmt.Errorf("failed to save lead agent run start: %w", err)
	}
	event := modulecore.NewEventEnvelope(traceID, "", nil, "superagent", "lead_agent.started", startedAt, map[string]any{
		"route": string(route), "status": "running",
	})
	event.TaskID = taskID
	event.RunID = runID
	event.ActorKind = "agent"
	event.ActorID = actorID
	if err := recorder.Append(ctx, event); err != nil {
		return leadAgentRunRecord{}, fmt.Errorf("failed to save lead agent start event: %w", err)
	}
	pack := domainsuperagent.ContextPack{
		ContextPackID:   leadAgentContextPackID(runID),
		TaskID:          taskID,
		RunID:           runID,
		WorkstreamID:    req.SessionID,
		Summary:         fmt.Sprintf("route=%s channel=%s chat_id=%s user_message=%s", route, req.Channel, req.ChatID, req.UserMessage),
		IncludedSources: []string{"session:" + req.SessionID, "channel:" + req.Channel, "route:" + string(route)},
		TokenEstimate:   estimateRuntimeContextTokens(req.UserMessage),
		CreatedAt:       startedAt,
	}
	if err := recorder.SaveContextPack(ctx, pack); err != nil {
		return leadAgentRunRecord{}, fmt.Errorf("failed to save lead agent context pack: %w", err)
	}
	return leadAgentRunRecord{StartedAt: startedAt, TraceID: traceID, StartedEventID: event.EventID}, nil
}

func recordLeadAgentRunFinished(ctx context.Context, recorder SuperAgentRuntimeRecorder, req ProcessMessageRequest, taskID modulecore.TaskID, runID modulecore.RunID, actor string, route routing.Route, record leadAgentRunRecord, status string, summary string) error {
	if recorder == nil {
		return nil
	}
	actorID, err := validateLeadAgentIdentity(taskID, runID, actor)
	if err != nil {
		return err
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	completedAt := time.Now().UTC()
	checkpointRevision, checkpointSummary, nextAction, checkpointAt := resumeCheckpoint(req, route, record.StartedAt)
	run := domainsuperagent.AgentRun{
		RunID:              runID,
		TaskID:             taskID,
		WorkstreamID:       req.SessionID,
		ActorID:            actorID,
		Goal:               req.UserMessage,
		Status:             status,
		StartedAt:          record.StartedAt,
		CompletedAt:        completedAt,
		Summary:            summary,
		ResumePolicy:       "checkpoint",
		CheckpointRevision: checkpointRevision,
		CheckpointSummary:  checkpointSummary,
		NextAction:         nextAction,
		LastCheckpointAt:   checkpointAt,
	}
	if err := recorder.SaveAgentRun(ctx, run); err != nil {
		return fmt.Errorf("failed to save lead agent run %s: %w", status, err)
	}
	if record.TraceID == "" || record.StartedEventID == "" {
		return fmt.Errorf("lead agent event context is missing")
	}
	event := modulecore.NewEventEnvelope(record.TraceID, record.StartedEventID, nil, "superagent", "lead_agent."+status, completedAt, map[string]any{
		"route":  string(route),
		"status": status, "summary": summary,
	})
	event.TaskID = taskID
	event.RunID = runID
	event.ActorKind = "agent"
	event.ActorID = actorID
	if err := recorder.Append(ctx, event); err != nil {
		return fmt.Errorf("failed to save lead agent %s event: %w", status, err)
	}
	return nil
}

func resumeCheckpoint(req ProcessMessageRequest, route routing.Route, fallbackAt time.Time) (int, string, string, time.Time) {
	if req.ResumeCheckpointRevision > 0 && strings.TrimSpace(req.ResumeCheckpointSummary) != "" && strings.TrimSpace(req.ResumeNextAction) != "" {
		return req.ResumeCheckpointRevision, strings.TrimSpace(req.ResumeCheckpointSummary), strings.TrimSpace(req.ResumeNextAction), fallbackAt
	}
	return 1, fmt.Sprintf("request accepted; route=%s", route), "dispatch with the same task_id", fallbackAt
}

func leadAgentContextPackID(runID modulecore.RunID) string {
	return "ctx_lead_" + string(runID)
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
