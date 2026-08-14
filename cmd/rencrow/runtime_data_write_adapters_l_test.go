package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteKnowledgeMemoryCandidateOwnerE2EThroughWorkerAndExactRecall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge_memory.db")
	store, err := knowledgememorypersistence.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()

	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteKnowledgeMemory(writeRegistry, store); err != nil {
		t.Fatalf("register knowledge memory write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallKnowledgeMemory(recallRegistry, store, store); err != nil {
		t.Fatalf("register knowledge memory recall: %v", err)
	}
	assertRuntimeDataWriteEContract(t, writeRegistry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "knowledge_memory",
		Operation:             "propose_creative_candidate",
		Access:                dataRecallAccessUser,
		RequiredPayloadFields: []string{"title"},
		OptionalPayloadFields: []string{"content_hints", "creator_names", "related_works", "work_type"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	ctx := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-write-1", "user-1", "shiro")
	payload := map[string]any{
		"title":         "  Private Candidate Work  ",
		"creator_names": []any{"  Creator One  "},
		"work_type":     "  novel ",
		"related_works": []any{"Related Work"},
		"content_hints": []any{"private payload must not enter indexed search"},
	}

	first := runtimeKnowledgeMemoryExecuteWrite(t, worker, ctx, payload)
	if first.IdempotentReplay || first.SchemaVersion != "knowledge-memory-creative-candidate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "knowledge-memory-write-1" {
		t.Fatalf("first receipt = %#v", first)
	}
	if !strings.HasPrefix(first.AuditRef, runtimeKnowledgeMemoryCandidateIDPrefix) {
		t.Fatalf("candidate id = %q", first.AuditRef)
	}
	candidate, found, err := store.FindCreativeCandidateByID(context.Background(), "user-1", first.AuditRef)
	if err != nil || !found {
		t.Fatalf("saved candidate = %#v found=%v err=%v", candidate, found, err)
	}
	if candidate.Title != "Private Candidate Work" || candidate.UserID != "user-1" || candidate.CreatorNames[0] != "Creator One" || candidate.WorkType != "novel" || candidate.Status != "candidate" || candidate.Visibility != "private" || candidate.CreatedAt.IsZero() {
		t.Fatalf("saved candidate fields = %#v", candidate)
	}
	if _, found, err := store.FindCreativeCandidateByID(context.Background(), "other-user", first.AuditRef); err != nil || found {
		t.Fatalf("cross-user candidate lookup leaked: found=%v err=%v", found, err)
	}

	exact := runtimeKnowledgeMemoryExecuteRecall(t, worker, ctx, "creative_candidate", first.AuditRef, 10)
	if len(exact.Records) != 1 || exact.Records[0]["item_id"] != first.AuditRef || exact.Records[0]["user_id"] != "user-1" || exact.Records[0]["status"] != "candidate" || exact.Records[0]["visibility"] != "private" {
		t.Fatalf("candidate exact recall = %#v", exact)
	}
	if _, leaked := exact.Records[0]["payload"]; leaked {
		t.Fatal("candidate exact recall exposed raw payload")
	}
	receipt := runtimeKnowledgeMemoryExecuteRecall(t, worker, ctx, "requests", "knowledge-memory-write-1", 10)
	if len(receipt.Records) != 1 || receipt.Records[0]["request_id"] != "knowledge-memory-write-1" || receipt.Records[0]["user_id"] != "user-1" || receipt.Records[0]["item_id"] != first.AuditRef || receipt.Records[0]["payload_hash"] == "" {
		t.Fatalf("request receipt recall = %#v", receipt)
	}

	replayed := runtimeKnowledgeMemoryExecuteWrite(t, worker, ctx, payload)
	if !replayed.IdempotentReplay || replayed.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt = %#v first=%#v", replayed, first)
	}
	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["title"] = "Different Candidate Work"
	conflict, err := worker.ExecuteV2(ctx, "data.write", map[string]any{
		"store": "knowledge_memory", "operation": "propose_creative_candidate", "payload": changed,
	})
	if err != nil || conflict == nil || !conflict.IsError() {
		t.Fatalf("mismatched request response=%#v err=%v", conflict, err)
	}

	secondCtx := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-write-2", "user-1", "shiro")
	second := runtimeKnowledgeMemoryExecuteWrite(t, worker, secondCtx, payload)
	if second.IdempotentReplay || second.AuditRef == first.AuditRef {
		t.Fatalf("same-content new request was collapsed: first=%#v second=%#v", first, second)
	}
	items, err := store.ListCreativeKnowledgeItems(context.Background(), 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("candidate rows = %#v err=%v", items, err)
	}

	otherUserCtx := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-other", "other-user", "shiro")
	otherCandidate := runtimeKnowledgeMemoryExecuteRecall(t, worker, otherUserCtx, "creative_candidate", first.AuditRef, 10)
	if len(otherCandidate.Records) != 0 {
		t.Fatalf("cross-user candidate recall leaked = %#v", otherCandidate)
	}
	otherReceipt := runtimeKnowledgeMemoryExecuteRecall(t, worker, otherUserCtx, "requests", "knowledge-memory-write-1", 10)
	if len(otherReceipt.Records) != 0 {
		t.Fatalf("cross-user request recall leaked = %#v", otherReceipt)
	}
	search := runtimeKnowledgeMemoryExecuteRecall(t, worker, ctx, "search_user_creative", "Private Candidate Work", 10)
	if len(search.Records) != 0 {
		t.Fatalf("candidate leaked into indexed search = %#v", search)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := knowledgememorypersistence.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	reopenedWrite := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteKnowledgeMemory(reopenedWrite, reopened); err != nil {
		t.Fatalf("register reopened write: %v", err)
	}
	reopenedRecall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallKnowledgeMemory(reopenedRecall, reopened, reopened); err != nil {
		t.Fatalf("register reopened recall: %v", err)
	}
	reopenedWorker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: reopenedWrite, OperationalDataRecall: reopenedRecall, DisableToolHarness: true,
	})
	reopenedReplay := runtimeKnowledgeMemoryExecuteWrite(t, reopenedWorker, ctx, payload)
	if !reopenedReplay.IdempotentReplay || reopenedReplay.AuditRef != first.AuditRef {
		t.Fatalf("reopen replay receipt = %#v first=%#v", reopenedReplay, first)
	}
}

