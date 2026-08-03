package conversation

import (
	"context"
	"path/filepath"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestL1ConversationManagerPersistsSharedAgentContextAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	engine := NewRealConversationEngine(NewL1ConversationManager(store), domconv.NewMioPersona("test"))
	if err := engine.EndTurnAs(ctx, "viewer-user", "shared token RC_L1_ONLY", "remembered", domconv.SpeakerMio); err != nil {
		t.Fatalf("EndTurnAs: %v", err)
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
	pack, err := restarted.BeginTurn(ctx, "viewer-user", "what was the token?")
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
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	manager := NewL1ConversationManager(store)
	trace := domconv.RecallTrace{
		ResponseID: "response-kuro-1",
		SessionID:  "viewer-user",
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
	got, err := store.RecentRecallTraces(ctx, "viewer-user", 1)
	if err != nil {
		t.Fatalf("RecentRecallTraces: %v", err)
	}
	if len(got) != 1 || got[0].Role != string(domconv.SpeakerKuro) || got[0].ResponseID != "response-kuro-1" {
		t.Fatalf("Agent-attributed recall trace = %#v", got)
	}
}
