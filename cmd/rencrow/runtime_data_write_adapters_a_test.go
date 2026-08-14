package main

import (
	"context"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	revenuepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteOwnerAdaptersE2EThroughWorkerAndRecall(t *testing.T) {
	t.Run("workstream goal", func(t *testing.T) {
		store := workstreampersistence.NewJSONLStore(t.TempDir())
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteWorkstream(writeRegistry, store); err != nil {
			t.Fatalf("register write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallWorkstream(recallRegistry, store); err != nil {
			t.Fatalf("register recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		ctx := runtimeDataWriteOwnerContext(t, "owner-workstream-1", true)
		payload := map[string]any{
			"workstream_id":    "ws_owner",
			"title":            "  Build a reusable report  ",
			"description":      "  Draft only  ",
			"success_criteria": []any{"  report exists  ", "reviewed"},
			"verification":     []any{"  inspect record  "},
		}
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "workstream", "create_goal", payload)
		if first.IdempotentReplay || first.SchemaVersion != "workstream-goal/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "owner-workstream-1" {
			t.Fatalf("first receipt = %#v", first)
		}
		second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "workstream", "create_goal", payload)
		if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
			t.Fatalf("replay receipt = %#v, first=%#v", second, first)
		}
		changed := runtimeDataWriteOwnerClonePayload(payload)
		changed["title"] = "different"
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "workstream", "operation": "create_goal", "payload": changed})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("mismatched replay response=%#v err=%v", response, err)
		}
		goals, err := store.ListGoals(ctx, 10)
		if err != nil || len(goals) != 1 || goals[0].Title != "Build a reusable report" || goals[0].Status != "draft" {
			t.Fatalf("goals=%#v err=%v", goals, err)
		}
		recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "workstream", "goals", first.AuditRef)
		if recalled.Evidence.RequestID != "owner-workstream-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.DataScope != string(dataRecallAccessUser) || recalled.Evidence.OwnerRoute != "workstream/goals" || len(recalled.Records) != 1 {
			t.Fatalf("recall result=%#v", recalled)
		}
	})

	t.Run("revenue opportunity", func(t *testing.T) {
		store := revenuepersistence.NewJSONLStore(t.TempDir())
		writeRegistry := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteRevenue(writeRegistry, store); err != nil {
			t.Fatalf("register write: %v", err)
		}
		recallRegistry := newRuntimeDataRecallRegistry()
		if err := registerRuntimeDataRecallRevenue(recallRegistry, store); err != nil {
			t.Fatalf("register recall: %v", err)
		}
		worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
			OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
		})
		ctx := runtimeDataWriteOwnerContext(t, "owner-revenue-1", false)
		payload := map[string]any{
			"source_kind":      "  note_archive  ",
			"title":            "  Reusable revenue draft  ",
			"summary":          "  Draft summary  ",
			"target_customer":  "  local teams  ",
			"expected_revenue": 5000,
			"expected_cost":    1200,
			"reuse_value":      0.8,
			"automation_rate":  0.7,
			"strategic_value":  0.6,
			"risk_score":       0.2,
		}
		first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "revenue", "draft_opportunity", payload)
		if first.IdempotentReplay || first.SchemaVersion != "revenue-opportunity/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "owner-revenue-1" {
			t.Fatalf("first receipt = %#v", first)
		}
		second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "revenue", "draft_opportunity", payload)
		if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
			t.Fatalf("replay receipt = %#v, first=%#v", second, first)
		}
		changed := runtimeDataWriteOwnerClonePayload(payload)
		changed["expected_cost"] = 1300
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "revenue", "operation": "draft_opportunity", "payload": changed})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("mismatched replay response=%#v err=%v", response, err)
		}
		opportunities, err := store.ListOpportunities(ctx, 10)
		if err != nil || len(opportunities) != 1 || opportunities[0].Title != "Reusable revenue draft" || opportunities[0].ExpectedProfit != 3800 {
			t.Fatalf("opportunities=%#v err=%v", opportunities, err)
		}
		recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "revenue", "opportunities", first.AuditRef)
		if recalled.Evidence.RequestID != "owner-revenue-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.DataScope != string(dataRecallAccessInternal) || recalled.Evidence.OwnerRoute != "revenue/opportunities" || len(recalled.Records) != 1 {
			t.Fatalf("recall result=%#v", recalled)
		}
	})
}

func TestRuntimeDataWriteOwnerAdaptersRejectUnknownAndModelOwnedFields(t *testing.T) {
	store := workstreampersistence.NewJSONLStore(t.TempDir())
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteWorkstream(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteOwnerContext(t, "owner-invalid-1", true)
	for _, extra := range []string{"goal_id", "trace_id", "created_at", "actor_id", "request_id", "unknown"} {
		payload := map[string]any{
			"workstream_id":    "ws_owner",
			"title":            "title",
			"success_criteria": []any{"criteria"},
			"verification":     []any{"verification"},
			extra:              "model-owned",
		}
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "workstream", "operation": "create_goal", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
		}
	}
	if goals, err := store.ListGoals(ctx, 10); err != nil || len(goals) != 0 {
		t.Fatalf("invalid payload mutated goals=%#v err=%v", goals, err)
	}
}

func runtimeDataWriteOwnerExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation string, payload map[string]any) runtimeDataWriteReceipt {
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

func runtimeDataWriteOwnerExecuteRecall(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation, query string) runtimeDataRecallResult {
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

func runtimeDataWriteOwnerContext(t *testing.T, requestID string, userScope bool) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "shiro",
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if userScope {
		scope.AuthenticatedUserID = "user-1"
		scope.AllowedDataScopes = []string{domaintool.DataScopeUser}
	} else {
		scope.AllowedDataScopes = []string{domaintool.DataScopeInternal}
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("owner scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeDataWriteOwnerClonePayload(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
