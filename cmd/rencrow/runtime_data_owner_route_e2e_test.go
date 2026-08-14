package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestOwnerRouteWriteRecallE2E(t *testing.T) {
	t.Run("sandbox promotion gate", func(t *testing.T) {
		root := t.TempDir()
		store := seedRuntimeSandboxPromotionStore(t, root, domainsandbox.SandboxStatusActive)
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSandbox(writeRegistry, store); err != nil {
			t.Fatalf("register sandbox write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallSandboxPromotionGates(recallRegistry, store); err != nil {
			t.Fatalf("register sandbox recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		requestID := "owner-route-sandbox-recall-1"
		ctx := runtimeDataWriteOwnerContext(t, requestID, false)
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "sandbox", "create_promotion_gate", runtimeDataWriteSandboxPayload())
		if first.IdempotentReplay || first.AuditRef == "" || first.OwnerRoute != "sandbox/create_promotion_gate" {
			t.Fatalf("sandbox write receipt=%#v", first)
		}
		result := ownerRouteRecallByAuditRef(t, worker, ctx, "sandbox", "promotion_gates", first.AuditRef, requestID)
		record := result.Records[0]
		if record["event_id"] != first.AuditRef || record["promotion_id"] != runtimeDataWriteDerivedID(runtimeSandboxPromotionIDPrefix, requestID) || record["requested_by"] != "shiro" || record["gate_status"] != domainsandbox.GateStatusPassed {
			t.Fatalf("sandbox recalled record=%#v", record)
		}
		assertOwnerRouteRecordDoesNotContain(t, record,
			"workspace/sandbox-owner-1/target.go",
			"workspace/sandbox-owner-1/change.diff",
			"workspace/sandbox-owner-1/test.txt",
			"workspace/sandbox-owner-1/rollback.md",
			"workspace/sandbox-owner-1/post-apply.txt",
		)
	})

	t.Run("complexity concrete diff review", func(t *testing.T) {
		store := complexitypersistence.NewJSONLStore(t.TempDir())
		if err := store.SaveHotspot(context.Background(), runtimeDataWriteComplexityHotspot()); err != nil {
			t.Fatalf("seed complexity hotspot: %v", err)
		}
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteComplexityHotspot(writeRegistry, store); err != nil {
			t.Fatalf("register complexity write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallComplexityReviews(recallRegistry, store); err != nil {
			t.Fatalf("register complexity recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		requestID := "owner-route-complexity-recall-1"
		ctx := runtimeDataWriteOwnerContext(t, requestID, false)
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "complexity_hotspot", "record_concrete_diff_review", map[string]any{
			"hotspot_id": "hotspot-owner-1", "concrete_diff": runtimeDataWriteComplexityDiff(),
		})
		if first.IdempotentReplay || first.AuditRef == "" || first.OwnerRoute != "complexity_hotspot/record_concrete_diff_review" {
			t.Fatalf("complexity write receipt=%#v", first)
		}
		result := ownerRouteRecallByAuditRef(t, worker, ctx, "complexity_hotspot", "concrete_diff_reviews", first.AuditRef, requestID)
		record := result.Records[0]
		if record["artifact_id"] != first.AuditRef || record["artifact_type"] != "complexity_concrete_diff_review" || record["status"] != "generated" || record["scan_id"] != "scan-owner-1" {
			t.Fatalf("complexity recalled record=%#v", record)
		}
	})

	t.Run("super agent trace event", func(t *testing.T) {
		store := superagentpersistence.NewJSONLStore(t.TempDir(), 3000)
		now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
		if err := store.SaveAgentRun(context.Background(), domainsuperagent.AgentRun{
			RunID: "run-owner-route", AgentType: "LeadAgent", Status: "running", StartedAt: now,
		}); err != nil {
			t.Fatalf("seed super agent run: %v", err)
		}
		if err := store.SaveTraceEvent(context.Background(), domainsuperagent.TraceEvent{
			EventID: "parent-owner-route", RunID: "run-owner-route", EventType: "run_started", Status: "running", CreatedAt: now,
		}); err != nil {
			t.Fatalf("seed super agent parent: %v", err)
		}
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSuperAgentHarness(writeRegistry, store); err != nil {
			t.Fatalf("register super agent write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallSuperAgentTraceEvents(recallRegistry, store); err != nil {
			t.Fatalf("register super agent recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		requestID := "owner-route-super-agent-recall-1"
		ctx := runtimeDataWriteOwnerContext(t, requestID, false)
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "super_agent_harness", "record_trace_event", map[string]any{
			"run_id": "run-owner-route", "event_type": "tool_started", "status": "running", "parent_event_id": "parent-owner-route", "payload_summary": "inspect source",
		})
		if first.IdempotentReplay || first.AuditRef == "" || first.OwnerRoute != "super_agent_harness/record_trace_event" {
			t.Fatalf("super agent write receipt=%#v", first)
		}
		result := ownerRouteRecallByAuditRef(t, worker, ctx, "super_agent_harness", "trace_events", first.AuditRef, requestID)
		record := result.Records[0]
		if record["event_id"] != first.AuditRef || record["run_id"] != "run-owner-route" || record["parent_event_id"] != "parent-owner-route" || record["actor"] != "shiro" || record["status"] != "running" {
			t.Fatalf("super agent recalled record=%#v", record)
		}
	})

	t.Run("AI workflow event", func(t *testing.T) {
		store := aiworkflowpersistence.NewJSONLStore(t.TempDir())
		now := time.Date(2026, 8, 14, 6, 0, 0, 0, time.UTC)
		if err := store.SaveWorkflowEvent(context.Background(), domainai.WorkflowEvent{
			EventID: "parent-ai-owner-route", RunID: "run-ai-owner-route", WorkstreamID: "ws-owner-route", EventType: "run_started",
			Agent: "mio", Repo: "repo-owner-route", WorktreeID: "wt-owner-route", Status: "running", CreatedAt: now,
		}); err != nil {
			t.Fatalf("seed AI workflow parent: %v", err)
		}
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteAIWorkflow(writeRegistry, store); err != nil {
			t.Fatalf("register AI workflow write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallAIWorkflowEvents(recallRegistry, store); err != nil {
			t.Fatalf("register AI workflow recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		requestID := "owner-route-ai-workflow-recall-1"
		ctx := runtimeDataWriteOwnerContext(t, requestID, false)
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "ai_workflow", "record_workflow_event", map[string]any{
			"event_type": "command_started", "status": "running", "parent_event_id": "parent-ai-owner-route",
			"run_id": "run-ai-owner-route", "workstream_id": "ws-owner-route", "repo": "repo-owner-route",
			"worktree_id": "wt-owner-route", "command_name": "/review", "skill_name": "audit", "summary": "command started",
		})
		if first.IdempotentReplay || first.AuditRef == "" || first.OwnerRoute != "ai_workflow/record_workflow_event" {
			t.Fatalf("AI workflow write receipt=%#v", first)
		}
		result := ownerRouteRecallByAuditRef(t, worker, ctx, "ai_workflow", "workflow_events", first.AuditRef, requestID)
		record := result.Records[0]
		if record["event_id"] != first.AuditRef || record["run_id"] != "run-ai-owner-route" || record["parent_event_id"] != "parent-ai-owner-route" || record["agent"] != "shiro" || record["status"] != "running" {
			t.Fatalf("AI workflow recalled record=%#v", record)
		}
	})

	t.Run("DCI search trace", func(t *testing.T) {
		corpus := t.TempDir()
		if err := writeDCIAdapterTestFile(filepath.Join(corpus, "owner.md"), "owner route DCI evidence\n"); err != nil {
			t.Fatalf("write DCI corpus: %v", err)
		}
		store := dcipersistence.NewJSONLStore(filepath.Join(t.TempDir(), "dci", "search_traces.jsonl"))
		explorer := dciapp.NewExplorer(dciapp.Config{
			Enabled: true, Allowlist: []string{corpus}, MaxEvidence: 2, MaxFilesRead: 2,
			Now: func() time.Time { return time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC) },
		}, store)
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteDCI(writeRegistry, store, explorer); err != nil {
			t.Fatalf("register DCI write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallDCISearchTrace(recallRegistry, store); err != nil {
			t.Fatalf("register DCI recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		requestID := "owner-route-dci-recall-1"
		ctx := runtimeDataWriteOwnerContext(t, requestID, false)
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "dci", "search", map[string]any{"query": "owner route"})
		if first.IdempotentReplay || first.AuditRef == "" || first.OwnerRoute != "dci/search" {
			t.Fatalf("DCI write receipt=%#v", first)
		}
		result := ownerRouteRecallByAuditRef(t, worker, ctx, "dci", "search_trace", first.AuditRef, requestID)
		record := result.Records[0]
		if record["trace_id"] != first.AuditRef || record["query"] != "owner route" || record["actor"] != "shiro" || record["mode"] != "dci" || record["status"] != "completed" {
			t.Fatalf("DCI recalled record=%#v", record)
		}
		if evidenceCount, ok := record["evidence_count"].(int); !ok || evidenceCount == 0 {
			t.Fatalf("DCI recalled evidence_count=%#v", record["evidence_count"])
		}
	})
}

func ownerRouteRecallByAuditRef(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation, auditRef, requestID string) runtimeDataRecallResult {
	t.Helper()
	if strings.TrimSpace(auditRef) == "" {
		t.Fatal("write receipt AuditRef is empty")
	}
	result := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, store, operation, auditRef)
	if result.Store != store || result.Operation != operation || result.Partial || len(result.Records) != 1 {
		t.Fatalf("recall store=%q operation=%q result=%#v", store, operation, result)
	}
	if result.Evidence.RequestID != requestID || result.Evidence.ActorID != "shiro" || result.Evidence.AgentRole != "worker" || result.Evidence.Purpose != "ops" || result.Evidence.DataScope != string(dataRecallAccessInternal) || result.Evidence.Owner != store || result.Evidence.OwnerRoute != store+"/"+operation || result.Evidence.ReturnedCount != 1 {
		t.Fatalf("recall evidence=%#v", result.Evidence)
	}
	return result
}

func assertOwnerRouteRecordDoesNotContain(t *testing.T, record map[string]any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal recalled record: %v", err)
	}
	for _, value := range forbidden {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("recalled record leaked forbidden value %q: %s", value, encoded)
		}
	}
}
