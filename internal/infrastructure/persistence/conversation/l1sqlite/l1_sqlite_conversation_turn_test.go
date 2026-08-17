package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func conversationTurnStoreTestRequest(turnID, sessionID string) domconv.ConversationTurnRequest {
	return domconv.ConversationTurnRequest{
		TurnID: turnID, SessionID: sessionID, OwnerID: "owner-1",
		UserMessage: "user-" + turnID, AgentMessage: "agent-" + turnID,
		AgentSpeaker: domconv.SpeakerMio,
	}
}

func TestConversationTurnCommitPersistsTypedResult(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	result, err := store.CommitConversationTurn(context.Background(), domconv.ConversationTurnRequest{
		TurnID:       "turn-red-1",
		SessionID:    "session-red-1",
		OwnerID:      "owner-red-1",
		UserMessage:  "hello",
		AgentMessage: "hi",
		AgentSpeaker: domconv.SpeakerMio,
	})
	if err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed", result.Status)
	}
}

func TestConversationTurnCommitIsAtomicOnSemanticInsertFailure(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE TRIGGER abort_conversation_turn_log BEFORE INSERT ON l1_event_log BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatalf("create abort trigger: %v", err)
	}
	request := conversationTurnStoreTestRequest("turn-atomic", "session-atomic")
	if _, err := store.CommitConversationTurn(context.Background(), request); !errors.Is(err, domconv.ErrConversationTurnInternal) {
		t.Fatalf("commit error=%v, want fixed internal", err)
	}
	for _, table := range []string{"l1_memory_event", "l1_event_log", "l1_profile_promotion_job", "recall_trace", "recall_trace_item", "prompt_injection_event", "conversation_turn_receipt", "conversation_turn_outbox", "conversation_active_thread"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("table %s retained %d rows after rollback", table, count)
		}
	}
}

func TestConversationTurnReplayConflictStableIDsAndTraceProfileCommit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "replay.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	request := conversationTurnStoreTestRequest("turn-replay", "session-replay")
	request.RecallTraceItems = nil
	first, err := store.CommitConversationTurn(context.Background(), request)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	var before int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event`).Scan(&before); err != nil {
		t.Fatalf("memory count: %v", err)
	}
	replay, err := store.CommitConversationTurn(context.Background(), request)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var after int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event`).Scan(&after); err != nil {
		t.Fatalf("memory count after replay: %v", err)
	}
	if before != after || replay.UserMessageID != first.UserMessageID || replay.AgentMessageID != first.AgentMessageID {
		t.Fatalf("replay changed durable pair: before=%d after=%d first=%+v replay=%+v", before, after, first, replay)
	}
	changed := request
	changed.AgentMessage = "changed"
	if _, err := store.CommitConversationTurn(context.Background(), changed); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("hash drift error=%v, want conflict", err)
	}
	var traceCount, itemCount, injectionCount, profileCount, logCount int
	for query, target := range map[string]*int{
		`SELECT count(*) FROM recall_trace WHERE trace_id = ?`:                      &traceCount,
		`SELECT count(*) FROM recall_trace_item WHERE trace_id = ?`:                 &itemCount,
		`SELECT count(*) FROM prompt_injection_event WHERE trace_id = ?`:            &injectionCount,
		`SELECT count(*) FROM l1_profile_promotion_job WHERE evidence_event_id = ?`: &profileCount,
		`SELECT count(*) FROM l1_event_log WHERE session_id = ?`:                    &logCount,
	} {
		argument := request.TurnID
		if strings.Contains(query, "evidence_event_id") {
			argument = first.UserMessageID
		}
		if strings.Contains(query, "session_id") {
			argument = request.SessionID
		}
		if err := store.db.QueryRow(query, argument).Scan(target); err != nil {
			t.Fatalf("query %s: %v", query, err)
		}
	}
	if traceCount != 1 || itemCount != 0 || injectionCount != 0 || profileCount != 1 || logCount != 2 {
		t.Fatalf("trace/profile/log counts=%d/%d/%d/%d/%d", traceCount, itemCount, injectionCount, profileCount, logCount)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	reopenedResult, err := reopened.CommitConversationTurn(context.Background(), request)
	if err != nil || !reopenedResult.IdempotentReplay || reopenedResult.UserMessageID != first.UserMessageID {
		t.Fatalf("reopened replay=%+v err=%v", reopenedResult, err)
	}
}

