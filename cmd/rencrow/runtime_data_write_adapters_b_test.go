package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	advisorpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
	skillpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteAdvisorAndSkillGovernanceOwnerAdaptersE2E(t *testing.T) {
	t.Run("advisor adoption", func(t *testing.T) {
		root := t.TempDir()
		store := advisorpersistence.NewJSONLStore(filepath.Join(root, "advisor"))
		now := time.Date(2026, 8, 14, 2, 3, 4, 0, time.UTC)
		if err := store.SaveAdviceRun(context.Background(), domainadvisor.AdviceRunRecord{
			RunID: "run-owner-1", RequestedByAgent: "shiro", AdvisorID: domainadvisor.AdvisorCodex,
			Status: domainadvisor.AdviceStatus(domainadvisor.StatusCompleted), StartedAt: now, FinishedAt: now,
		}); err != nil {
			t.Fatalf("seed advice run: %v", err)
		}
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteAdvisor(writeRegistry, store); err != nil {
			t.Fatalf("register advisor write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallAdvisor(recallRegistry, store); err != nil {
			t.Fatalf("register advisor recall: %v", err)
		}
		if err := registerRuntimeDataRecallAdvisorAdoptions(recallRegistry, store); err != nil {
			t.Fatalf("register advisor adoption recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true})
		ctx := runtimeDataWriteBContext(t, "owner-adoption-1")
		payload := map[string]any{"run_id": " run-owner-1 ", "task_id": " task-1 ", "adopted": true, "outcome": " success ", "revision_count": 2, "reason": " useful "}
		first := runtimeDataWriteBExecuteWrite(t, worker, ctx, "advisor", "record_adoption", payload)
		if first.IdempotentReplay || first.SchemaVersion != "advisor-adoption/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "owner-adoption-1" {
			t.Fatalf("first advisor receipt = %#v", first)
		}
		second := runtimeDataWriteBExecuteWrite(t, worker, ctx, "advisor", "record_adoption", payload)
		if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
			t.Fatalf("advisor replay receipt=%#v first=%#v", second, first)
		}
		adoptions, err := store.ListAdvisorAdoptions(ctx, 10)
		if err != nil || len(adoptions) != 1 || adoptions[0].Outcome != "success" || adoptions[0].AdoptedByAgent != "shiro" {
			t.Fatalf("advisor adoptions=%#v err=%v", adoptions, err)
		}
		changed := runtimeDataWriteBClonePayload(payload)
		changed["outcome"] = "failed"
		if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "advisor", "operation": "record_adoption", "payload": changed}); err != nil || response == nil || !response.IsError() {
			t.Fatalf("advisor conflict response=%#v err=%v", response, err)
		}
		adoptions, err = store.ListAdvisorAdoptions(ctx, 10)
		if err != nil || len(adoptions) != 1 || adoptions[0].Outcome != "success" {
			t.Fatalf("advisor conflict mutated=%#v err=%v", adoptions, err)
		}
		recalled := runtimeDataWriteBExecuteRecall(t, worker, ctx, "advisor", "adoptions", first.AuditRef)
		if len(recalled.Records) != 1 || recalled.Evidence.RequestID != "owner-adoption-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.OwnerRoute != "advisor/adoptions" || recalled.Evidence.DataScope != string(dataRecallAccessInternal) {
			t.Fatalf("advisor recall=%#v", recalled)
		}
		if got := recalled.Records[0]; got["adoption_id"] != first.AuditRef || got["run_id"] != "run-owner-1" || got["advisor_id"] != string(domainadvisor.AdvisorCodex) || got["outcome"] != "success" {
			t.Fatalf("advisor recalled record=%#v", got)
		}
	})

	t.Run("skill contribution gate", func(t *testing.T) {
		root := t.TempDir()
		store := skillpersistence.NewJSONLStore(filepath.Join(root, "skill"))
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSkillGovernance(writeRegistry, store); err != nil {
			t.Fatalf("register skill write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallSkillGovernance(recallRegistry, store); err != nil {
			t.Fatalf("register skill manifest recall: %v", err)
		}
		if err := registerRuntimeDataRecallSkillContributionGates(recallRegistry, store); err != nil {
			t.Fatalf("register contribution gate recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true})
		ctx := runtimeDataWriteBContext(t, "owner-gate-1")
		payload := map[string]any{
			"repo": " example/repo ", "target_branch": " main ", "problem_statement": " real problem ",
			"existing_prs_checked": true, "real_problem_verified": true, "core_change_verified": true,
			"diff_reviewed": true, "test_result": " go test ./... ",
		}
		first := runtimeDataWriteBExecuteWrite(t, worker, ctx, "skill_governance", "record_contribution_gate", payload)
		if first.IdempotentReplay || first.SchemaVersion != "skill-contribution-gate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "owner-gate-1" {
			t.Fatalf("first gate receipt = %#v", first)
		}
		second := runtimeDataWriteBExecuteWrite(t, worker, ctx, "skill_governance", "record_contribution_gate", payload)
		if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
			t.Fatalf("gate replay receipt=%#v first=%#v", second, first)
		}
		gates, err := store.ListContributionGateLogs(ctx, 10)
		if err != nil || len(gates) != 1 || gates[0].GateStatus != domainskill.GateStatusPassed {
			t.Fatalf("gates=%#v err=%v", gates, err)
		}
		changed := runtimeDataWriteBClonePayload(payload)
		changed["diff_reviewed"] = false
		if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "skill_governance", "operation": "record_contribution_gate", "payload": changed}); err != nil || response == nil || !response.IsError() {
			t.Fatalf("gate conflict response=%#v err=%v", response, err)
		}
		gates, err = store.ListContributionGateLogs(ctx, 10)
		if err != nil || len(gates) != 1 || gates[0].DiffReviewed != true || gates[0].GateStatus != domainskill.GateStatusPassed {
			t.Fatalf("gate conflict mutated=%#v err=%v", gates, err)
		}
		blockedCtx := runtimeDataWriteBContext(t, "owner-gate-blocked")
		blockedPayload := runtimeDataWriteBClonePayload(payload)
		blockedPayload["diff_reviewed"] = false
		blocked := runtimeDataWriteBExecuteWrite(t, worker, blockedCtx, "skill_governance", "record_contribution_gate", blockedPayload)
		if blocked.IdempotentReplay || blocked.AuditRef == first.AuditRef {
			t.Fatalf("fresh blocked gate receipt=%#v", blocked)
		}
		gates, err = store.ListContributionGateLogs(blockedCtx, 10)
		foundBlocked := false
		for _, gate := range gates {
			if gate.EventID == blocked.AuditRef {
				foundBlocked = gate.GateStatus == domainskill.GateStatusBlocked && !gate.DiffReviewed
				break
			}
		}
		if err != nil || len(gates) != 2 || !foundBlocked {
			t.Fatalf("fresh blocked gate not persisted gates=%#v err=%v", gates, err)
		}
		recalled := runtimeDataWriteBExecuteRecall(t, worker, ctx, "skill_governance", "contribution_gates", first.AuditRef)
		if len(recalled.Records) != 1 || recalled.Evidence.RequestID != "owner-gate-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.OwnerRoute != "skill_governance/contribution_gates" || recalled.Evidence.DataScope != string(dataRecallAccessInternal) {
			t.Fatalf("gate recall=%#v", recalled)
		}
		if got := recalled.Records[0]; got["event_id"] != first.AuditRef || got["repo"] != "example/repo" || got["gate_status"] != domainskill.GateStatusPassed {
			t.Fatalf("gate recalled record=%#v", got)
		}
	})
}