func TestRuntimeDataWriteKnowledgeMemoryCandidateRejectsModelOwnedFieldsAndUnsafeBounds(t *testing.T) {
	store, err := knowledgememorypersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge_memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteKnowledgeMemory(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	base := map[string]any{"title": "Candidate"}
	for _, key := range []string{"item_id", "user_id", "status", "visibility", "created_at", "request_id", "actor_id", "source", "path", "db", "sql"} {
		payload := runtimeDataWriteOwnerClonePayload(base)
		payload[key] = "model-owned"
		response, err := worker.ExecuteV2(runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-forbidden-"+key, "user-1", "shiro"), "data.write", map[string]any{
			"store": "knowledge_memory", "operation": "propose_creative_candidate", "payload": payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", key, response, err)
		}
	}
	tooLong := runtimeDataWriteOwnerClonePayload(base)
	tooLong["title"] = strings.Repeat("a", domainkm.MaxCreativeCandidateTitleRunes+1)
	response, err := worker.ExecuteV2(runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-too-long", "user-1", "shiro"), "data.write", map[string]any{
		"store": "knowledge_memory", "operation": "propose_creative_candidate", "payload": tooLong,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("overlong title response=%#v err=%v", response, err)
	}
	publicScope := runtimeKnowledgeMemoryPublicContext(t, "knowledge-memory-public-write")
	response, err = worker.ExecuteV2(publicScope, "data.write", map[string]any{
		"store": "knowledge_memory", "operation": "propose_creative_candidate", "payload": base,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("public write response=%#v err=%v", response, err)
	}
}

func runtimeKnowledgeMemoryExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, payload map[string]any) runtimeDataWriteReceipt {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{
		"store": "knowledge_memory", "operation": "propose_creative_candidate", "payload": payload,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("knowledge memory data.write response=%#v err=%v", response, err)
	}
	receipt, ok := response.Result.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("knowledge memory data.write result type=%T value=%#v", response.Result, response.Result)
	}
	return receipt
}

func runtimeKnowledgeMemoryUserContext(t *testing.T, requestID, userID, actorID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AuthenticatedUserID: userID, AllowedDataScopes: []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("user scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeKnowledgeMemoryPublicContext(t *testing.T, requestID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "mio",
		AllowedDataScopes:    []string{domaintool.DataScopePublic},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("public scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
