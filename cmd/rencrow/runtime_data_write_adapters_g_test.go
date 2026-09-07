package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domaindurable "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	durablepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeDataWriteDurableStoreWorkflowUsesDurableRequestReceipts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.db")
	store, err := durablepersistence.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	workflow := appstore.NewService([]domaindurable.Manifest{runtimeDurableWriteTestManifest()}, store, nil)
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDurableStoreWorkflow(writeRegistry, workflow); err != nil {
		t.Fatal(err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(recallRegistry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true})
	message := "XのBookmarkを保存するDBの設計を確認して"
	action1 := modulecore.ActionID("act_00000000-0000-5000-8000-000000000001")
	action2 := modulecore.ActionID("act_00000000-0000-5000-8000-000000000002")
	firstContext := runtimeDurableWriteTestContext(t, string(action1), "user-1")
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, firstContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if first.IdempotentReplay || first.AuditRef == "" || first.IdempotencyKey != string(action1) {
		t.Fatalf("first receipt=%#v", first)
	}
	receipt, err := store.FindByActionID(context.Background(), action1)
	if err != nil || receipt == nil || receipt.RequirementID != first.AuditRef || receipt.UserScope != "user-1" {
		t.Fatalf("first receipt persistence=%+v err=%v", receipt, err)
	}
	second := runtimeDataWriteOwnerExecuteWrite(t, worker, firstContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt=%#v first=%#v", second, first)
	}
	changedResponse, err := worker.ExecuteV2(firstContext, "data.write", map[string]any{
		"store": "durable_store_workflow", "operation": "handle_storage_intent", "payload": map[string]any{"message": "XのBookmarkを保存するDBを別方式で実装して"},
	})
	if err != nil || changedResponse == nil || !changedResponse.IsError() {
		t.Fatalf("same-request conflict response=%#v err=%v", changedResponse, err)
	}

	secondRequestContext := runtimeDurableWriteTestContext(t, string(action2), "user-1")
	semantic := runtimeDataWriteOwnerExecuteWrite(t, worker, secondRequestContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if semantic.IdempotentReplay || semantic.AuditRef != first.AuditRef {
		t.Fatalf("semantic dedupe receipt=%#v first=%#v", semantic, first)
	}
	semanticReceipt, err := store.FindByActionID(context.Background(), action2)
	if err != nil || semanticReceipt == nil || semanticReceipt.RequirementID != first.AuditRef {
		t.Fatalf("semantic receipt=%+v err=%v", semanticReceipt, err)
	}

	recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, firstContext, "durable_store_workflow", "exact_request", string(action2))
	if len(recalled.Records) != 1 || recalled.Records[0]["requirement_id"] != first.AuditRef || recalled.Records[0]["deduplicated"] != true {
		t.Fatalf("semantic recall=%#v", recalled)
	}
	requirementRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, firstContext, "durable_store_workflow", "requirement", first.AuditRef)
	if len(requirementRecall.Records) != 1 || requirementRecall.Records[0]["requirement_id"] != first.AuditRef || requirementRecall.Records[0]["action_id"] != string(action1) || requirementRecall.Records[0]["status"] == "" || requirementRecall.Records[0]["lifecycle"] == "" {
		t.Fatalf("requirement recall=%#v", requirementRecall)
	}
	for _, forbidden := range []string{"message", "user_scope", "path", "database", "sql"} {
		if strings.Contains(strings.ToLower(string(mustJSON(requirementRecall.Records[0]))), forbidden) {
			t.Fatalf("requirement recall leaked %q: %#v", forbidden, requirementRecall.Records[0])
		}
	}
	otherUser := runtimeDurableWriteTestContext(t, "act_00000000-0000-5000-8000-000000000099", "user-2")
	otherRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUser, "durable_store_workflow", "exact_request", string(action2))
	if len(otherRecall.Records) != 0 {
		t.Fatalf("cross-user recall leaked=%#v", otherRecall)
	}
	otherRequirementRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUser, "durable_store_workflow", "requirement", first.AuditRef)
	if len(otherRequirementRecall.Records) != 0 {
		t.Fatalf("cross-user requirement recall leaked=%#v", otherRequirementRecall)
	}
	missingRequirementRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, firstContext, "durable_store_workflow", "requirement", "missing-requirement")
	if len(missingRequirementRecall.Records) != 0 {
		t.Fatalf("missing requirement recall=%#v", missingRequirementRecall)
	}

	for _, forbidden := range []string{"action_id", "trace_id", "requested_by", "user_scope", "requirement_id"} {
		response, err := worker.ExecuteV2(firstContext, "data.write", map[string]any{
			"store": "durable_store_workflow", "operation": "handle_storage_intent",
			"payload": map[string]any{"message": message, forbidden: "model-owned"},
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", forbidden, response, err)
		}
	}
	unknown, err := worker.ExecuteV2(firstContext, "data.write", map[string]any{
		"store": "durable_store_workflow", "operation": "handle_storage_intent", "payload": map[string]any{"message": "今日は天気について教えて"},
	})
	if err != nil || unknown == nil || !unknown.IsError() {
		t.Fatalf("unhandled intent response=%#v err=%v", unknown, err)
	}

	routes := writeRegistry.Snapshot()
	if len(routes) != 1 || routes[0].Store != "durable_store_workflow" || routes[0].Operation != "handle_storage_intent" || routes[0].Access != dataRecallAccessUser || len(routes[0].RequiredPayloadFields) != 1 || routes[0].RequiredPayloadFields[0] != "message" || len(routes[0].OptionalPayloadFields) != 0 {
		t.Fatalf("write route snapshot=%#v", routes)
	}
	_ = store.Close()

	store, err = durablepersistence.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restartedWorkflow := appstore.NewService([]domaindurable.Manifest{runtimeDurableWriteTestManifest()}, store, nil)
	restartedRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDurableStoreWorkflow(restartedRegistry, restartedWorkflow); err != nil {
		t.Fatal(err)
	}
	restartedWorker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: restartedRegistry, DisableToolHarness: true})
	replayed := runtimeDataWriteOwnerExecuteWrite(t, restartedWorker, firstContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if !replayed.IdempotentReplay || replayed.AuditRef != first.AuditRef {
		t.Fatalf("restart replay=%#v first=%#v", replayed, first)
	}
}

func runtimeDurableWriteTestManifest() domaindurable.Manifest {
	return domaindurable.Manifest{ContractVersion: "rencrow-durable-stores/v1", ModuleID: "RenCrow_CORE", Stores: []domaindurable.StoreManifest{{
		StoreID: "core.conversation_l1", OwnerModule: "RenCrow_CORE", StoreKind: "sqlite", DurabilityClass: "durable", DataClasses: []string{"x_bookmark"}, CanonicalConfigKeys: []string{"storage.databases.conversation_l1"}, ProductionRootTemplate: "/srv/rencrow/db/core",
		AuthoritativeWriter: "rencrow-core", SchemaRevision: "conversation-l1/v1", MigrationOwner: "RenCrow_CORE", RetentionPolicy: "class-specific", BackupProfile: "core-snapshot/v1", RestoreCheck: "sqlite-integrity/v1", RPO: "PT24H", RTO: "PT4H", Sensitivity: "private", FallbackPolicy: "fail_closed", ChangeClass: domaindurable.ChangeS1, ProposalRevision: 1, LifecycleStatus: domaindurable.LifecycleValidated,
	}}}
}

func runtimeDurableWriteTestContext(t *testing.T, requestID, userID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "shiro", AuthenticatedUserID: userID,
		AllowedDataScopes: []string{domaintool.DataScopeUser}, AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