func TestConversationTurnThreadBoundaryAndOutboxPayloadIsIDOnly(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "threads.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	firstRequest := conversationTurnStoreTestRequest("turn-thread-1", "session-thread")
	firstRequest.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	first, err := store.CommitConversationTurn(ctx, firstRequest)
	if err != nil || first.Status != domconv.ConversationTurnPartial {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	var outboxCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM conversation_turn_outbox WHERE turn_id = ?`, first.TurnID).Scan(&outboxCount); err != nil {
		t.Fatalf("first outbox count: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("thread follower without closure created %d outboxes, want 1 redis outbox", outboxCount)
	}
	var payload string
	if err := store.db.QueryRow(`SELECT payload_json FROM conversation_turn_outbox WHERE turn_id = ?`, first.TurnID).Scan(&payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if strings.Contains(payload, firstRequest.UserMessage) || strings.Contains(payload, firstRequest.AgentMessage) {
		t.Fatalf("outbox payload contains message body: %s", payload)
	}
	var oldThread int64
	if err := store.db.QueryRow(`SELECT thread_id FROM conversation_active_thread WHERE session_id = ?`, firstRequest.SessionID).Scan(&oldThread); err != nil {
		t.Fatalf("active thread: %v", err)
	}
	for i := 2; i <= 6; i++ {
		request := conversationTurnStoreTestRequest("turn-thread-"+string(rune('0'+i)), firstRequest.SessionID)
		if _, err := store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("fill turn %d: %v", i, err)
		}
	}
	boundary := conversationTurnStoreTestRequest("turn-thread-boundary", firstRequest.SessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "new topic"
	boundary.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	result, err := store.CommitConversationTurn(ctx, boundary)
	if err != nil {
		t.Fatalf("boundary commit: %v", err)
	}
	if result.ClosedThreadID != oldThread || result.ThreadID == oldThread || result.Status != domconv.ConversationTurnPartial {
		t.Fatalf("boundary result=%+v old thread=%d", result, oldThread)
	}
	var count int
	if err := store.db.QueryRow(`SELECT message_count FROM conversation_active_thread WHERE session_id = ?`, boundary.SessionID).Scan(&count); err != nil {
		t.Fatalf("boundary active count: %v", err)
	}
	if count != 2 {
		t.Fatalf("new active message_count=%d, want 2", count)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM conversation_turn_outbox WHERE turn_id = ?`, boundary.TurnID).Scan(&outboxCount); err != nil {
		t.Fatalf("boundary outbox count: %v", err)
	}
	if outboxCount != 2 {
		t.Fatalf("boundary outbox count=%d, want redis+thread followers", outboxCount)
	}
}

