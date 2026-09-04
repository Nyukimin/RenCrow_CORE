package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func conversationTurnStoreTestRequest(turnID, sessionID string) domconv.ConversationTurnRequest {
	return domconv.ConversationTurnRequest{
		TurnID:         modulecore.TurnID(canonicalConversationTurnIdentityForTest(modulecore.CanonicalTurnID, turnID)),
		TraceID:        modulecore.TraceID(canonicalConversationTurnIdentityForTest(modulecore.CanonicalTraceID, "trace:"+turnID)),
		RootTaskID:     modulecore.TaskID(canonicalConversationTurnIdentityForTest(modulecore.CanonicalTaskID, "root:"+turnID)),
		UserMessageID:  modulecore.MessageID(canonicalConversationTurnIdentityForTest(modulecore.CanonicalMessageID, "user:"+turnID)),
		AgentMessageID: modulecore.MessageID(canonicalConversationTurnIdentityForTest(modulecore.CanonicalMessageID, "agent:"+turnID)),
		SessionID:      canonicalConversationTurnSessionIDForTest(sessionID), OwnerID: "owner-1",
		UserMessage: "user-" + turnID, AgentMessage: "agent-" + turnID,
		AgentSpeaker: domconv.SpeakerMio,
	}
}

func canonicalConversationTurnIdentityForTest(kind modulecore.CanonicalIDType, source string) string {
	id, err := modulecore.NewMigrationID(kind, "conversation_turn_test", "identity", source)
	if err != nil {
		panic(err)
	}
	return id
}

func assertConversationTurnIdentity(t *testing.T, request domconv.ConversationTurnRequest, result domconv.ConversationTurnResult) {
	t.Helper()
	if result.TurnID != request.TurnID || result.TraceID != request.TraceID || result.RootTaskID != request.RootTaskID ||
		result.UserMessageID != request.UserMessageID || result.AgentMessageID != request.AgentMessageID ||
		len(result.MessageIDs) != 2 || result.MessageIDs[0] != string(request.UserMessageID) || result.MessageIDs[1] != string(request.AgentMessageID) {
		t.Fatalf("identity result=%+v, want turn=%s trace=%s root=%s user=%s agent=%s", result, request.TurnID, request.TraceID, request.RootTaskID, request.UserMessageID, request.AgentMessageID)
	}
}

func canonicalConversationTurnSessionIDForTest(source string) string {
	if id := modulecore.SessionID(source); id.Validate() == nil {
		return source
	}
	id, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "conversation_turn_test", "session_id", source)
	if err != nil {
		panic(err)
	}
	return id
}

