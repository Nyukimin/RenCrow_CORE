package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteConversationArchiveOwnerUsesTrustedScopeAndExactEvent(t *testing.T) {
	event := runtimeConversationArchiveTestEvent(l1sqlite.MemoryStateConfirmed)
	l1 := &runtimeConversationArchiveTestL1Store{event: event, found: true}
	archive := &runtimeConversationArchiveTestArchiveStore{}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteConversationArchive(registry, l1, archive); err != nil {
		t.Fatalf("register conversation archive: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 1 || routes[0].Store != "conversation_archive" || routes[0].Operation != "archive_user_memory" || routes[0].Access != dataRecallAccessUser || len(routes[0].RequiredPayloadFields) != 1 || routes[0].RequiredPayloadFields[0] != "memory_id" || len(routes[0].OptionalPayloadFields) != 0 {
		t.Fatalf("archive route=%#v", routes)
	}

	ctx := runtimeDataWriteOwnerContext(t, "archive-owner-1", true)
	worker := runtimeConversationArchiveTestWorker(t, registry)
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "conversation_archive", "archive_user_memory", map[string]any{"memory_id": " " + event.ID + " "})
	if first.IdempotentReplay || first.SchemaVersion != "conversation-archive-user-memory/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef != event.ID || first.IdempotencyKey != "archive-owner-1" || first.PolicyRevision != runtimeConversationArchivePolicyRevision {
		t.Fatalf("first archive receipt=%#v", first)
	}
	if archive.lastEvent.Message != event.Message || archive.lastEvent.Meta["statement"] != event.Meta["statement"] {
		t.Fatalf("archive did not receive exact L1 event=%+v", archive.lastEvent)
	}
	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "conversation_archive", "archive_user_memory", map[string]any{"memory_id": event.ID})
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("replay archive receipt=%#v first=%#v", second, first)
	}

	for _, forbidden := range []string{"message", "summary", "content", "namespace", "user_id", "actor_id", "request_id", "payload_hash", "state"} {
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{
			"store": "conversation_archive", "operation": "archive_user_memory",
			"payload": map[string]any{"memory_id": event.ID, forbidden: "model-owned"},
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", forbidden, response, err)
		}
	}
}

func TestRuntimeDataWriteConversationArchiveOwnerRejectsCandidateAndCrossUserScope(t *testing.T) {
	event := runtimeConversationArchiveTestEvent(l1sqlite.MemoryStateCandidate)
	l1 := &runtimeConversationArchiveTestL1Store{event: event, found: true}
	archive := &runtimeConversationArchiveTestArchiveStore{}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteConversationArchive(registry, l1, archive); err != nil {
		t.Fatal(err)
	}
	worker := runtimeConversationArchiveTestWorker(t, registry)
	response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "archive-candidate-1", true), "data.write", map[string]any{
		"store": "conversation_archive", "operation": "archive_user_memory", "payload": map[string]any{"memory_id": event.ID},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("candidate archive response=%#v err=%v", response, err)
	}
	if archive.calls != 0 {
		t.Fatalf("candidate must not reach archive writer: calls=%d", archive.calls)
	}

	event.MemoryState = l1sqlite.MemoryStateConfirmed
	l1.event = event
	otherUser := runtimeConversationArchiveUserContext(t, "archive-cross-user-1", "user-2")
	response, err = worker.ExecuteV2(otherUser, "data.write", map[string]any{
		"store": "conversation_archive", "operation": "archive_user_memory", "payload": map[string]any{"memory_id": event.ID},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("cross-user archive response=%#v err=%v", response, err)
	}
}

func runtimeConversationArchiveTestWorker(t *testing.T, registry *runtimeDataWriteRegistry) *toolsinfra.ToolRunner {
	t.Helper()
	return toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
}

type runtimeConversationArchiveTestL1Store struct {
	event l1sqlite.L1MemoryEvent
	found bool
}

func (s *runtimeConversationArchiveTestL1Store) FindUserMemoryEventByID(_ context.Context, userID, memoryID string) (l1sqlite.L1MemoryEvent, bool, error) {
	if !s.found || s.event.ID != memoryID || s.event.Namespace != "user:"+strings.TrimSpace(userID) {
		return l1sqlite.L1MemoryEvent{}, false, nil
	}
	return s.event, true, nil
}

type runtimeConversationArchiveTestArchiveStore struct {
	receipts  map[string]archivesqlite.ArchiveRequestReceipt
	lastEvent l1sqlite.L1MemoryEvent
	calls     int
}

func (s *runtimeConversationArchiveTestArchiveStore) ArchiveUserMemoryWithReceipt(_ context.Context, event l1sqlite.L1MemoryEvent, receipt archivesqlite.ArchiveRequestReceipt) (bool, error) {
	s.calls++
	if s.receipts == nil {
		s.receipts = make(map[string]archivesqlite.ArchiveRequestReceipt)
	}
	if previous, ok := s.receipts[receipt.RequestID]; ok {
		if previous.UserID != receipt.UserID || previous.ActorID != receipt.ActorID || previous.PayloadHash != receipt.PayloadHash || previous.MemoryID != receipt.MemoryID {
			return false, fmt.Errorf("receipt conflict")
		}
		return true, nil
	}
	s.lastEvent = event
	s.receipts[receipt.RequestID] = receipt
	return false, nil
}

func runtimeConversationArchiveTestEvent(state string) l1sqlite.L1MemoryEvent {
	now := time.Date(2026, 8, 14, 2, 3, 4, 0, time.UTC)
	return l1sqlite.L1MemoryEvent{
		ID: "memory-archive-1", Namespace: "user:user-1", Speaker: domconv.SpeakerMemory,
		Message: "exact L1 memory message", Meta: map[string]interface{}{"statement": "exact L1 memory message", "type": "preference"},
		MemoryState: state, Layer: l1sqlite.MemoryLayerL1, Source: "user_explicit", CreatedAt: now, UpdatedAt: now,
	}
}

func runtimeConversationArchiveUserContext(t *testing.T, requestID, userID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "shiro", AuthenticatedUserID: userID,
		AllowedDataScopes: []string{domaintool.DataScopeUser}, AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("archive owner scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