func TestConversationTurnOutboxLeaseFailureAndCompletion(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	seed := conversationTurnStoreTestRequest("turn-outbox-seed", "session-outbox")
	if _, err := store.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := conversationTurnStoreTestRequest("turn-outbox", "session-outbox")
	request.Boundary = true
	request.BoundaryReason = "close"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	initial, err := store.CommitConversationTurn(ctx, request)
	if err != nil || initial.Status != domconv.ConversationTurnPartial {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	claim, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	secondInitial, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now.Add(30*time.Second), time.Minute)
	if err != nil || secondInitial == nil || secondInitial.Target == claim.Target {
		t.Fatalf("pending sibling was not independently claimed: second=%+v err=%v", secondInitial, err)
	}
	if _, err := store.CompleteConversationTurnOutbox(ctx, request.TurnID, claim.Target, "wrong", now); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("wrong lease error=%v, want conflict", err)
	}
	stale, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now.Add(2*time.Minute), time.Minute)
	if err != nil || stale == nil || stale.LeaseToken == claim.LeaseToken || stale.Attempts != 2 {
		t.Fatalf("stale reclaim=%+v err=%v", stale, err)
	}
	if _, err := store.FailConversationTurnOutbox(ctx, request.TurnID, stale.Target, stale.LeaseToken, domconv.ConversationTurnErrorUnavailable, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("fail outbox: %v", err)
	}
	receipt, err := store.GetConversationTurnReceipt(ctx, request.TurnID)
	if err != nil || receipt.Status != domconv.ConversationTurnPartial || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("failed receipt=%+v err=%v", receipt, err)
	}
	other, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now.Add(3*time.Minute), time.Minute)
	if err != nil || other == nil {
		t.Fatalf("claim remaining target=%+v err=%v", other, err)
	}
	completeAt := now.Add(3 * time.Minute)
	if other.Target == stale.Target {
		if _, err := store.FailConversationTurnOutbox(ctx, request.TurnID, other.Target, other.LeaseToken, domconv.ConversationTurnErrorUnavailable, now.Add(3*time.Minute)); err != nil {
			t.Fatalf("retry failed outbox: %v", err)
		}
		other, err = store.ClaimConversationTurnOutbox(ctx, request.TurnID, now.Add(4*time.Minute), time.Minute)
		if err != nil || other == nil || other.Target == stale.Target {
			t.Fatalf("claim sibling after retry=%+v err=%v", other, err)
		}
		completeAt = now.Add(4 * time.Minute)
	}
	completed, err := store.CompleteConversationTurnOutbox(ctx, request.TurnID, other.Target, other.LeaseToken, completeAt)
	if err != nil {
		t.Fatalf("complete remaining: %v", err)
	}
	if completed.Status != domconv.ConversationTurnFailed {
		t.Fatalf("terminal failed target should keep receipt failed: %+v", completed)
	}
	var rawLastError string
	if err := store.db.QueryRow(`SELECT last_error FROM conversation_turn_outbox WHERE turn_id = ? AND status = 'failed'`, request.TurnID).Scan(&rawLastError); err != nil {
		t.Fatalf("failed outbox error: %v", err)
	}
	if rawLastError != string(domconv.ConversationTurnErrorUnavailable) {
		t.Fatalf("last_error=%q, want fixed unavailable", rawLastError)
	}
}

func TestConversationTurnCompletesOnlyAfterAllOutboxes(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "complete.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	seed := conversationTurnStoreTestRequest("turn-complete-seed", "session-complete")
	if _, err := store.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := conversationTurnStoreTestRequest("turn-complete", "session-complete")
	request.Boundary = true
	request.BoundaryReason = "close"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("commit: %v", err)
	}
	now := time.Now().UTC()
	first, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now, time.Minute)
	if err != nil || first == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	partial, err := store.CompleteConversationTurnOutbox(ctx, request.TurnID, first.Target, first.LeaseToken, now)
	if err != nil || partial.Status != domconv.ConversationTurnPartial {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	second, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now, time.Minute)
	if err != nil || second == nil {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	final, err := store.CompleteConversationTurnOutbox(ctx, request.TurnID, second.Target, second.LeaseToken, now)
	if err != nil || final.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	var completed int
	if err := store.db.QueryRow(`SELECT count(*) FROM conversation_turn_outbox WHERE turn_id = ? AND status = 'completed'`, request.TurnID).Scan(&completed); err != nil {
		t.Fatalf("completed count: %v", err)
	}
	if completed != 2 {
		t.Fatalf("completed outboxes=%d, want 2", completed)
	}
}