func TestConversationTurnCommitPersistsTypedResult(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	request := conversationTurnStoreTestRequest("turn-red-1", "session-red-1")
	request.OwnerID = "owner-red-1"
	request.UserMessage = "hello"
	request.AgentMessage = "hi"
	result, err := store.CommitConversationTurn(context.Background(), request)
	if err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed", result.Status)
	}
	assertConversationTurnIdentity(t, request, result)
	var storedTurnID, storedTraceID, storedRootTaskID, storedUserMessageID, storedAgentMessageID string
	if err := store.db.QueryRow(`SELECT turn_id, trace_id, root_task_id, user_message_id, agent_message_id FROM conversation_turn_receipt WHERE turn_id = ?`, request.TurnID).
		Scan(&storedTurnID, &storedTraceID, &storedRootTaskID, &storedUserMessageID, &storedAgentMessageID); err != nil {
		t.Fatalf("receipt identity: %v", err)
	}
	if storedTurnID != string(request.TurnID) || storedTraceID != string(request.TraceID) || storedRootTaskID != string(request.RootTaskID) ||
		storedUserMessageID != string(request.UserMessageID) || storedAgentMessageID != string(request.AgentMessageID) {
		t.Fatalf("receipt identity=%q/%q/%q/%q/%q", storedTurnID, storedTraceID, storedRootTaskID, storedUserMessageID, storedAgentMessageID)
	}
	var recallTraceID, recallTurnID, recallRootTaskID string
	if err := store.db.QueryRow(`SELECT trace_id, turn_id, root_task_id FROM recall_trace WHERE trace_id = ?`, request.TraceID).
		Scan(&recallTraceID, &recallTurnID, &recallRootTaskID); err != nil {
		t.Fatalf("recall identity: %v", err)
	}
	if recallTraceID != string(request.TraceID) || recallTurnID != string(request.TurnID) || recallRootTaskID != string(request.RootTaskID) {
		t.Fatalf("recall identity=%q/%q/%q", recallTraceID, recallTurnID, recallRootTaskID)
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
	assertConversationTurnIdentity(t, request, first)
	var before int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event`).Scan(&before); err != nil {
		t.Fatalf("memory count: %v", err)
	}
	replay, err := store.CommitConversationTurn(context.Background(), request)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	assertConversationTurnIdentity(t, request, replay)
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
		argument := string(request.TraceID)
		if strings.Contains(query, "evidence_event_id") {
			argument = string(first.UserMessageID)
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
	assertConversationTurnIdentity(t, request, reopenedResult)
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
	assertConversationTurnIdentity(t, firstRequest, first)
	if first.ThreadSeq != 1 || first.ClosedThreadID != "" || first.ClosedThreadSeq != 0 {
		t.Fatalf("first thread tuple=%s/%d closed=%s/%d, want seq1 and no closed thread", first.ThreadID, first.ThreadSeq, first.ClosedThreadID, first.ClosedThreadSeq)
	}
	if err := first.ThreadID.Validate(); err != nil {
		t.Fatalf("first thread ID is not canonical: %v", err)
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
	var firstPayload struct {
		Version    string                `json:"version"`
		TurnID     modulecore.TurnID     `json:"turn_id"`
		TraceID    modulecore.TraceID    `json:"trace_id"`
		RootTaskID modulecore.TaskID     `json:"root_task_id"`
		SessionID  string                `json:"session_id"`
		ThreadID   modulecore.ThreadID   `json:"thread_id"`
		ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
		ThreadKind modulecore.ThreadKind `json:"thread_kind"`
		UserID     modulecore.MessageID  `json:"user_message_id"`
		AgentID    modulecore.MessageID  `json:"agent_message_id"`
	}
	if err := json.Unmarshal([]byte(payload), &firstPayload); err != nil {
		t.Fatalf("decode first outbox payload: %v", err)
	}
	if firstPayload.Version != "rencrow.conversation_turn_outbox.v2" || firstPayload.TurnID != firstRequest.TurnID || firstPayload.TraceID != firstRequest.TraceID || firstPayload.RootTaskID != firstRequest.RootTaskID || firstPayload.UserID != firstRequest.UserMessageID || firstPayload.AgentID != firstRequest.AgentMessageID || firstPayload.SessionID != firstRequest.SessionID || firstPayload.ThreadID != first.ThreadID || firstPayload.ThreadSeq != 1 || firstPayload.ThreadKind != domconv.ThreadKindUserConversation {
		t.Fatalf("first outbox payload tuple=%+v, want session/thread/seq1/kind", firstPayload)
	}

	var oldThread modulecore.ThreadID
	if err := store.db.QueryRow(`SELECT thread_id FROM conversation_active_thread WHERE session_id = ?`, firstRequest.SessionID).Scan(&oldThread); err != nil {
		t.Fatalf("active thread: %v", err)
	}
	for i := 2; i <= 6; i++ {
		request := conversationTurnStoreTestRequest("turn-thread-"+string(rune('0'+i)), firstRequest.SessionID)
		reused, err := store.CommitConversationTurn(ctx, request)
		if err != nil {
			t.Fatalf("fill turn %d: %v", i, err)
		}
		if i == 2 {
			if reused.ThreadID != first.ThreadID || reused.ThreadSeq != 1 || reused.ClosedThreadID != "" || reused.ClosedThreadSeq != 0 {
				t.Fatalf("reused thread tuple=%s/%d closed=%s/%d, want same seq1 tuple", reused.ThreadID, reused.ThreadSeq, reused.ClosedThreadID, reused.ClosedThreadSeq)
			}
			if err := reused.ThreadID.Validate(); err != nil {
				t.Fatalf("reused thread ID is not canonical: %v", err)
			}
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
	assertConversationTurnIdentity(t, boundary, result)
	if result.ClosedThreadID != oldThread || result.ThreadID == oldThread || result.ThreadSeq != 2 || result.ClosedThreadSeq != 1 || result.Status != domconv.ConversationTurnPartial {
		t.Fatalf("boundary result=%+v old thread=%s", result, oldThread)
	}
	if err := result.ThreadID.Validate(); err != nil {
		t.Fatalf("boundary thread ID is not canonical: %v", err)
	}
	if err := result.ClosedThreadID.Validate(); err != nil {
		t.Fatalf("closed thread ID is not canonical: %v", err)
	}
	receipt, err := store.GetConversationTurnReceipt(ctx, string(boundary.TurnID))
	if err != nil {
		t.Fatalf("boundary receipt: %v", err)
	}
	if receipt.ThreadID != result.ThreadID || receipt.ThreadSeq != result.ThreadSeq || receipt.ThreadKind != result.ThreadKind || receipt.ClosedThreadID != result.ClosedThreadID || receipt.ClosedThreadSeq != result.ClosedThreadSeq || receipt.ClosedThreadKind != result.ClosedThreadKind {
		t.Fatalf("boundary receipt tuple=%q/%d/%q closed=%q/%d/%q, result=%q/%d/%q closed=%q/%d/%q", receipt.ThreadID, receipt.ThreadSeq, receipt.ThreadKind, receipt.ClosedThreadID, receipt.ClosedThreadSeq, receipt.ClosedThreadKind, result.ThreadID, result.ThreadSeq, result.ThreadKind, result.ClosedThreadID, result.ClosedThreadSeq, result.ClosedThreadKind)
	}
	for i := 0; i < 2; i++ {
		claim, claimErr := store.ClaimConversationTurnOutbox(ctx, string(boundary.TurnID), time.Now().UTC(), time.Minute)
		if claimErr != nil || claim == nil {
			t.Fatalf("boundary outbox claim %d=%+v err=%v", i, claim, claimErr)
		}
		if claim.TurnID != boundary.TurnID || claim.TraceID != boundary.TraceID || claim.RootTaskID != boundary.RootTaskID || claim.ThreadID != result.ThreadID || claim.ThreadSeq != result.ThreadSeq || claim.ThreadKind != result.ThreadKind || claim.ClosedThreadID != result.ClosedThreadID || claim.ClosedThreadSeq != result.ClosedThreadSeq || claim.ClosedThreadKind != result.ClosedThreadKind {
			t.Fatalf("boundary outbox claim %d tuple=%q/%d/%q closed=%q/%d/%q", i, claim.ThreadID, claim.ThreadSeq, claim.ThreadKind, claim.ClosedThreadID, claim.ClosedThreadSeq, claim.ClosedThreadKind)
		}
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
	rows, err := store.db.Query(`SELECT payload_json FROM conversation_turn_outbox WHERE turn_id = ?`, boundary.TurnID)
	if err != nil {
		t.Fatalf("boundary payloads: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rawPayload string
		if err := rows.Scan(&rawPayload); err != nil {
			t.Fatalf("scan boundary payload: %v", err)
		}
		var boundaryPayload struct {
			Version          string                `json:"version"`
			TurnID           modulecore.TurnID     `json:"turn_id"`
			TraceID          modulecore.TraceID    `json:"trace_id"`
			RootTaskID       modulecore.TaskID     `json:"root_task_id"`
			SessionID        string                `json:"session_id"`
			ThreadID         modulecore.ThreadID   `json:"thread_id"`
			ThreadSeq        modulecore.ThreadSeq  `json:"thread_seq"`
			ThreadKind       modulecore.ThreadKind `json:"thread_kind"`
			ClosedThreadID   modulecore.ThreadID   `json:"closed_thread_id"`
			ClosedThreadSeq  modulecore.ThreadSeq  `json:"closed_thread_seq"`
			ClosedThreadKind modulecore.ThreadKind `json:"closed_thread_kind"`
			UserID           modulecore.MessageID  `json:"user_message_id"`
			AgentID          modulecore.MessageID  `json:"agent_message_id"`
		}
		if err := json.Unmarshal([]byte(rawPayload), &boundaryPayload); err != nil {
			t.Fatalf("decode boundary payload: %v", err)
		}
		if boundaryPayload.Version != "rencrow.conversation_turn_outbox.v2" || boundaryPayload.TurnID != boundary.TurnID || boundaryPayload.TraceID != boundary.TraceID || boundaryPayload.RootTaskID != boundary.RootTaskID || boundaryPayload.UserID != boundary.UserMessageID || boundaryPayload.AgentID != boundary.AgentMessageID || boundaryPayload.SessionID != boundary.SessionID || boundaryPayload.ThreadID != result.ThreadID || boundaryPayload.ThreadSeq != 2 || boundaryPayload.ThreadKind != domconv.ThreadKindUserConversation || boundaryPayload.ClosedThreadID != oldThread || boundaryPayload.ClosedThreadSeq != 1 || boundaryPayload.ClosedThreadKind != domconv.ThreadKindUserConversation {
			t.Fatalf("boundary outbox payload tuple=%+v", boundaryPayload)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate boundary payloads: %v", err)
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
	assertConversationTurnIdentity(t, request, initial)
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	claim, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if claim.TurnID != request.TurnID || claim.TraceID != request.TraceID || claim.RootTaskID != request.RootTaskID {
		t.Fatalf("claim identity=%+v, want turn=%s trace=%s root=%s", claim, request.TurnID, request.TraceID, request.RootTaskID)
	}
	secondInitial, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now.Add(30*time.Second), time.Minute)
	if err != nil || secondInitial == nil || secondInitial.Target == claim.Target {
		t.Fatalf("pending sibling was not independently claimed: second=%+v err=%v", secondInitial, err)
	}
	if _, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), claim.Target, "wrong", now); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("wrong lease error=%v, want conflict", err)
	}
	stale, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now.Add(2*time.Minute), time.Minute)
	if err != nil || stale == nil || stale.LeaseToken == claim.LeaseToken || stale.Attempts != 2 {
		t.Fatalf("stale reclaim=%+v err=%v", stale, err)
	}
	if _, err := store.FailConversationTurnOutbox(ctx, string(request.TurnID), stale.Target, stale.LeaseToken, domconv.ConversationTurnErrorUnavailable, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("fail outbox: %v", err)
	}
	receipt, err := store.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil || receipt.Status != domconv.ConversationTurnPartial || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("failed receipt=%+v err=%v", receipt, err)
	}
	assertConversationTurnIdentity(t, request, receipt)
	other, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now.Add(3*time.Minute), time.Minute)
	if err != nil || other == nil {
		t.Fatalf("claim remaining target=%+v err=%v", other, err)
	}
	completeAt := now.Add(3 * time.Minute)
	if other.Target == stale.Target {
		if _, err := store.FailConversationTurnOutbox(ctx, string(request.TurnID), other.Target, other.LeaseToken, domconv.ConversationTurnErrorUnavailable, now.Add(3*time.Minute)); err != nil {
			t.Fatalf("retry failed outbox: %v", err)
		}
		other, err = store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now.Add(4*time.Minute), time.Minute)
		if err != nil || other == nil || other.Target == stale.Target {
			t.Fatalf("claim sibling after retry=%+v err=%v", other, err)
		}
		completeAt = now.Add(4 * time.Minute)
	}
	completed, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), other.Target, other.LeaseToken, completeAt)
	if err != nil {
		t.Fatalf("complete remaining: %v", err)
	}
	if completed.Status != domconv.ConversationTurnFailed {
		t.Fatalf("terminal failed target should keep receipt failed: %+v", completed)
	}
	assertConversationTurnIdentity(t, request, completed)
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
	first, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
	if err != nil || first == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	partial, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), first.Target, first.LeaseToken, now)
	if err != nil || partial.Status != domconv.ConversationTurnPartial {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	assertConversationTurnIdentity(t, request, partial)
	second, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
	if err != nil || second == nil {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	final, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), second.Target, second.LeaseToken, now)
	if err != nil || final.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	assertConversationTurnIdentity(t, request, final)
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
	wantFirst := first
	wantSecond := second
	if string(wantFirst.TurnID) > string(wantSecond.TurnID) {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	claim, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated, time.Minute)
	if err != nil || claim == nil || claim.TurnID != wantFirst.TurnID {
		t.Fatalf("global claim=%+v err=%v, want lexical first turn", claim, err)
	}
	if next, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated.Add(30*time.Second), time.Minute); err != nil || next == nil || next.TurnID != wantSecond.TurnID {
		t.Fatalf("global sibling claim=%+v err=%v", next, err)
	}
	stale, err := store.ClaimNextConversationTurnOutbox(ctx, equalCreated.Add(2*time.Minute), time.Minute)
	if err != nil || stale == nil || stale.TurnID != wantFirst.TurnID || stale.Attempts != 2 {
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
	result, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), claim.Target, claim.LeaseToken, now)
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
	for _, table := range []string{"recall_trace", conversationTurnActiveThreadTable, conversationTurnReceiptTable, conversationTurnOutboxTable} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count=%d", table, count)
		}
	}
	for _, table := range []struct {
		name   string
		closed bool
	}{
		{name: "l1_memory_event"},
		{name: "l1_event_log"},
		{name: "l1_profile_promotion_job"},
		{name: conversationTurnActiveThreadTable},
		{name: conversationTurnReceiptTable, closed: true},
		{name: conversationTurnOutboxTable, closed: true},
	} {
		rows, err := store.db.Query(`PRAGMA table_info(` + table.name + `)`)
		if err != nil {
			t.Fatalf("table info %s: %v", table.name, err)
		}
		columns := map[string]string{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, columnType string
			var defaultValue interface{}
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan table info %s: %v", table.name, err)
			}
			columns[name] = strings.ToUpper(strings.TrimSpace(columnType))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate table info %s: %v", table.name, err)
		}
		rows.Close()
		for column, want := range map[string]string{"thread_id": "TEXT", "thread_seq": "INTEGER", "thread_kind": "TEXT"} {
			if columns[column] != want {
				t.Fatalf("table %s column %s type=%q, want %q", table.name, column, columns[column], want)
			}
		}
		if table.closed {
			for column, want := range map[string]string{"closed_thread_id": "TEXT", "closed_thread_seq": "INTEGER", "closed_thread_kind": "TEXT"} {
				if columns[column] != want {
					t.Fatalf("table %s column %s type=%q, want %q", table.name, column, columns[column], want)
				}
			}
		}
	}
	for table, required := range map[string][]string{
		"recall_trace":               {"root_task_id"},
		conversationTurnReceiptTable: {"root_task_id"},
		conversationTurnOutboxTable:  {"trace_id", "root_task_id"},
	} {
		rows, err := store.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatalf("identity table info %s: %v", table, err)
		}
		type columnInfo struct {
			typeName string
			notNull  int
		}
		columns := map[string]columnInfo{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, columnType string
			var defaultValue interface{}
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan identity table info %s: %v", table, err)
			}
			columns[name] = columnInfo{typeName: strings.ToUpper(strings.TrimSpace(columnType)), notNull: notNull}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate identity table info %s: %v", table, err)
		}
		rows.Close()
		for _, column := range required {
			info, ok := columns[column]
			if !ok || info.typeName != "TEXT" || info.notNull != 1 {
				t.Fatalf("table %s identity column %s=%+v, want required TEXT", table, column, info)
			}
		}
	}
}

func TestNewL1SQLiteStoreRejectsLegacyThreadSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-thread-schema.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE l1_memory_event (
	id TEXT PRIMARY KEY,
	namespace TEXT NOT NULL,
	session_id TEXT NOT NULL,
	thread_id INTEGER NOT NULL,
	speaker TEXT NOT NULL,
	message TEXT NOT NULL,
	meta_json TEXT NOT NULL DEFAULT '{}',
	memory_state TEXT NOT NULL,
	layer TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_l1_memory_thread_created ON l1_memory_event(thread_id, created_at DESC);`); err != nil {
		db.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
	store, err := NewL1SQLiteStore(dbPath)
	if err == nil {
		store.Close()
		t.Fatal("NewL1SQLiteStore accepted legacy INTEGER thread_id schema")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "canonical thread schema") {
		t.Fatalf("legacy schema error=%v, want canonical schema rejection", err)
	}
}

