package l1sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func canonicalProjectionFixture(t *testing.T) []domconv.CanonicalConversationProjection {
	t.Helper()
	sessionID, turnID, threadID := modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewThreadID()
	items := []domconv.CanonicalConversationProjection{
		{ConversationProjectionSource: domconv.ConversationProjectionSource{
			SessionID: sessionID, TurnID: turnID, MessageID: modulecore.NewMessageID(), EventID: modulecore.NewEventID(), EventSeq: 10,
			ActorID: "user-1", Speaker: "user", Text: "question", OccurredAt: time.Date(2026, 10, 22, 2, 3, 4, 0, time.UTC), Visibility: "public",
		}, ThreadID: threadID, ThreadSeq: 1, ThreadKind: modulecore.ThreadKindUserConversation},
		{ConversationProjectionSource: domconv.ConversationProjectionSource{
			SessionID: sessionID, TurnID: turnID, MessageID: modulecore.NewMessageID(), EventID: modulecore.NewEventID(), EventSeq: 11,
			ActorID: "mio", Speaker: "mio", Text: "answer", OccurredAt: time.Date(2026, 10, 22, 2, 3, 5, 0, time.UTC), Visibility: "public",
		}, ThreadID: threadID, ThreadSeq: 2, ThreadKind: modulecore.ThreadKindUserConversation},
	}
	for i := range items {
		hash, err := domconv.ConversationProjectionContentHash(items[i])
		if err != nil {
			t.Fatalf("ConversationProjectionContentHash(): %v", err)
		}
		items[i].ContentSHA256 = hash
	}
	return items
}

func TestApplyCanonicalConversationProjectionIsIdempotentAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	items := canonicalProjectionFixture(t)
	first, err := store.ApplyCanonicalConversationProjection(context.Background(), items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(first): %v", err)
	}
	if first.SessionID != items[0].SessionID || first.AppliedThrough != 11 || len(first.Items) != 2 {
		t.Fatalf("first receipt = %#v, want two items through event 11", first)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	store, err = NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewL1SQLiteStore(): %v", err)
	}
	defer store.Close()
	second, err := store.ApplyCanonicalConversationProjection(context.Background(), items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(retry): %v", err)
	}
	if second.AppliedThrough != first.AppliedThrough || len(second.Items) != len(first.Items) {
		t.Fatalf("retry receipt = %#v, want original identity", second)
	}
	var events, receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM l1_memory_event WHERE source='conversation_projection'`).Scan(&events); err != nil {
		t.Fatalf("count projected events: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM l1_canonical_conversation_projection_receipt`).Scan(&receipts); err != nil {
		t.Fatalf("count projection receipts: %v", err)
	}
	if events != 2 || receipts != 2 {
		t.Fatalf("projected events/receipts = %d/%d, want 2/2 after retry", events, receipts)
	}
}

func TestApplyCanonicalConversationProjectionRejectsPrivateAndChangedRetry(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer store.Close()
	items := canonicalProjectionFixture(t)
	private := append([]domconv.CanonicalConversationProjection(nil), items[:1]...)
	private[0].Visibility = "private"
	if _, err := store.ApplyCanonicalConversationProjection(context.Background(), private); !errors.Is(err, domconv.ErrInvalidConversationProjection) {
		t.Fatalf("ApplyCanonicalConversationProjection(private) error = %v, want public validation error", err)
	}
	var before int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM l1_memory_event WHERE source='conversation_projection'`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("private projection rows before valid apply = %d, err=%v; want zero", before, err)
	}
	if _, err := store.ApplyCanonicalConversationProjection(context.Background(), items); err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(valid): %v", err)
	}
	changed := append([]domconv.CanonicalConversationProjection(nil), items...)
	changed[0].Text = "tampered"
	changed[0].ContentSHA256, err = domconv.ConversationProjectionContentHash(changed[0])
	if err != nil {
		t.Fatalf("hash changed item: %v", err)
	}
	if _, err := store.ApplyCanonicalConversationProjection(context.Background(), changed); !errors.Is(err, ErrCanonicalConversationProjectionConflict) {
		t.Fatalf("ApplyCanonicalConversationProjection(changed retry) error = %v, want conflict", err)
	}
}

func TestApplyCanonicalConversationProjectionAcceptsSelectedPublicUserSingleton(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1-singleton.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer store.Close()
	item := canonicalProjectionFixture(t)[0]
	item.ThreadID = modulecore.NewThreadID()
	item.ThreadSeq = 1
	hash, err := domconv.ConversationProjectionContentHash(item)
	if err != nil {
		t.Fatalf("ConversationProjectionContentHash(): %v", err)
	}
	item.ContentSHA256 = hash
	receipt, err := store.ApplyCanonicalConversationProjection(context.Background(), []domconv.CanonicalConversationProjection{item})
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(user singleton): %v", err)
	}
	if receipt.SessionID != item.SessionID || receipt.AppliedThrough != item.EventSeq || len(receipt.Items) != 1 || receipt.Items[0].MessageID != item.MessageID {
		t.Fatalf("singleton receipt = %#v, want one selected user source", receipt)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM l1_memory_event WHERE source='conversation_projection' AND speaker='user'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("projected user singleton count = %d, err=%v; want 1", count, err)
	}
}
