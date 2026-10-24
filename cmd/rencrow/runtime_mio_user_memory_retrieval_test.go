package main

import (
	"context"
	"path/filepath"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeMioRetrievalReadsConfirmedUserMemoryFromPreparedLocalL1(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "user-memory.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	proposed, err := store.OwnerProposeUserMemory(ctx, "propose-blue-tea", "ren", "ren", domainmemory.UserMemoryTypePreference, "Ren prefers blue tea", "confirmed by Ren")
	if err != nil {
		t.Fatalf("OwnerProposeUserMemory: %v", err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "confirm-blue-tea", "ren", "ren", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "confirmed by Ren"); err != nil {
		t.Fatalf("OwnerTransitionUserMemory(confirm): %v", err)
	}

	prepared, err := conversationpersistence.NewPreparedRecallRuntime("ren", store)
	if err != nil {
		t.Fatalf("NewPreparedRecallRuntime: %v", err)
	}
	if err := prepared.Warm(ctx); err != nil {
		t.Fatalf("PreparedRecallRuntime.Warm: %v", err)
	}
	defer prepared.Close()

	address, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	input, err := domainconversation.NewTurnInput(modulecore.NewTaskID(), "Do you remember my tea preference?", address)
	if err != nil {
		t.Fatalf("NewTurnInput: %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID()))
	binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		modulecore.SessionID(input.SessionID()), input.ChannelAddress(), domainconversation.SessionAudiencePrivate,
		"ren", 1, "test-publication-incarnation",
	)
	if err != nil {
		t.Fatalf("NewSessionBindingWithPublicationIncarnation: %v", err)
	}

	userCtx := authenticatedMioRetrievalContext(t, "ren")
	mioCtx, err := domaintool.DeriveAgentToolExecutionScope(userCtx, "mio-user-memory-read", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	mioCtx = domainconversation.WithSessionBinding(mioCtx, binding)
	port := newRuntimeMioRetrieval(nil, nil)
	port.userMemoryStore = store
	port.preparedRecall = prepared
	result, err := port.Retrieve(mioCtx, input, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceUserMemory,
		Query:      "tea preference",
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if result.Status != domainagent.MioRetrievalStatusFound || len(result.Items) != 1 || result.Items[0].Text != "Ren prefers blue tea" {
		t.Fatalf("local L1 UserMemory result = %#v; want the confirmed owner statement", result)
	}
	unsupportedReference, err := port.Retrieve(mioCtx, input, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceUserMemory,
		Query:      "tea preference",
		References: []string{"memory-not-authorized-by-owner"},
	})
	if err != nil {
		t.Fatalf("Retrieve(unsupported reference): %v", err)
	}
	if unsupportedReference.Status != domainagent.MioRetrievalStatusUnavailable || len(unsupportedReference.Items) != 0 {
		t.Fatalf("unsupported UserMemory reference result = %#v; want fail-closed unavailable with no evidence", unsupportedReference)
	}
}

func TestRuntimeMioUserMemoryIncompleteEmptyReadIsUnavailableNotNotFound(t *testing.T) {
	if got := runtimeMioUserMemoryResultStatus(domainmemory.UserMemoryChatReadResult{HasMore: true}); got != domainagent.MioRetrievalStatusUnavailable {
		t.Fatalf("incomplete empty UserMemory read status=%q, want unavailable", got)
	}
	if got := runtimeMioUserMemoryResultStatus(domainmemory.UserMemoryChatReadResult{
		HasMore: true, Items: []domainmemory.UserMemoryChatRecallItem{{}},
	}); got != domainagent.MioRetrievalStatusPartial {
		t.Fatalf("incomplete positive UserMemory read status=%q, want partial", got)
	}
	if got := runtimeMioUserMemoryResultStatus(domainmemory.UserMemoryChatReadResult{}); got != domainagent.MioRetrievalStatusNotFound {
		t.Fatalf("complete empty UserMemory read status=%q, want not_found", got)
	}
}