func TestRuntimeDataWriteAdvisorAndSkillGovernanceRejectUnknownAndModelOwnedFields(t *testing.T) {
	t.Run("advisor", func(t *testing.T) {
		store := advisorpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "advisor"))
		if err := store.SaveAdviceRun(context.Background(), domainadvisor.AdviceRunRecord{RunID: "run-owner-1", RequestedByAgent: "shiro", AdvisorID: domainadvisor.AdvisorCodex, Status: domainadvisor.AdviceStatus(domainadvisor.StatusCompleted), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		registry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteAdvisor(registry, store); err != nil {
			t.Fatal(err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
		ctx := runtimeDataWriteBContext(t, "owner-adoption-invalid")
		for _, extra := range []string{"adoption_id", "advisor_id", "adopted_by_agent", "created_at", "request_id", "unknown"} {
			payload := map[string]any{"run_id": "run-owner-1", "adopted": true, "outcome": "success", "revision_count": 0, extra: "model-owned"}
			response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "advisor", "operation": "record_adoption", "payload": payload})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
			}
		}
		if items, err := store.ListAdvisorAdoptions(ctx, 10); err != nil || len(items) != 0 {
			t.Fatalf("invalid advisor payload mutated=%#v err=%v", items, err)
		}
	})

	t.Run("skill", func(t *testing.T) {
		store := skillpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "skill"))
		registry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteSkillGovernance(registry, store); err != nil {
			t.Fatal(err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
		ctx := runtimeDataWriteBContext(t, "owner-gate-invalid")
		for _, extra := range []string{"event_id", "gate_status", "created_at", "actor_id", "request_id", "unknown"} {
			payload := map[string]any{"repo": "example/repo", "existing_prs_checked": true, "real_problem_verified": true, "core_change_verified": true, "diff_reviewed": true, extra: "model-owned"}
			response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "skill_governance", "operation": "record_contribution_gate", "payload": payload})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
			}
		}
		if items, err := store.ListContributionGateLogs(ctx, 10); err != nil || len(items) != 0 {
			t.Fatalf("invalid gate payload mutated=%#v err=%v", items, err)
		}
	})
}

func runtimeDataWriteBExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation string, payload map[string]any) runtimeDataWriteReceipt {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": store, "operation": operation, "payload": payload})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("data.write response=%#v err=%v", response, err)
	}
	receipt, ok := response.Result.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("data.write result type=%T value=%#v", response.Result, response.Result)
	}
	return receipt
}

func runtimeDataWriteBExecuteRecall(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation, query string) runtimeDataRecallResult {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{"store": store, "operation": operation, "query": query, "limit": 1})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("data.recall response=%#v err=%v", response, err)
	}
	result, ok := response.Result.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("data.recall result type=%T value=%#v", response.Result, response.Result)
	}
	return result
}

func runtimeDataWriteBContext(t *testing.T, requestID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "shiro",
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
		AllowedDataScopes: []string{domaintool.DataScopeInternal},
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("internal scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeDataWriteBClonePayload(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
