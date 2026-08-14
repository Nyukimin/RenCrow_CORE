package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteSuperAgentHarnessTraceOwnerE2E(t *testing.T) {
	store := superagentpersistence.NewJSONLStore(t.TempDir(), 3000)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	if err := store.SaveAgentRun(ctx, domainsuperagent.AgentRun{
		RunID:     "run-super-1",
		AgentType: "LeadAgent",
		Status:    "running",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("seed agent run: %v", err)
	}
	if err := store.SaveTraceEvent(ctx, domainsuperagent.TraceEvent{
		EventID:   "parent-super-1",
		RunID:     "run-super-1",
		EventType: "run_started",
		Status:    "running",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed parent trace event: %v", err)
	}

	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteSuperAgentHarness(registry, store); err != nil {
		t.Fatalf("register super agent trace write: %v", err)
	}
	assertRuntimeDataWriteEContract(t, registry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "super_agent_harness",
		Operation:             "record_trace_event",
		Access:                dataRecallAccessInternal,
		RequiredPayloadFields: []string{"event_type", "run_id", "status"},
		OptionalPayloadFields: []string{"parent_event_id", "payload_summary"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: registry,
		DisableToolHarness:   true,
	})
	ownerCtx := runtimeDataWriteOwnerContext(t, "super-agent-owner-1", false)
	payload := map[string]any{
		"run_id":          " run-super-1 ",
		"event_type":      " tool_started ",
		"status":          " running ",
		"parent_event_id": " parent-super-1 ",
		"payload_summary": "  inspect source  ",
	}

	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ownerCtx, "super_agent_harness", "record_trace_event", payload)
	if first.IdempotentReplay || first.SchemaVersion != "super-agent-trace/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "super-agent-owner-1" || first.OwnerRoute != "super_agent_harness/record_trace_event" {
		t.Fatalf("first receipt = %#v", first)
	}

	trace, found, err := store.FindTraceEventByID(ctx, first.AuditRef)
	if err != nil || !found {
		t.Fatalf("saved trace = %#v found=%v err=%v", trace, found, err)
	}
	if trace.EventID != first.AuditRef || trace.RunID != "run-super-1" || trace.ParentEventID != "parent-super-1" || trace.EventType != "tool_started" || trace.Status != "running" || trace.Actor != "shiro" || trace.PayloadSummary != "inspect source" || trace.CreatedAt.IsZero() {
		t.Fatalf("saved trace fields = %#v", trace)
	}
	if len(trace.EventID) <= len("super-agent-trace/sha256:") || trace.EventID[:len("super-agent-trace/sha256:")] != "super-agent-trace/sha256:" {
		t.Fatalf("derived trace event ID = %q", trace.EventID)
	}

	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ownerCtx, "super_agent_harness", "record_trace_event", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt = %#v first=%#v", second, first)
	}
	if events, err := store.ListTraceEvents(ctx, 10); err != nil || len(events) != 2 {
		t.Fatalf("replay mutated trace events = %#v err=%v", events, err)
	}

	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["payload_summary"] = "different"
	response, err := worker.ExecuteV2(ownerCtx, "data.write", map[string]any{
		"store": "super_agent_harness", "operation": "record_trace_event", "payload": changed,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("conflicting replay response=%#v err=%v", response, err)
	}
	if events, err := store.ListTraceEvents(ctx, 10); err != nil || len(events) != 2 {
		t.Fatalf("conflicting replay mutated trace events = %#v err=%v", events, err)
	}
}

