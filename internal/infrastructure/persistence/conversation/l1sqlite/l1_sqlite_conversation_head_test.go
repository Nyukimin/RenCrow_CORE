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

func newHeadResetRequest(sessionID, ownerID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, operationID string) domconv.ConversationHeadResetRequest {
	return domconv.ConversationHeadResetRequest{
		OperationID: operationID, SessionID: sessionID, OwnerID: ownerID,
		ExpectedThreadID: threadID, ExpectedThreadSeq: threadSeq,
	}
}

func seedConversationHeadForResetTest(t *testing.T, store *L1SQLiteStore, turnID, sessionID string) domconv.ConversationTurnResult {
	t.Helper()
	result, err := store.CommitConversationTurn(context.Background(), conversationTurnStoreTestRequest(turnID, sessionID))
	if err != nil {
		t.Fatalf("seed conversation turn: %v", err)
	}
	return result
}

func assertResetHeadSnapshot(t *testing.T, snapshot domconv.ConversationHeadSnapshot, sessionID, ownerID string, active domconv.ConversationTurnThreadIdentity, domain string) {
	t.Helper()
	if snapshot.SessionID != sessionID || snapshot.OwnerID != ownerID || snapshot.Thread != active || snapshot.Domain != domain ||
		snapshot.MessageCount != 0 || len(snapshot.Messages) != 0 || snapshot.UpdatedAt.IsZero() {
		t.Fatalf("empty head snapshot=%+v, want session=%q owner=%q thread=%+v domain=%q and no messages", snapshot, sessionID, ownerID, active, domain)
	}
}

func TestLatestConversationThreadReferenceIncludesEmptyActiveHead(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "empty-active-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	sessionID := canonicalConversationTurnSessionIDForTest("empty-active-head-session")
	threadID := modulecore.NewThreadID()
	const threadSeq = modulecore.ThreadSeq(4)
	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO conversation_active_thread (session_id, thread_id, thread_seq, thread_kind, domain, message_count, updated_at)
VALUES (?, ?, ?, ?, ?, 0, ?)`, sessionID, threadID, threadSeq, modulecore.ThreadKindUserConversation, "conversation", time.Now().UTC()); err != nil {
		t.Fatalf("insert empty active head: %v", err)
	}

	gotID, gotSeq, gotKind, found, err := store.LatestConversationThreadReference(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LatestConversationThreadReference: %v", err)
	}
	if !found || gotID != threadID || gotSeq != threadSeq || gotKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("LatestConversationThreadReference=(%q,%d,%q,%v), want empty active head (%q,%d,%q,true)", gotID, gotSeq, gotKind, found, threadID, threadSeq, modulecore.ThreadKindUserConversation)
	}
}

func TestConversationHeadResetPersistsEmptyHeadAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "conversation-head-reset.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	sessionID := canonicalConversationTurnSessionIDForTest("head-reset-restart-session")
	seed := seedConversationHeadForResetTest(t, store, "head-reset-restart-seed", sessionID)
	request := newHeadResetRequest(sessionID, "owner-1", seed.ThreadID, seed.ThreadSeq, "head-reset-restart-op")
	receipt, err := store.ResetConversationHead(ctx, request)
	if err != nil {
		t.Fatalf("ResetConversationHead: %v", err)
	}
	if receipt.PreviousThread != (domconv.ConversationTurnThreadIdentity{ID: seed.ThreadID, Seq: seed.ThreadSeq, Kind: seed.ThreadKind}) ||
		receipt.ActiveThread.ID == seed.ThreadID || receipt.ActiveThread.Seq != seed.ThreadSeq+1 || receipt.ActiveThread.Kind != seed.ThreadKind ||
		receipt.MessageCount != 0 || receipt.Domain == "" || receipt.PayloadSHA256 == "" || receipt.ResetAt.IsZero() || receipt.IdempotentReplay {
		t.Fatalf("reset receipt=%+v, want the next empty thread identity", receipt)
	}
	snapshot, err := store.LoadActiveConversationHead(ctx, sessionID, request.OwnerID)
	if err != nil {
		t.Fatalf("LoadActiveConversationHead after reset: %v", err)
	}
	assertResetHeadSnapshot(t, snapshot, sessionID, request.OwnerID, receipt.ActiveThread, receipt.Domain)
	latestID, latestSeq, latestKind, found, err := store.LatestConversationThreadReference(ctx, sessionID)
	if err != nil || !found || latestID != receipt.ActiveThread.ID || latestSeq != receipt.ActiveThread.Seq || latestKind != receipt.ActiveThread.Kind {
		t.Fatalf("latest reference=(%q,%d,%q,%v,%v), want active reset thread", latestID, latestSeq, latestKind, found, err)
	}
	var oldMessageCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event WHERE session_id = ? AND thread_id = ?`, sessionID, seed.ThreadID).Scan(&oldMessageCount); err != nil || oldMessageCount != 2 {
		t.Fatalf("old thread retained messages=%d, err=%v, want 2", oldMessageCount, err)
	}
	if _, err := store.GetConversationTurnReceipt(ctx, string(seed.TurnID)); err != nil {
		t.Fatalf("old turn receipt was not retained: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store, err = NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1: %v", err)
	}
	defer store.Close()
	restarted, err := store.LoadActiveConversationHead(ctx, sessionID, request.OwnerID)
	if err != nil {
		t.Fatalf("LoadActiveConversationHead after restart: %v", err)
	}
	assertResetHeadSnapshot(t, restarted, sessionID, request.OwnerID, receipt.ActiveThread, receipt.Domain)
	replay, err := store.ResetConversationHead(ctx, request)
	if err != nil || !replay.IdempotentReplay || replay.ActiveThread != receipt.ActiveThread {
		t.Fatalf("reset receipt after restart=(%+v,%v), want durable idempotent replay", replay, err)
	}
}