func TestConversationTurnGlobalClaimOrdersPendingAndReclaimsStale(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "global-claim.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	first := conversationTurnStoreTestRequest("turn-global-b", "session-global")
	first.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	second := conversationTurnStoreTestRequest("turn-global-a", "session-global")
	second.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := store.CommitConversationTurn(ctx, first); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if _, err := store.CommitConversationTurn(ctx, second); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	equalCreated := time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`UPDATE conversation_turn_outbox SET created_at = ? WHERE turn_id IN (?, ?)`, equalCreated, first.TurnID, second.TurnID); err != nil {
		t.Fatalf("normalize created_at: %v", err)
	}
	claim, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated, time.Minute)
	if err != nil || claim == nil || claim.TurnID != second.TurnID {
		t.Fatalf("global claim=%+v err=%v, want lexical turn-global-a", claim, err)
	}
	if next, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated.Add(30*time.Second), time.Minute); err != nil || next == nil || next.TurnID != first.TurnID {
		t.Fatalf("global sibling claim=%+v err=%v", next, err)
	}
	stale, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated.Add(2*time.Minute), time.Minute)
	if err != nil || stale == nil || stale.TurnID != second.TurnID || stale.Attempts != 2 {
		t.Fatalf("global stale reclaim=%+v err=%v", stale, err)
	}
}

func TestConversationTurnOutboxZeroRowUpdatesReturnConflict(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "zero-update.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	request := conversationTurnStoreTestRequest("turn-zero-update", "session-zero-update")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER ignore_conversation_turn_claim BEFORE UPDATE OF status ON conversation_turn_outbox WHEN NEW.status = 'running' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatalf("create claim trigger: %v", err)
	}
	now := time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC)
	claimed, err := store.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
	if claimed != nil || !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("ignored claim update=%+v err=%v, want conflict", claimed, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER ignore_conversation_turn_claim`); err != nil {
		t.Fatalf("drop claim trigger: %v", err)
	}
	claim, err := store.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim after trigger=%+v err=%v", claim, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER ignore_conversation_turn_complete BEFORE UPDATE OF status ON conversation_turn_outbox WHEN NEW.status = 'completed' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatalf("create complete trigger: %v", err)
	}
	result, err := store.CompleteConversationTurnOutbox(ctx, request.TurnID, claim.Target, claim.LeaseToken, now)
	if !errors.Is(err, domconv.ErrConversationTurnConflict) || result.Status != domconv.ConversationTurnFailed || result.ErrorCode != domconv.ConversationTurnErrorConflict {
		t.Fatalf("ignored completion update result=%+v err=%v, want conflict", result, err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM conversation_turn_outbox WHERE turn_id = ? AND target = ?`, request.TurnID, claim.Target).Scan(&status); err != nil {
		t.Fatalf("outbox status: %v", err)
	}
	if status != string(domconv.ConversationTurnOutboxRunning) {
		t.Fatalf("outbox status=%q after ignored completion, want running", status)
	}
}

func TestConversationTurnSchemaUsesExpectedTables(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	for _, table := range []string{conversationTurnActiveThreadTable, conversationTurnReceiptTable, conversationTurnOutboxTable} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count=%d", table, count)
		}
	}
}