func TestRuntimeDataWriteSuperAgentHarnessTraceRejectsMissingRunParentAndForbiddenFields(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 5, 10, 0, 0, time.UTC)

	t.Run("missing run", func(t *testing.T) {
		store := superagentpersistence.NewJSONLStore(t.TempDir(), 3000)
		registry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSuperAgentHarness(registry, store); err != nil {
			t.Fatal(err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
		response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "super-agent-missing-run", false), "data.write", map[string]any{
			"store": "super_agent_harness", "operation": "record_trace_event",
			"payload": map[string]any{"run_id": "missing", "event_type": "started", "status": "running"},
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("missing run response=%#v err=%v", response, err)
		}
		if events, err := store.ListTraceEvents(ctx, 10); err != nil || len(events) != 0 {
			t.Fatalf("missing run mutated events=%#v err=%v", events, err)
		}
	})

	t.Run("missing parent and parent run mismatch", func(t *testing.T) {
		store := superagentpersistence.NewJSONLStore(t.TempDir(), 3000)
		if err := store.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: "run-super-2", AgentType: "LeadAgent", Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveTraceEvent(ctx, domainsuperagent.TraceEvent{EventID: "parent-wrong-run", RunID: "other-run", EventType: "started", Status: "running", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		registry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSuperAgentHarness(registry, store); err != nil {
			t.Fatal(err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
		base := map[string]any{"run_id": "run-super-2", "event_type": "started", "status": "running"}
		for _, parentID := range []string{"missing-parent", "parent-wrong-run"} {
			payload := runtimeDataWriteOwnerClonePayload(base)
			payload["parent_event_id"] = parentID
			response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "super-agent-parent-"+parentID, false), "data.write", map[string]any{
				"store": "super_agent_harness", "operation": "record_trace_event", "payload": payload,
			})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("parent %q response=%#v err=%v", parentID, response, err)
			}
		}
		events, err := store.ListTraceEvents(ctx, 10)
		if err != nil || len(events) != 1 || events[0].EventID != "parent-wrong-run" {
			t.Fatalf("parent rejection mutated events=%#v err=%v", events, err)
		}
	})

	t.Run("forbidden fields", func(t *testing.T) {
		store := superagentpersistence.NewJSONLStore(t.TempDir(), 3000)
		if err := store.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: "run-super-3", AgentType: "LeadAgent", Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		registry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSuperAgentHarness(registry, store); err != nil {
			t.Fatal(err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
		for _, key := range []string{"actor", "agent", "event_id", "created_at", "request_id", "path", "branch", "unknown"} {
			payload := map[string]any{"run_id": "run-super-3", "event_type": "started", "status": "running", key: "model-owned"}
			response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "super-agent-forbidden-"+key, false), "data.write", map[string]any{
				"store": "super_agent_harness", "operation": "record_trace_event", "payload": payload,
			})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("forbidden field %q response=%#v err=%v", key, response, err)
			}
		}
		if events, err := store.ListTraceEvents(ctx, 10); err != nil || len(events) != 0 {
			t.Fatalf("forbidden payload mutated events=%#v err=%v", events, err)
		}
	})
}

func TestRuntimeDataWriteAIWorkflowEventOwnerE2E(t *testing.T) {
	store := aiworkflowpersistence.NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 6, 0, 0, 0, time.UTC)
	if err := store.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{
		EventID:      "parent-ai-1",
		RunID:        "run-ai-1",
		WorkstreamID: "ws-ai-1",
		EventType:    "run_started",
		Agent:        "mio",
		Repo:         "repo-ai",
		WorktreeID:   "wt-ai-1",
		Status:       "running",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("seed parent workflow event: %v", err)
	}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteAIWorkflow(registry, store); err != nil {
		t.Fatalf("register ai workflow write: %v", err)
	}
	assertRuntimeDataWriteEContract(t, registry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "ai_workflow",
		Operation:             "record_workflow_event",
		Access:                dataRecallAccessInternal,
		RequiredPayloadFields: []string{"event_type", "status"},
		OptionalPayloadFields: []string{"command_name", "parent_event_id", "repo", "run_id", "skill_name", "summary", "workstream_id", "worktree_id"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ownerCtx := runtimeDataWriteOwnerContext(t, "ai-workflow-owner-1", false)
	payload := map[string]any{
		"event_type":      " command_started ",
		"status":          " running ",
		"parent_event_id": " parent-ai-1 ",
		"run_id":          " run-ai-1 ",
		"workstream_id":   " ws-ai-1 ",
		"repo":            " repo-ai ",
		"worktree_id":     " wt-ai-1 ",
		"command_name":    " /review ",
		"skill_name":      " audit ",
		"summary":         "  command started  ",
	}
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ownerCtx, "ai_workflow", "record_workflow_event", payload)
	if first.IdempotentReplay || first.SchemaVersion != "ai-workflow-event/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "ai-workflow-owner-1" || first.OwnerRoute != "ai_workflow/record_workflow_event" {
		t.Fatalf("first AI workflow receipt = %#v", first)
	}
	event, found, err := store.FindWorkflowEventByID(ctx, first.AuditRef)
	if err != nil || !found {
		t.Fatalf("saved workflow event=%#v found=%v err=%v", event, found, err)
	}
	if event.EventID != first.AuditRef || event.ParentEventID != "parent-ai-1" || event.RunID != "run-ai-1" || event.WorkstreamID != "ws-ai-1" || event.EventType != "command_started" || event.Agent != "shiro" || event.Repo != "repo-ai" || event.WorktreeID != "wt-ai-1" || event.CommandName != "/review" || event.SkillName != "audit" || event.Status != "running" || event.Summary != "command started" || event.CreatedAt.IsZero() || !event.CompletedAt.IsZero() {
		t.Fatalf("saved workflow event fields=%#v", event)
	}
	if len(event.EventID) <= len("ai-workflow-event/sha256:") || event.EventID[:len("ai-workflow-event/sha256:")] != "ai-workflow-event/sha256:" {
		t.Fatalf("derived workflow event ID=%q", event.EventID)
	}

	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ownerCtx, "ai_workflow", "record_workflow_event", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("AI workflow replay receipt=%#v first=%#v", second, first)
	}
	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["summary"] = "different"
	response, err := worker.ExecuteV2(ownerCtx, "data.write", map[string]any{
		"store": "ai_workflow", "operation": "record_workflow_event", "payload": changed,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("AI workflow conflicting replay response=%#v err=%v", response, err)
	}
	if events, err := store.ListWorkflowEvents(ctx, 10); err != nil || len(events) != 2 {
		t.Fatalf("AI workflow conflict mutated events=%#v err=%v", events, err)
	}

	terminalPayload := map[string]any{"event_type": "command_finished", "status": "completed"}
	terminal := runtimeDataWriteOwnerExecuteWrite(t, worker, runtimeDataWriteOwnerContext(t, "ai-workflow-terminal-1", false), "ai_workflow", "record_workflow_event", terminalPayload)
	terminalEvent, found, err := store.FindWorkflowEventByID(ctx, terminal.AuditRef)
	if err != nil || !found || terminalEvent.Status != "completed" || terminalEvent.CompletedAt.IsZero() {
		t.Fatalf("terminal workflow event=%#v found=%v err=%v", terminalEvent, found, err)
	}
}