func TestLatestConversationThreadReferenceIgnoresNewerUnthreadedRows(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "latest-thread-reference.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	sessionID := canonicalConversationTurnSessionIDForTest("latest-thread-reference-session")
	threadID := modulecore.NewThreadID()
	if err := store.SaveMessage(ctx, sessionID, threadID, 7, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), domconv.NewMessage(domconv.SpeakerUser, "canonical row", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	for i := 0; i < 13; i++ {
		createdAt := time.Now().UTC().Add(time.Duration(i+1) * time.Second)
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, '', 0, '', ?, ?, '{}', ?, ?, ?, ?, ?)`,
			fmt.Sprintf("unthreaded-%02d", i), "other", sessionID, string(domconv.SpeakerSystem), "unthreaded row", MemoryStateObserved, MemoryLayerL1, "test", createdAt, createdAt); err != nil {
			t.Fatalf("insert unthreaded row %d: %v", i, err)
		}
	}
	lowerThreadID := modulecore.NewThreadID()
	if err := store.SaveMessage(ctx, sessionID, lowerThreadID, 3, modulecore.ThreadKindUserConversation, "conv:"+string(lowerThreadID), domconv.NewMessage(domconv.SpeakerUser, "later lower sequence", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage lower sequence: %v", err)
	}
	gotID, gotSeq, gotKind, found, err := store.LatestConversationThreadReference(ctx, sessionID)
	if err != nil {
		t.Fatalf("LatestConversationThreadReference: %v", err)
	}
	if !found || gotID != threadID || gotSeq != 7 || gotKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("latest tuple=%q/%d/%q found=%t, want %q/7/%q", gotID, gotSeq, gotKind, found, threadID, modulecore.ThreadKindUserConversation)
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
	var threadID modulecore.ThreadID
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
		if event.Source != "conversation" || event.Layer != MemoryLayerL1 || event.MemoryState != MemoryStateObserved || event.SessionID != request.SessionID || event.ThreadID != threadID || event.ThreadSeq != 1 || event.ThreadKind != domconv.ThreadKindUserConversation {
			t.Fatalf("projection[%d] ownership=%+v", index, event)
		}
		if event.Meta["domain"] != request.Domain || event.Meta["turn_id"] != string(request.TurnID) || event.Meta["speaker"] != string(event.Speaker) {
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
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET namespace = ? WHERE id = ?`, "conv:"+string(threadID), projection[0].ID); err != nil {
		t.Fatalf("restore namespace: %v", err)
	}
	for i := 0; i < 11; i++ {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'user', ?, '{}', ?, ?, ?, ?, ?)`, fmt.Sprintf("extra-%d", i), "conv:"+string(threadID), request.SessionID, threadID, 1, domconv.ThreadKindUserConversation, "extra", MemoryStateObserved, MemoryLayerL1, "conversation", time.Now().UTC(), time.Now().UTC()); err != nil {
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
		claim, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
		if err != nil || claim == nil || claim.Attempts != attempt {
			t.Fatalf("attempt %d claim=%+v err=%v", attempt, claim, err)
		}
		result, err := store.FailConversationTurnOutbox(ctx, string(request.TurnID), claim.Target, claim.LeaseToken, domconv.ConversationTurnErrorUnavailable, now)
		if err != nil {
			t.Fatalf("attempt %d fail: %v", attempt, err)
		}
		assertConversationTurnIdentity(t, request, result)
		if attempt < domconv.ConversationTurnMaxOutboxAttempts && result.Status != domconv.ConversationTurnPartial {
			t.Fatalf("attempt %d result=%+v, want partial", attempt, result)
		}
		if attempt == domconv.ConversationTurnMaxOutboxAttempts && (result.Status != domconv.ConversationTurnFailed || result.ErrorCode != domconv.ConversationTurnErrorUnavailable) {
			t.Fatalf("terminal result=%+v, want failed/unavailable", result)
		}
		now = now.Add(time.Minute)
	}
	claim, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("terminal claim=%+v err=%v, want no claim", claim, err)
	}
}

func TestConversationTurnProjectionRejectsOddAndMixedPairs(t *testing.T) {
	ctx := context.Background()
	newProjection := func(t *testing.T) (*L1SQLiteStore, domconv.ConversationTurnRequest, []L1MemoryEvent, modulecore.ThreadID) {
		t.Helper()
		store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-corrupt.db"))
		if err != nil {
			t.Fatalf("NewL1SQLiteStore: %v", err)
		}
		request := conversationTurnStoreTestRequest("turn-projection-corrupt", "session-projection-corrupt")
		if _, err := store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("commit: %v", err)
		}
		var threadID modulecore.ThreadID
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
	receipt, err := store.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil || receipt.Status != domconv.ConversationTurnFailed || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("exhausted stale receipt=%+v err=%v, want failed/unavailable", receipt, err)
	}
	assertConversationTurnIdentity(t, request, receipt)
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
	if claim, err := store.ClaimConversationTurnOutbox(ctx, string(requests[1].TurnID), now, time.Minute); err != nil || claim != nil {
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
		receipt, err := store.GetConversationTurnReceipt(ctx, string(requests[i].TurnID))
		if err != nil || receipt.Status != domconv.ConversationTurnFailed || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
			t.Fatalf("receipt %d=%+v err=%v, want terminal failed/unavailable", i, receipt, err)
		}
		assertConversationTurnIdentity(t, requests[i], receipt)
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

	request := conversationTurnStoreTestRequest("turn-multi-section-1", "session-multi-section-1")
	request.OwnerID = "owner-multi-section-1"
	request.UserMessage = "hello"
	request.AgentMessage = "hi"
	request.RecallTraceItems = []domconv.RecallTraceItem{
		{Layer: "L1", Kind: "short_context", Summary: "recent turn", Status: domconv.TraceStatusInjected, Decision: "included", PromptSection: "[RecallPack: ShortContext]", TokenCount: 10},
		{Layer: "L1", Kind: "rolling_summary", Summary: "summary", Status: domconv.TraceStatusInjected, Decision: "included", PromptSection: "[RecallPack: RollingSummary]", TokenCount: 20},
		{Layer: "L1", Kind: "user_memory", Summary: "", Status: "filtered_status", Decision: "rejected", Reason: "memory state is not confirmed or pinned", PromptSection: "[RecallPack: UserMemory]"},
	}
	result, err := store.CommitConversationTurn(context.Background(), request)
	if err != nil {
		t.Fatalf("CommitConversationTurn with multi-section trace: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed", result.Status)
	}
	assertConversationTurnIdentity(t, request, result)
	var events int
	if err := store.db.QueryRow(`SELECT count(*) FROM prompt_injection_event WHERE trace_id = ?`, request.TraceID).Scan(&events); err != nil {
		t.Fatalf("count injection events: %v", err)
	}
	if events != 2 {
		t.Fatalf("injection events=%d, want one per injected section", events)
	}
	var distinct int
	if err := store.db.QueryRow(`SELECT count(DISTINCT injection_id) FROM prompt_injection_event WHERE trace_id = ?`, request.TraceID).Scan(&distinct); err != nil {
		t.Fatalf("count distinct injection ids: %v", err)
	}
	if distinct != 2 {
		t.Fatalf("distinct injection ids=%d, want 2 non-colliding ids", distinct)
	}
}