func TestConversationTurnProjectionRequiresExactPairsAndMetadata(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	request := conversationTurnStoreTestRequest("turn-projection", "session-projection")
	request.Domain = "projection-domain"
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	threadID := int64(0)
	if err := store.db.QueryRowContext(ctx, `SELECT thread_id FROM conversation_active_thread WHERE session_id = ?`, request.SessionID).Scan(&threadID); err != nil {
		t.Fatalf("active thread: %v", err)
	}
	projection, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID)
	if err != nil {
		t.Fatalf("LoadConversationThreadProjection: %v", err)
	}
	if len(projection) != 2 || projection[0].Message != request.UserMessage || projection[1].Message != request.AgentMessage {
		t.Fatalf("projection=%+v, want exact two-message body", projection)
	}
	for index, event := range projection {
		if event.Source != "conversation" || event.Layer != MemoryLayerL1 || event.MemoryState != MemoryStateObserved || event.SessionID != request.SessionID || event.ThreadID != threadID {
			t.Fatalf("projection[%d] ownership=%+v", index, event)
		}
		if event.Meta["domain"] != request.Domain || event.Meta["turn_id"] != request.TurnID || event.Meta["speaker"] != string(event.Speaker) {
			t.Fatalf("projection[%d] metadata=%+v", index, event.Meta)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = '{}' WHERE id = ?`, projection[0].ID); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}
	if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("corrupt projection error=%v, want invalid", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, fmt.Sprintf(`{"domain":%q,"message_id":%q,"turn_id":%q,"speaker":"user","from":"user","to":"mio"}`, request.Domain, projection[0].ID, request.TurnID), projection[0].ID); err != nil {
		t.Fatalf("restore metadata: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET namespace = ? WHERE id = ?`, "conv:wrong", projection[0].ID); err != nil {
		t.Fatalf("corrupt namespace: %v", err)
	}
	if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("wrong namespace error=%v, want invalid", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET namespace = ? WHERE id = ?`, fmt.Sprintf("conv:%d", threadID), projection[0].ID); err != nil {
		t.Fatalf("restore namespace: %v", err)
	}
	for i := 0; i < 11; i++ {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, 'user', ?, '{}', ?, ?, ?, ?, ?)`, fmt.Sprintf("extra-%d", i), fmt.Sprintf("conv:%d", threadID), request.SessionID, threadID, "extra", MemoryStateObserved, MemoryLayerL1, "conversation", time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatalf("insert excess projection row %d: %v", i, err)
		}
	}
	if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("excess projection error=%v, want invalid", err)
	}
}

func TestConversationTurnOutboxFailedAttemptsReachTerminalReceipt(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	request := conversationTurnStoreTestRequest("turn-attempts", "session-attempts")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	now := time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)
	for attempt := 1; attempt <= domconv.ConversationTurnMaxOutboxAttempts; attempt++ {
		claim, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now, time.Minute)
		if err != nil || claim == nil || claim.Attempts != attempt {
			t.Fatalf("attempt %d claim=%+v err=%v", attempt, claim, err)
		}
		result, err := store.FailConversationTurnOutbox(ctx, request.TurnID, claim.Target, claim.LeaseToken, domconv.ConversationTurnErrorUnavailable, now)
		if err != nil {
			t.Fatalf("attempt %d fail: %v", attempt, err)
		}
		if attempt < domconv.ConversationTurnMaxOutboxAttempts && result.Status != domconv.ConversationTurnPartial {
			t.Fatalf("attempt %d result=%+v, want partial", attempt, result)
		}
		if attempt == domconv.ConversationTurnMaxOutboxAttempts && (result.Status != domconv.ConversationTurnFailed || result.ErrorCode != domconv.ConversationTurnErrorUnavailable) {
			t.Fatalf("terminal result=%+v, want failed/unavailable", result)
		}
		now = now.Add(time.Minute)
	}
	claim, err := store.ClaimConversationTurnOutbox(ctx, request.TurnID, now, time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("terminal claim=%+v err=%v, want no claim", claim, err)
	}
}

