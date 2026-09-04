package conversation

import (
	"context"
	"path/filepath"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestL1ConversationManagerReusesLatestCanonicalThreadAfterUnthreadedRows(t *testing.T) {
	ctx := context.Background()
	sessionID := conversationTestSessionID("restart-session")
	store := &mockL1Store{}
	firstManager := NewL1ConversationManager(store)
	firstMessage := domconv.NewMessage(domconv.SpeakerUser, "first", nil)
	if err := firstManager.Store(ctx, sessionID, firstMessage); err != nil {
		t.Fatalf("first Store: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("first saved rows = %d, want 1", len(store.saved))
	}
	canonical := store.saved[0]
	if canonical.ThreadID == "" || canonical.ThreadSeq != 1 || canonical.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("first canonical tuple = %q/%d/%q", canonical.ThreadID, canonical.ThreadSeq, canonical.ThreadKind)
	}

	// These rows are deliberately newer but carry no conversation tuple. A
	// bounded RecentBySession page would hide the canonical row after enough of
	// them; restart recovery must use the owner query instead.
	for i := 0; i < 13; i++ {
		store.saved = append(store.saved, l1sqlite.L1MemoryEvent{
			SessionID:  sessionID,
			Namespace:  "other",
			ThreadID:   "",
			ThreadSeq:  0,
			ThreadKind: "",
			Speaker:    domconv.SpeakerSystem,
			Message:    "unthreaded row",
		})
	}

	// A new manager models a process restart. It must recover the persisted
	// tuple, rather than create a second UUID at sequence one.
	restartedManager := NewL1ConversationManager(store)
	if err := restartedManager.Store(ctx, sessionID, domconv.NewMessage(domconv.SpeakerUser, "after restart", nil)); err != nil {
		t.Fatalf("restart Store: %v", err)
	}
	if len(store.saved) != 15 {
		t.Fatalf("saved rows after restart = %d, want 15", len(store.saved))
	}
	last := store.saved[len(store.saved)-1]
	if last.ThreadID != canonical.ThreadID || last.ThreadSeq != canonical.ThreadSeq || last.ThreadKind != canonical.ThreadKind {
		t.Fatalf("recovered tuple = %q/%d/%q, want %q/%d/%q", last.ThreadID, last.ThreadSeq, last.ThreadKind, canonical.ThreadID, canonical.ThreadSeq, canonical.ThreadKind)
	}
}

func TestL1ConversationManagerCreateThreadReusesLatestTupleAfterRestart(t *testing.T) {
	ctx := context.Background()
	sessionID := conversationTestSessionID("create-restart-session")
	store := &mockL1Store{}
	firstManager := NewL1ConversationManager(store)
	first, err := firstManager.CreateThread(ctx, sessionID, "general")
	if err != nil {
		t.Fatalf("first CreateThread: %v", err)
	}
	if err := firstManager.Store(ctx, sessionID, domconv.NewMessage(domconv.SpeakerUser, "persist tuple", nil)); err != nil {
		t.Fatalf("persist first tuple: %v", err)
	}

	restartedManager := NewL1ConversationManager(store)
	second, err := restartedManager.CreateThread(ctx, sessionID, "general")
	if err != nil {
		t.Fatalf("restart CreateThread: %v", err)
	}
	if second.ID == first.ID || second.ThreadSeq != first.ThreadSeq+1 || second.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("restart thread=%+v, first=%+v", second, first)
	}
}

func TestL1ConversationManagerPersistsSharedAgentContextAcrossReopen(t *testing.T) {
	ctx := context.Background()
	sessionID := conversationTestSessionID("viewer-user")
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	engine := NewRealConversationEngine(NewL1ConversationManager(store), domconv.NewMioPersona("test")).WithUserMemoryStore(store, "ren")
	request := domconv.ConversationTurnRequest{
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		RootTaskID:     modulecore.NewTaskID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		SessionID:      sessionID,
		UserMessage:    "shared token RC_L1_ONLY",
		AgentMessage:   "remembered",
		AgentSpeaker:   domconv.SpeakerMio,
	}
	if _, err := engine.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1: %v", err)
	}
	defer reopened.Close()
	restarted := NewRealConversationEngine(NewL1ConversationManager(reopened), domconv.NewMioPersona("test"))
	pack, err := restarted.BeginTurn(ctx, sessionID, "what was the token?")
	if err != nil {
		t.Fatalf("BeginTurn after reopen: %v", err)
	}
	if len(pack.ShortContext) != 2 {
		t.Fatalf("ShortContext len = %d, want 2: %#v", len(pack.ShortContext), pack.ShortContext)
	}
	if pack.ShortContext[0].Speaker != domconv.SpeakerUser || pack.ShortContext[0].Msg != "shared token RC_L1_ONLY" {
		t.Fatalf("user context = %#v", pack.ShortContext[0])
	}
	if pack.ShortContext[1].Speaker != domconv.SpeakerMio || pack.ShortContext[1].Msg != "remembered" {
		t.Fatalf("Agent context = %#v", pack.ShortContext[1])
	}
}

func TestL1ConversationManagerPersistsAgentAttributedRecallTrace(t *testing.T) {
	ctx := context.Background()
	sessionID := conversationTestSessionID("viewer-user")
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	manager := NewL1ConversationManager(store)
	trace := domconv.RecallTrace{
		TraceID:    modulecore.NewTraceID(),
		TurnID:     modulecore.NewTurnID(),
		RootTaskID: modulecore.NewTaskID(),
		SessionID:  sessionID,
		Role:       string(domconv.SpeakerKuro),
		Items: []domconv.RecallTraceItem{{
			Layer:   "L1",
			Kind:    "short_context",
			Summary: "RC_SHARED_1234",
		}},
	}
	if err := manager.SaveRecallTrace(ctx, trace); err != nil {
		t.Fatalf("SaveRecallTrace: %v", err)
	}
	got, err := store.RecentRecallTraces(ctx, sessionID, 1)
	if err != nil {
		t.Fatalf("RecentRecallTraces: %v", err)
	}
	if len(got) != 1 || got[0].Role != string(domconv.SpeakerKuro) || got[0].TraceID != trace.TraceID || got[0].TurnID != trace.TurnID || got[0].RootTaskID != trace.RootTaskID {
		t.Fatalf("Agent-attributed recall trace = %#v", got)
	}
}

func TestL1ConversationManagerCommitConversationTurnDelegatesOnlyWithoutTargets(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1-turn.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	manager := NewL1ConversationManager(store)
	request := domconv.ConversationTurnRequest{
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		RootTaskID:     modulecore.NewTaskID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		SessionID:      string(modulecore.NewSessionID()),
		OwnerID:        "owner",
		UserMessage:    "hello",
		AgentMessage:   "hi",
		AgentSpeaker:   domconv.SpeakerMio,
	}
	result, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || result.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("empty target result=%+v err=%v", result, err)
	}
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	rejected, err := manager.CommitConversationTurn(ctx, request)
	if err == nil || rejected.Status != domconv.ConversationTurnFailed || rejected.ErrorCode != domconv.ConversationTurnErrorInvalid {
		t.Fatalf("target result=%+v err=%v, want invalid without fallback", rejected, err)
	}
	replayed, err := store.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil || replayed.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("target rejection changed L1 receipt=%+v err=%v", replayed, err)
	}
}
