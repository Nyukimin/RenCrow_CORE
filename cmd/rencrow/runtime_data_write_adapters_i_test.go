package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteConversationL1CandidateOwnerE2E(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	requestID := "conversation-l1-owner-1"
	ctx := runtimeDataWriteOwnerContext(t, requestID, true)
	worker := runtimeConversationL1OwnerWorker(t, store)
	payload := map[string]any{
		"type": " preference ", "statement": "  Ren prefers concise explanations  ",
		"evidence_event_ids": []any{"evt-1"}, "confidence": 0.85, "sensitivity": " normal ", "persona_scope": " mio_only ",
	}
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "conversation_l1", "propose_user_memory", payload)
	if first.IdempotentReplay || first.SchemaVersion != "conversation-l1-user-memory-candidate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || !strings.HasPrefix(first.AuditRef, "user-memory-candidate/sha256:") || first.IdempotencyKey != requestID || first.PolicyRevision != runtimeDataWritePolicyRevision || first.OwnerRoute != "conversation_l1/propose_user_memory" {
		t.Fatalf("first conversation L1 receipt=%#v", first)
	}
	created, found, err := store.FindUserMemoryByID(ctx, first.AuditRef)
	if err != nil || !found || created.UserID != "user-1" || created.Statement != "Ren prefers concise explanations" || created.State != l1sqlite.MemoryStateCandidate || created.Sensitivity != "normal" || created.Scope != "mio_only" {
		t.Fatalf("created user memory=%#v found=%v err=%v", created, found, err)
	}
	if items, err := store.ListPromptInjectableUserMemories(ctx, "user-1", "mio", 10); err != nil || len(items) != 0 {
		t.Fatalf("candidate became prompt injectable: %#v err=%v", items, err)
	}

	exact := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "conversation_l1", "user_memory", first.AuditRef)
	assertConversationL1RecallResult(t, exact, requestID, "conversation_l1/user_memory", 1)
	if record := exact.Records[0]; record["memory_id"] != first.AuditRef || record["type"] != "preference" || record["statement"] != "Ren prefers concise explanations" || record["state"] != l1sqlite.MemoryStateCandidate || record["persona_scope"] != "mio_only" {
		t.Fatalf("exact user memory record=%#v", record)
	}
	assertConversationL1SafeRecord(t, exact.Records[0])
	list := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "conversation_l1", "user_memories", "Ren prefers")
	assertConversationL1RecallResult(t, list, requestID, "conversation_l1/user_memories", 1)
	if list.Records[0]["memory_id"] != first.AuditRef {
		t.Fatalf("listed user memory=%#v", list.Records[0])
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close L1 store: %v", err)
	}
	reopened, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1 store: %v", err)
	}
	defer reopened.Close()
	worker = runtimeConversationL1OwnerWorker(t, reopened)
	replay := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "conversation_l1", "propose_user_memory", payload)
	if !replay.IdempotentReplay || replay.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt=%#v first=%#v", replay, first)
	}
	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["statement"] = "different statement"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "conversation_l1", "operation": "propose_user_memory", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request conflict response=%#v err=%v", response, err)
	}
	otherActor := conversationL1UserContext(t, requestID, "user-1", "mio")
	if response, err := worker.ExecuteV2(otherActor, "data.write", map[string]any{"store": "conversation_l1", "operation": "propose_user_memory", "payload": payload}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request actor conflict response=%#v err=%v", response, err)
	}
	otherUser := conversationL1UserContext(t, requestID, "user-2", "shiro")
	if response, err := worker.ExecuteV2(otherUser, "data.write", map[string]any{"store": "conversation_l1", "operation": "propose_user_memory", "payload": payload}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request user conflict response=%#v err=%v", response, err)
	}
	if unchanged, found, err := reopened.FindUserMemoryByID(ctx, first.AuditRef); err != nil || !found || unchanged.Statement != "Ren prefers concise explanations" || unchanged.UserID != "user-1" {
		t.Fatalf("conflict mutated candidate=%#v found=%v err=%v", unchanged, found, err)
	}

	otherExact := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUser, "conversation_l1", "user_memory", first.AuditRef)
	assertConversationL1RecallResult(t, otherExact, requestID, "conversation_l1/user_memory", 0)
	otherList := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUser, "conversation_l1", "user_memories", "Ren prefers")
	assertConversationL1RecallResult(t, otherList, requestID, "conversation_l1/user_memories", 0)
	internal := runtimeDataWriteOwnerContext(t, "conversation-l1-internal", false)
	if response, err := worker.ExecuteV2(internal, "data.recall", map[string]any{"store": "conversation_l1", "operation": "user_memory", "query": first.AuditRef}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("internal scope must not read user memory response=%#v err=%v", response, err)
	}
}

func TestRuntimeDataWriteConversationL1RejectsModelOwnedFields(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	worker := runtimeConversationL1OwnerWorker(t, store)
	for _, field := range []string{"user_id", "memory_id", "namespace", "state", "source", "actor_id", "request_id", "path", "db", "sql", "unknown"} {
		payload := map[string]any{"type": "preference", "statement": "candidate", field: "model-owned"}
		ctx := runtimeDataWriteOwnerContext(t, "conversation-l1-forbidden-"+field, true)
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "conversation_l1", "operation": "propose_user_memory", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", field, response, err)
		}
	}
	if items, err := store.ListUserMemories(context.Background(), "user-1", "", true, 20); err != nil || len(items) != 0 {
		t.Fatalf("forbidden payload mutated memories=%#v err=%v", items, err)
	}
}

func runtimeConversationL1OwnerWorker(t *testing.T, store *l1sqlite.L1SQLiteStore) *toolsinfra.ToolRunner {
	t.Helper()
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteConversationL1(writeRegistry, store); err != nil {
		t.Fatalf("register conversation L1 write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallConversationL1(recallRegistry, store); err != nil {
		t.Fatalf("register conversation L1 recall: %v", err)
	}
	return toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
}

func assertConversationL1RecallResult(t *testing.T, result runtimeDataRecallResult, requestID, ownerRoute string, count int) {
	t.Helper()
	if result.Store != "conversation_l1" || result.Records == nil || len(result.Records) != count || result.Partial || result.Evidence.RequestID != requestID || result.Evidence.ActorID != "shiro" || result.Evidence.DataScope != string(dataRecallAccessUser) || result.Evidence.OwnerRoute != ownerRoute || result.Evidence.ReturnedCount != count {
		t.Fatalf("conversation L1 recall result=%#v", result)
	}
}

func assertConversationL1SafeRecord(t *testing.T, record map[string]any) {
	t.Helper()
	for _, forbidden := range []string{"namespace", "user_id", "source", "actor_id", "request_id", "meta_json", "database", "sql", "path"} {
		if _, ok := record[forbidden]; ok {
			t.Fatalf("conversation L1 record exposed forbidden field %q: %#v", forbidden, record)
		}
	}
}

func conversationL1UserContext(t *testing.T, requestID, userID, actorID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AuthenticatedUserID: userID, AllowedDataScopes: []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("conversation L1 user scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