func TestConversationTurnProjectionRejectsOddAndMixedPairs(t *testing.T) {
	ctx := context.Background()
	newProjection := func(t *testing.T) (*L1SQLiteStore, domconv.ConversationTurnRequest, []L1MemoryEvent, int64) {
		t.Helper()
		store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-corrupt.db"))
		if err != nil {
			t.Fatalf("NewL1SQLiteStore: %v", err)
		}
		request := conversationTurnStoreTestRequest("turn-projection-corrupt", "session-projection-corrupt")
		if _, err := store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("commit: %v", err)
		}
		var threadID int64
		if err := store.db.QueryRowContext(ctx, `SELECT thread_id FROM conversation_active_thread WHERE session_id = ?`, request.SessionID).Scan(&threadID); err != nil {
			store.Close()
			t.Fatalf("thread id: %v", err)
		}
		projection, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID)
		if err != nil {
			store.Close()
			t.Fatalf("projection: %v", err)
		}
		return store, request, projection, threadID
	}
	t.Run("odd count", func(t *testing.T) {
		store, request, projection, threadID := newProjection(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `DELETE FROM l1_memory_event WHERE id = ?`, projection[1].ID); err != nil {
			t.Fatalf("delete pair: %v", err)
		}
		if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
			t.Fatalf("odd projection error=%v, want invalid", err)
		}
	})
	t.Run("mixed speaker", func(t *testing.T) {
		store, request, projection, threadID := newProjection(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET speaker = ? WHERE id = ?`, string(domconv.SpeakerUser), projection[1].ID); err != nil {
			t.Fatalf("mix speaker: %v", err)
		}
		if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
			t.Fatalf("mixed projection error=%v, want invalid", err)
		}
	})
	t.Run("mixed turn", func(t *testing.T) {
		store, request, projection, threadID := newProjection(t)
		defer store.Close()
		var meta string
		if err := store.db.QueryRowContext(ctx, `SELECT meta_json FROM l1_memory_event WHERE id = ?`, projection[1].ID).Scan(&meta); err != nil {
			t.Fatalf("load metadata: %v", err)
		}
		meta = strings.Replace(meta, fmt.Sprintf(`"turn_id":"%s"`, request.TurnID), `"turn_id":"other-turn"`, 1)
		if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, meta, projection[1].ID); err != nil {
			t.Fatalf("mix turn: %v", err)
		}
		if _, err := store.LoadConversationThreadProjection(ctx, request.SessionID, threadID); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
			t.Fatalf("mixed turn projection error=%v, want invalid", err)
		}
	})
}

func TestConversationTurnOutboxExhaustedStaleLeaseBecomesTerminalFailure(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "stale-exhausted.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	request := conversationTurnStoreTestRequest("turn-stale-exhausted", "session-stale-exhausted")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	now := time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `UPDATE conversation_turn_outbox SET status = 'running', lease_token = 'crashed', lease_expires_at = ?, attempts = ? WHERE turn_id = ?`, now.Add(-time.Minute), domconv.ConversationTurnMaxOutboxAttempts, request.TurnID); err != nil {
		t.Fatalf("seed stale lease: %v", err)
	}
	claim, err := store.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("exhausted stale claim=%+v err=%v, want no claim", claim, err)
	}
	receipt, err := store.GetConversationTurnReceipt(ctx, request.TurnID)
	if err != nil || receipt.Status != domconv.ConversationTurnFailed || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("exhausted stale receipt=%+v err=%v, want failed/unavailable", receipt, err)
	}
}

func TestConversationTurnOutboxStaleTerminalizationIsTurnScopedAndBounded(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "stale-bounded.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	requests := make([]domconv.ConversationTurnRequest, 3)
	for i := range requests {
		requests[i] = conversationTurnStoreTestRequest(fmt.Sprintf("turn-stale-bounded-%d", i), fmt.Sprintf("session-stale-bounded-%d", i))
		requests[i].Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
		if _, err := store.CommitConversationTurn(ctx, requests[i]); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	now := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
	for i := range requests {
		createdAt := now.Add(time.Duration(i) * time.Second)
		if _, err := store.db.ExecContext(ctx, `UPDATE conversation_turn_outbox SET status = 'running', lease_token = ?, lease_expires_at = ?, attempts = ?, created_at = ?, updated_at = ? WHERE turn_id = ?`, fmt.Sprintf("stale-%d", i), now.Add(-time.Minute), domconv.ConversationTurnMaxOutboxAttempts, createdAt, createdAt, requests[i].TurnID); err != nil {
			t.Fatalf("seed stale row %d: %v", i, err)
		}
	}
	if claim, err := store.ClaimConversationTurnOutbox(ctx, requests[1].TurnID, now, time.Minute); err != nil || claim != nil {
		t.Fatalf("turn-scoped claim=%+v err=%v, want no claim", claim, err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM conversation_turn_outbox WHERE turn_id = ?`, requests[0].TurnID).Scan(&status); err != nil {
		t.Fatalf("row 0 status: %v", err)
	}
	if status != string(domconv.ConversationTurnOutboxRunning) {
		t.Fatalf("row 0 status=%q, turn-scoped claim mutated another turn", status)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM conversation_turn_outbox WHERE turn_id = ?`, requests[1].TurnID).Scan(&status); err != nil {
		t.Fatalf("row 1 status: %v", err)
	}
	if status != string(domconv.ConversationTurnOutboxFailed) {
		t.Fatalf("row 1 status=%q, want terminal failed", status)
	}
	for i := 0; i < 2; i++ {
		if claim, err := store.ClaimNextConversationTurnOutbox(ctx, now, time.Minute); err != nil || claim != nil {
			t.Fatalf("global bounded claim %d=%+v err=%v, want no claim", i, claim, err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT status FROM conversation_turn_outbox WHERE turn_id = ?`, requests[2-i*2].TurnID).Scan(&status); err != nil {
			t.Fatalf("remaining row status %d: %v", i, err)
		}
		if i == 0 && status != string(domconv.ConversationTurnOutboxRunning) {
			t.Fatalf("row 2 status after first global terminalization=%q, want running", status)
		}
	}
	for i := range requests {
		receipt, err := store.GetConversationTurnReceipt(ctx, requests[i].TurnID)
		if err != nil || receipt.Status != domconv.ConversationTurnFailed || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
			t.Fatalf("receipt %d=%+v err=%v, want terminal failed/unavailable", i, receipt, err)
		}
	}
}