func TestConversationHeadResetCASConflictLeavesCurrentHead(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-head-reset-cas.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := seedConversationHeadForResetTest(t, store, "head-reset-cas-seed", "head-reset-cas-session")
	request := newHeadResetRequest(seed.SessionID, "owner-1", seed.ThreadID, seed.ThreadSeq+1, "head-reset-cas-op")
	if _, err := store.ResetConversationHead(context.Background(), request); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("ResetConversationHead stale CAS error=%v, want conflict", err)
	}
	current := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	if current.Thread.ID != seed.ThreadID || current.Thread.Seq != seed.ThreadSeq || current.MessageCount != 2 {
		t.Fatalf("stale reset changed current head: %+v", current)
	}
}

func TestConversationHeadResetRejectsPendingOutbox(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-head-reset-outbox.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := seedConversationHeadForResetTest(t, store, "head-reset-outbox-seed", "head-reset-outbox-session")
	boundary := conversationTurnStoreTestRequest("head-reset-outbox-boundary", seed.SessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "context_reset"
	boundary.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetThreadFollowers}
	committed, err := store.CommitConversationTurn(context.Background(), boundary)
	if err != nil || committed.ClosedThreadID != seed.ThreadID || committed.Status != domconv.ConversationTurnPartial {
		t.Fatalf("commit boundary turn=(%+v,%v), want pending thread follower", committed, err)
	}
	active := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	request := newHeadResetRequest(seed.SessionID, "owner-1", active.Thread.ID, active.Thread.Seq, "head-reset-outbox-op")
	if _, err := store.ResetConversationHead(context.Background(), request); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("ResetConversationHead with pending outbox error=%v, want conflict", err)
	}
	unchanged := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	if unchanged.Thread != active.Thread || unchanged.MessageCount != active.MessageCount {
		t.Fatalf("pending outbox reset changed active head: before=%+v after=%+v", active, unchanged)
	}
}

func TestConversationHeadResetIsIdempotentForExactOperation(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-head-reset-idempotent.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := seedConversationHeadForResetTest(t, store, "head-reset-idempotent-seed", "head-reset-idempotent-session")
	request := newHeadResetRequest(seed.SessionID, "owner-1", seed.ThreadID, seed.ThreadSeq, "head-reset-idempotent-op")
	first, err := store.ResetConversationHead(context.Background(), request)
	if err != nil {
		t.Fatalf("first reset: %v", err)
	}
	replay, err := store.ResetConversationHead(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent reset replay: %v", err)
	}
	if replay.ActiveThread != first.ActiveThread || replay.PreviousThread != first.PreviousThread || replay.PayloadSHA256 != first.PayloadSHA256 || !replay.IdempotentReplay {
		t.Fatalf("replay=%+v, first=%+v, want same typed receipt marked replay", replay, first)
	}
	changed := request
	changed.OwnerID = "another-owner"
	if _, err := store.ResetConversationHead(context.Background(), changed); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("operation ID reused for changed payload error=%v, want conflict", err)
	}
}

func TestConversationHeadResetRollsBackHeadWhenReceiptInsertFails(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-head-reset-rollback.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := seedConversationHeadForResetTest(t, store, "head-reset-rollback-seed", "head-reset-rollback-session")
	request := newHeadResetRequest(seed.SessionID, "owner-1", seed.ThreadID, seed.ThreadSeq, "head-reset-rollback-op")
	if _, err := store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_head_reset_receipt BEFORE INSERT ON conversation_head_reset_receipt BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`); err != nil {
		t.Fatalf("create receipt failure trigger: %v", err)
	}
	if _, err := store.ResetConversationHead(context.Background(), request); !errors.Is(err, domconv.ErrConversationTurnInternal) {
		t.Fatalf("receipt insertion failure error=%v, want internal", err)
	}
	current := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	if current.Thread.ID != seed.ThreadID || current.Thread.Seq != seed.ThreadSeq || current.MessageCount != 2 {
		t.Fatalf("failed reset did not roll back active head: %+v", current)
	}
	var receiptCount int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM conversation_head_reset_receipt WHERE operation_id = ?`, request.OperationID).Scan(&receiptCount); err != nil {
		t.Fatalf("query failed reset receipt: %v", err)
	}
	if receiptCount != 0 {
		t.Fatalf("failed reset persisted %d receipts, want 0", receiptCount)
	}
}

func TestConversationTurnPlannedCommitConflictsAfterHeadReset(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-head-reset-old-turn.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := seedConversationHeadForResetTest(t, store, "head-reset-old-turn-seed", "head-reset-old-turn-session")
	active := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	stale := planConversationTurnForL1Test(t, conversationTurnStoreTestRequest("head-reset-old-turn-stale", seed.SessionID), &active)
	resetRequest := newHeadResetRequest(seed.SessionID, "owner-1", active.Thread.ID, active.Thread.Seq, "head-reset-old-turn-op")
	reset, err := store.ResetConversationHead(context.Background(), resetRequest)
	if err != nil {
		t.Fatalf("reset active head: %v", err)
	}
	if _, err := store.CommitConversationTurn(context.Background(), stale); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("stale planned EndTurn after reset error=%v, want conflict", err)
	}
	current := loadConversationTurnActiveThreadForL1Test(t, store, seed.SessionID)
	if current.Thread != reset.ActiveThread || current.MessageCount != 0 {
		t.Fatalf("stale EndTurn overwrote reset head: %+v, reset=%+v", current, reset)
	}
}
