package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	runtimeSuperAgentTraceEventIDPrefix = "super-agent-trace/sha256:"
	runtimeAIWorkflowEventIDPrefix      = "ai-workflow-event/sha256:"
)

type runtimeSuperAgentTraceStore interface {
	FindAgentRunByID(context.Context, string) (domainsuperagent.AgentRun, bool, error)
	FindTraceEventByID(context.Context, string) (domainsuperagent.TraceEvent, bool, error)
	SaveTraceEvent(context.Context, domainsuperagent.TraceEvent) error
}

type runtimeAIWorkflowEventStore interface {
	FindWorkflowEventByID(context.Context, string) (domainai.WorkflowEvent, bool, error)
	SaveWorkflowEvent(context.Context, domainai.WorkflowEvent) error
}

type runtimeSuperAgentTraceWritePayload struct {
	RunID          string `json:"run_id"`
	EventType      string `json:"event_type"`
	Status         string `json:"status"`
	ParentEventID  string `json:"parent_event_id,omitempty"`
	PayloadSummary string `json:"payload_summary,omitempty"`
}

type runtimeAIWorkflowEventWritePayload struct {
	EventType     string `json:"event_type"`
	Status        string `json:"status"`
	ParentEventID string `json:"parent_event_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	WorkstreamID  string `json:"workstream_id,omitempty"`
	Repo          string `json:"repo,omitempty"`
	WorktreeID    string `json:"worktree_id,omitempty"`
	CommandName   string `json:"command_name,omitempty"`
	SkillName     string `json:"skill_name,omitempty"`
	Summary       string `json:"summary,omitempty"`
}

type runtimeSuperAgentTraceWriter struct {
	mu    sync.Mutex
	store runtimeSuperAgentTraceStore
}

type runtimeAIWorkflowEventWriter struct {
	mu    sync.Mutex
	store runtimeAIWorkflowEventStore
}

func registerRuntimeDataWriteSuperAgentHarness(r *runtimeDataWriteRegistry, store runtimeSuperAgentTraceStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("super agent harness trace write unavailable")
	}
	writer := &runtimeSuperAgentTraceWriter{store: store}
	return r.RegisterWithContract("super_agent_harness", "record_trace_event", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id", "event_type", "status"},
		OptionalPayloadFields: []string{"parent_event_id", "payload_summary"},
	}, writer.write)
}

func registerRuntimeDataWriteAIWorkflow(r *runtimeDataWriteRegistry, store runtimeAIWorkflowEventStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("ai workflow event write unavailable")
	}
	writer := &runtimeAIWorkflowEventWriter{store: store}
	return r.RegisterWithContract("ai_workflow", "record_workflow_event", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"event_type", "status"},
		OptionalPayloadFields: []string{"parent_event_id", "run_id", "workstream_id", "repo", "worktree_id", "command_name", "skill_name", "summary"},
	}, writer.write)
}

func (w *runtimeSuperAgentTraceWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeSuperAgentTracePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	run, found, err := w.store.FindAgentRunByID(ctx, payload.RunID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent run %q is not found", payload.RunID)
	}
	if err := domainsuperagent.ValidateAgentRun(run); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent run %q is invalid: %w", payload.RunID, err)
	}
	if run.RunID != payload.RunID {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent run ID %q does not match requested run %q", run.RunID, payload.RunID)
	}
	if payload.ParentEventID != "" {
		parent, found, err := w.store.FindTraceEventByID(ctx, payload.ParentEventID)
		if err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		if !found {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent parent trace event %q is not found", payload.ParentEventID)
		}
		if err := domainsuperagent.ValidateTraceEvent(parent); err != nil {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent parent trace event %q is invalid: %w", payload.ParentEventID, err)
		}
		if parent.EventID != payload.ParentEventID {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent parent trace event ID %q does not match requested parent %q", parent.EventID, payload.ParentEventID)
		}
		if parent.RunID != payload.RunID {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent parent trace event %q belongs to run %q, not %q", payload.ParentEventID, parent.RunID, payload.RunID)
		}
	}

	now := time.Now().UTC()
	trace := domainsuperagent.TraceEvent{
		EventID:        runtimeDataWriteDerivedID(runtimeSuperAgentTraceEventIDPrefix, scope.RequestID),
		ParentEventID:  payload.ParentEventID,
		RunID:          payload.RunID,
		EventType:      payload.EventType,
		Actor:          strings.TrimSpace(scope.ActorID),
		PayloadSummary: payload.PayloadSummary,
		Status:         payload.Status,
		CreatedAt:      now,
	}
	if err := domainsuperagent.ValidateTraceEvent(trace); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	existing, found, err := w.store.FindTraceEventByID(ctx, trace.EventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeSuperAgentTraceEventsEqual(existing, trace) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("super agent trace event idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "super-agent-trace/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.EventID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveTraceEvent(ctx, trace); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "super-agent-trace/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         trace.EventID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func (w *runtimeAIWorkflowEventWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeAIWorkflowEventPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if payload.ParentEventID != "" {
		parent, found, err := w.store.FindWorkflowEventByID(ctx, payload.ParentEventID)
		if err != nil {
			return runtimeDataWriteOwnerResult{}, err
		}
		if !found {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("ai workflow parent event %q is not found", payload.ParentEventID)
		}
		if err := domainai.ValidateWorkflowEvent(parent); err != nil {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("ai workflow parent event %q is invalid: %w", payload.ParentEventID, err)
		}
		if parent.EventID != payload.ParentEventID {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("ai workflow parent event ID %q does not match requested parent %q", parent.EventID, payload.ParentEventID)
		}
		for _, field := range []struct {
			name  string
			child string
			value string
		}{
			{name: "run_id", child: payload.RunID, value: parent.RunID},
			{name: "workstream_id", child: payload.WorkstreamID, value: parent.WorkstreamID},
			{name: "repo", child: payload.Repo, value: parent.Repo},
			{name: "worktree_id", child: payload.WorktreeID, value: parent.WorktreeID},
		} {
			if field.child != "" && field.value != "" && field.child != field.value {
				return runtimeDataWriteOwnerResult{}, fmt.Errorf("ai workflow parent %q %s mismatch", payload.ParentEventID, field.name)
			}
		}
	}

	now := time.Now().UTC()
	event := domainai.WorkflowEvent{
		EventID:       runtimeDataWriteDerivedID(runtimeAIWorkflowEventIDPrefix, scope.RequestID),
		ParentEventID: payload.ParentEventID,
		RunID:         payload.RunID,
		WorkstreamID:  payload.WorkstreamID,
		EventType:     payload.EventType,
		Agent:         strings.TrimSpace(scope.ActorID),
		Repo:          payload.Repo,
		WorktreeID:    payload.WorktreeID,
		CommandName:   payload.CommandName,
		SkillName:     payload.SkillName,
		Status:        payload.Status,
		CreatedAt:     now,
		Summary:       payload.Summary,
	}
	if isRuntimeAIWorkflowTerminalStatus(event.Status) {
		event.CompletedAt = now
	}
	if err := domainai.ValidateWorkflowEvent(event); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	existing, found, err := w.store.FindWorkflowEventByID(ctx, event.EventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeAIWorkflowEventsEqual(existing, event) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("ai workflow event idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "ai-workflow-event/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.EventID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}
	if err := w.store.SaveWorkflowEvent(ctx, event); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "ai-workflow-event/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         event.EventID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimeSuperAgentTracePayload(payload map[string]any) (runtimeSuperAgentTraceWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"run_id": {}, "event_type": {}, "status": {}, "parent_event_id": {}, "payload_summary": {},
	}); err != nil {
		return runtimeSuperAgentTraceWritePayload{}, err
	}
	var decoded runtimeSuperAgentTraceWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeSuperAgentTraceWritePayload{}, err
	}
	decoded.RunID = strings.TrimSpace(decoded.RunID)
	decoded.EventType = strings.TrimSpace(decoded.EventType)
	decoded.Status = strings.TrimSpace(decoded.Status)
	decoded.ParentEventID = strings.TrimSpace(decoded.ParentEventID)
	decoded.PayloadSummary = strings.TrimSpace(decoded.PayloadSummary)
	if decoded.RunID == "" {
		return runtimeSuperAgentTraceWritePayload{}, fmt.Errorf("run_id is required")
	}
	if decoded.EventType == "" {
		return runtimeSuperAgentTraceWritePayload{}, fmt.Errorf("event_type is required")
	}
	if decoded.Status == "" {
		return runtimeSuperAgentTraceWritePayload{}, fmt.Errorf("status is required")
	}
	return decoded, nil
}

func decodeRuntimeAIWorkflowEventPayload(payload map[string]any) (runtimeAIWorkflowEventWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"event_type": {}, "status": {}, "parent_event_id": {}, "run_id": {}, "workstream_id": {},
		"repo": {}, "worktree_id": {}, "command_name": {}, "skill_name": {}, "summary": {},
	}); err != nil {
		return runtimeAIWorkflowEventWritePayload{}, err
	}
	var decoded runtimeAIWorkflowEventWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeAIWorkflowEventWritePayload{}, err
	}
	decoded.EventType = strings.TrimSpace(decoded.EventType)
	decoded.Status = strings.TrimSpace(decoded.Status)
	decoded.ParentEventID = strings.TrimSpace(decoded.ParentEventID)
	decoded.RunID = strings.TrimSpace(decoded.RunID)
	decoded.WorkstreamID = strings.TrimSpace(decoded.WorkstreamID)
	decoded.Repo = strings.TrimSpace(decoded.Repo)
	decoded.WorktreeID = strings.TrimSpace(decoded.WorktreeID)
	decoded.CommandName = strings.TrimSpace(decoded.CommandName)
	decoded.SkillName = strings.TrimSpace(decoded.SkillName)
	decoded.Summary = strings.TrimSpace(decoded.Summary)
	if decoded.EventType == "" {
		return runtimeAIWorkflowEventWritePayload{}, fmt.Errorf("event_type is required")
	}
	if decoded.Status == "" {
		return runtimeAIWorkflowEventWritePayload{}, fmt.Errorf("status is required")
	}
	return decoded, nil
}

func isRuntimeAIWorkflowTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "rejected", "blocked", "stopped":
		return true
	default:
		return false
	}
}

func runtimeSuperAgentTraceEventsEqual(left, right domainsuperagent.TraceEvent) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeAIWorkflowEventsEqual(left, right domainai.WorkflowEvent) bool {
	left.CreatedAt = time.Time{}
	left.CompletedAt = time.Time{}
	right.CreatedAt = time.Time{}
	right.CompletedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