// A real RecallPack injects items into more than one prompt section. The
// injection_id primary key must be filled per section, or the second section
// collides on an empty ID and every production turn fails.
func TestConversationTurnCommitPersistsMultiSectionInjectionEvents(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	result, err := store.CommitConversationTurn(context.Background(), domconv.ConversationTurnRequest{
		TurnID:       "turn-multi-section-1",
		SessionID:    "session-multi-section-1",
		OwnerID:      "owner-multi-section-1",
		UserMessage:  "hello",
		AgentMessage: "hi",
		AgentSpeaker: domconv.SpeakerMio,
		RecallTraceItems: []domconv.RecallTraceItem{
			{Layer: "L1", Kind: "short_context", Summary: "recent turn", Status: domconv.TraceStatusInjected, Decision: "included", PromptSection: "[RecallPack: ShortContext]", TokenCount: 10},
			{Layer: "L1", Kind: "rolling_summary", Summary: "summary", Status: domconv.TraceStatusInjected, Decision: "included", PromptSection: "[RecallPack: RollingSummary]", TokenCount: 20},
			{Layer: "L1", Kind: "user_memory", Summary: "", Status: "filtered_status", Decision: "rejected", Reason: "memory state is not confirmed or pinned", PromptSection: "[RecallPack: UserMemory]"},
		},
	})
	if err != nil {
		t.Fatalf("CommitConversationTurn with multi-section trace: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed", result.Status)
	}
	var events int
	if err := store.db.QueryRow(`SELECT count(*) FROM prompt_injection_event WHERE trace_id = 'turn-multi-section-1'`).Scan(&events); err != nil {
		t.Fatalf("count injection events: %v", err)
	}
	if events != 2 {
		t.Fatalf("injection events=%d, want one per injected section", events)
	}
	var distinct int
	if err := store.db.QueryRow(`SELECT count(DISTINCT injection_id) FROM prompt_injection_event WHERE trace_id = 'turn-multi-section-1'`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct injection ids: %v", err)
	}
	if distinct != 2 {
		t.Fatalf("distinct injection ids=%d, want 2 non-colliding ids", distinct)
	}
}