func TestRuntimeDataWriteAIWorkflowEventRejectsMissingParentMismatchAndForbiddenFields(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 6, 10, 0, 0, time.UTC)
	store := aiworkflowpersistence.NewJSONLStore(t.TempDir())
	if err := store.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{
		EventID:      "parent-ai-2",
		RunID:        "run-ai-2",
		WorkstreamID: "ws-ai-2",
		EventType:    "started",
		Repo:         "repo-ai-2",
		WorktreeID:   "wt-ai-2",
		Status:       "running",
		CreatedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteAIWorkflow(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})

	base := map[string]any{"event_type": "child", "status": "running", "parent_event_id": "parent-ai-2"}
	for index, field := range []string{"run_id", "workstream_id", "repo", "worktree_id"} {
		payload := runtimeDataWriteOwnerClonePayload(base)
		payload[field] = "mismatch-" + field
		response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "ai-workflow-mismatch-"+field, false), "data.write", map[string]any{
			"store": "ai_workflow", "operation": "record_workflow_event", "payload": payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("parent mismatch %d field=%q response=%#v err=%v", index, field, response, err)
		}
	}
	missingParent := runtimeDataWriteOwnerClonePayload(base)
	missingParent["parent_event_id"] = "missing-parent"
	response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "ai-workflow-missing-parent", false), "data.write", map[string]any{
		"store": "ai_workflow", "operation": "record_workflow_event", "payload": missingParent,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("missing parent response=%#v err=%v", response, err)
	}
	if events, err := store.ListWorkflowEvents(ctx, 20); err != nil || len(events) != 1 {
		t.Fatalf("parent rejection mutated events=%#v err=%v", events, err)
	}

	for _, key := range []string{"agent", "event_id", "created_at", "completed_at", "input_tokens", "context_tokens", "estimated_cost", "latency_ms", "file_path", "path", "branch", "target_branch", "unknown"} {
		payload := map[string]any{"event_type": "child", "status": "running", key: "model-owned"}
		response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "ai-workflow-forbidden-"+key, false), "data.write", map[string]any{
			"store": "ai_workflow", "operation": "record_workflow_event", "payload": payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", key, response, err)
		}
	}
	if events, err := store.ListWorkflowEvents(ctx, 20); err != nil || len(events) != 1 {
		t.Fatalf("forbidden payload mutated events=%#v err=%v", events, err)
	}
}

func assertRuntimeDataWriteEContract(t *testing.T, routes []runtimeDataWriteRoute, want runtimeDataWriteRoute) {
	t.Helper()
	if len(routes) != 1 || routes[0].Store != want.Store || routes[0].Operation != want.Operation || routes[0].Access != want.Access || !reflect.DeepEqual(routes[0].RequiredPayloadFields, want.RequiredPayloadFields) || !reflect.DeepEqual(routes[0].OptionalPayloadFields, want.OptionalPayloadFields) {
		t.Fatalf("routes=%#v want=%#v", routes, want)
	}
}
