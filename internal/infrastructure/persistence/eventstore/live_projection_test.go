package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestConversationProjectionRequiresOwnerAndFirstDeliveryAttemptThenCompletesAfterL1(t *testing.T) {
	ctx := context.Background()
	eventStorePath := filepath.Join(t.TempDir(), "events.sqlite")
	store, err := NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer func() { _ = store.Close() }()
	accepted := acceptLiveTurn(t, store, modulecore.NewSessionID(), "projection-turn", true, 1)
	private := eventFixture(accepted.InputEvent.TraceID, "conversation.agent_report", "delegation_mailbox")
	private.SessionID, private.TurnID, private.MessageID = accepted.SessionID, modulecore.NewTurnID(), modulecore.NewMessageID()
	private.ActorKind, private.ActorID = "agent", "shiro"
	private.Payload = map[string]any{"from": "shiro", "source_actor_id": "shiro", "to": "mio", "visibility": "private", "content": "PRIVATE REPORT BODY", "text": "PRIVATE REPORT BODY"}
	private, err = store.AppendSequenced(ctx, private)
	if err != nil {
		t.Fatalf("AppendSequenced(private report): %v", err)
	}
	if err := store.insertTestPrivateMessageIndex(ctx, private, "shiro"); err != nil {
		t.Fatalf("insert private report index: %v", err)
	}

	output := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
	output.CausationEventID = accepted.InputEvent.EventID
	output.SessionID, output.TurnID, output.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
	output.Payload = map[string]any{"kind": "reply", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user", "content": "public answer", "text": "public answer"}
	workID := "projection:" + string(accepted.TurnID)
	commit, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-projection-turn", SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		GenerationEpoch: 1, ResultState: "completed", Events: []modulecore.EventEnvelope{output},
		MessageIndexes: []MessageIndex{{
			MessageID: output.MessageID, EventID: output.EventID, SessionID: output.SessionID, TurnID: output.TurnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message",
			ReplyToMessageID: accepted.InputMessageID, Visibility: "public",
		}},
		Outbox: []WorkItem{{
			WorkID: workID, DedupeKey: "conversation_projection:" + string(accepted.TurnID), Kind: conversationProjectionWorkKind,
			SourceEventID: output.EventID, SessionID: accepted.SessionID, Priority: 10,
		}},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	output = commit.Events[0]

	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	if claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: now}); err != nil || len(claims) != 0 {
		t.Fatalf("ClaimWork(before delivery marker) = %#v, err=%v; want no claims", claims, err)
	}
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: workID, SourceEventID: output.EventID, OwnerID: "user-2", AttemptedAt: now,
	}); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(wrong owner) = %v, want owner conflict", err)
	}
	mark := ConversationProjectionDeliveryAttemptInput{WorkID: workID, SourceEventID: output.EventID, OwnerID: "user-1", AttemptedAt: now}
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, mark); err != nil {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(): %v", err)
	}
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, mark); err != nil {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(retry): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(after marker) = %#v, err=%v; want one claim", claims, err)
	}
	lease := WorkLeaseInput{WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version, Now: now.Add(time.Second)}
	if err := store.AckWork(ctx, lease); !errors.Is(err, ErrWorkResultNotPersisted) {
		t.Fatalf("AckWork(projection bypass) = %v, want result-not-persisted", err)
	}

	loadInput := LoadConversationProjectionInput{Lease: lease, OwnerID: "user-1"}
	if _, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version + 1, Now: lease.Now,
	}, OwnerID: "user-1"}); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadLeasedConversationProjection(stale lease) = %v, want lease conflict", err)
	}
	if _, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: lease, OwnerID: "user-2"}); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("LoadLeasedConversationProjection(wrong owner) = %v, want owner conflict", err)
	}
	projection, err := store.LoadLeasedConversationProjection(ctx, loadInput)
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(): %v", err)
	}
	if len(projection.Items) != 2 || projection.Items[0].Speaker != "user" || projection.Items[1].Speaker != "mio" || projection.AppliedThrough != output.EventSeq {
		t.Fatalf("projection = %#v, want user/response pair through public output", projection)
	}
	for _, item := range projection.Items {
		if strings.Contains(item.Text, "PRIVATE REPORT BODY") || item.Visibility != "public" {
			t.Fatalf("private content leaked into projection: %#v", item)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(EventStore before retry): %v", err)
	}
	store, err = NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	retryProjection, err := store.LoadLeasedConversationProjection(ctx, loadInput)
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(retry): %v", err)
	}
	if retryProjection.Items[0].ThreadID != projection.Items[0].ThreadID || retryProjection.Items[1].ContentSHA256 != projection.Items[1].ContentSHA256 {
		t.Fatalf("projection retry changed canonical assignment: first %#v retry %#v", projection.Items, retryProjection.Items)
	}

	badReceipt := domconv.ConversationProjectionApplyReceipt{SessionID: projection.SessionID, AppliedThrough: projection.AppliedThrough}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: lease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark, Receipt: badReceipt,
	}); !errors.Is(err, ErrConversationProjectionConflict) {
		t.Fatalf("CompleteConversationProjection(before L1 apply) = %v, want receipt conflict", err)
	}
	stateBefore, found, err := store.ReadSessionState(ctx, accepted.SessionID, "user-1")
	if err != nil || !found || stateBefore.ProjectionWatermark != projection.ExpectedWatermark {
		t.Fatalf("watermark after rejected completion = %#v found=%v err=%v", stateBefore, found, err)
	}

	l1Store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer l1Store.Close()
	if _, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items); err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(): %v", err)
	}
	receipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(after simulated worker restart): %v", err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: lease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark + 1, Receipt: receipt,
	}); !errors.Is(err, ErrProjectionWatermarkConflict) {
		t.Fatalf("CompleteConversationProjection(stale watermark after L1 apply) = %v, want watermark conflict", err)
	}
	stateAfterFailure, found, err := store.ReadSessionState(ctx, accepted.SessionID, "user-1")
	if err != nil || !found || stateAfterFailure.ProjectionWatermark != projection.ExpectedWatermark {
		t.Fatalf("watermark after failed ack transaction = %#v found=%v err=%v", stateAfterFailure, found, err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: lease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark, Receipt: receipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(retry after L1 apply): %v", err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: lease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark, Receipt: receipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(lost ack response replay): %v", err)
	}
	stateAfter, found, err := store.ReadSessionState(ctx, accepted.SessionID, "user-1")
	if err != nil || !found || stateAfter.ProjectionWatermark != output.EventSeq {
		t.Fatalf("final projection watermark = %#v found=%v err=%v, want %d", stateAfter, found, err, output.EventSeq)
	}
}

func TestConversationProjectionClosesOddThreadBeforeNextPairInSQLite(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	accepted := acceptLiveTurn(t, store, modulecore.NewSessionID(), "odd-boundary", true, 1)
	oldThreadID := modulecore.NewThreadID()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO live_conversation_projection_thread(session_id,thread_id,thread_seq,message_count,status)
		VALUES(?,?,1,11,'active')`, string(accepted.SessionID), string(oldThreadID)); err != nil {
		t.Fatalf("seed 11-message active thread: %v", err)
	}
	output := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
	output.CausationEventID = accepted.InputEvent.EventID
	output.SessionID, output.TurnID, output.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
	output.Payload = map[string]any{"kind": "reply", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user", "content": "answer", "text": "answer"}
	workID := "projection:" + string(accepted.TurnID)
	committed, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-odd-boundary", SessionID: accepted.SessionID, TurnID: accepted.TurnID, GenerationEpoch: 1,
		ResultState: "completed", Events: []modulecore.EventEnvelope{output},
		MessageIndexes: []MessageIndex{{MessageID: output.MessageID, EventID: output.EventID, SessionID: output.SessionID, TurnID: output.TurnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", ReplyToMessageID: accepted.InputMessageID, Visibility: "public"}},
		Outbox: []WorkItem{{WorkID: workID, DedupeKey: "conversation_projection:" + string(accepted.TurnID), Kind: conversationProjectionWorkKind,
			SourceEventID: output.EventID, SessionID: accepted.SessionID, Priority: 10}},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	output = committed.Events[0]
	now := time.Date(2026, 10, 22, 4, 0, 0, 0, time.UTC)
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{WorkID: workID, SourceEventID: output.EventID, OwnerID: "user-1", AttemptedAt: now}); err != nil {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork() = %#v err=%v", claims, err)
	}
	projection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{
		Lease: WorkLeaseInput{WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version, Now: now.Add(time.Second)}, OwnerID: "user-1",
	})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(): %v", err)
	}
	if len(projection.Items) != 2 || projection.Items[0].ThreadID == oldThreadID || projection.Items[1].ThreadID != projection.Items[0].ThreadID || projection.Items[1].ThreadSeq != 2 {
		t.Fatalf("pair assignment = %#v, want new thread after odd close", projection.Items)
	}
	var oldStatus string
	var oldCount int
	if err := store.db.QueryRowContext(ctx, `SELECT status,message_count FROM live_conversation_projection_thread WHERE thread_id=?`, string(oldThreadID)).Scan(&oldStatus, &oldCount); err != nil {
		t.Fatalf("read closed thread: %v", err)
	}
	if oldStatus != "closed" || oldCount != 11 {
		t.Fatalf("old thread status/count = %s/%d, want closed/11", oldStatus, oldCount)
	}
	var newStatus string
	var newCount, newSequence int
	if err := store.db.QueryRowContext(ctx, `SELECT status,message_count,thread_seq FROM live_conversation_projection_thread WHERE thread_id=?`, string(projection.Items[0].ThreadID)).Scan(&newStatus, &newCount, &newSequence); err != nil {
		t.Fatalf("read new thread: %v", err)
	}
	if newStatus != "active" || newCount != 2 || newSequence != 2 {
		t.Fatalf("new thread status/count/sequence = %s/%d/%d, want active/2/2", newStatus, newCount, newSequence)
	}
}

func TestPublicReadbackOutputQueuesProjectionAndPrivateQuestionDoesNot(t *testing.T) {
	for _, test := range []struct {
		name   string
		status domconv.ReadbackStatus
		want   int
	}{
		{name: "accepted public readback", status: domconv.ReadbackStatusAccepted, want: 1},
		{name: "private clarification question", status: domconv.ReadbackStatusNeedsClarification, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := newReadbackFixture(t, store, test.status)
			acceptance, err := store.AcceptReadback(context.Background(), fixture.input)
			if err != nil {
				t.Fatalf("AcceptReadback(): %v", err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind=? AND source_event_id=?`,
				conversationProjectionWorkKind, string(acceptance.Event.EventID)).Scan(&count); err != nil {
				t.Fatalf("count projection work: %v", err)
			}
			if count != test.want {
				t.Fatalf("projection work for %s = %d, want %d", test.name, count, test.want)
			}
		})
	}
}

func TestAcceptTurnQueuesClarificationReplyProjectionAtomically(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	input := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"projection-reply-accept", "projection-reply-submission", "Use the staging region.")
	input.ActorID = "viewer-user"
	input.InputEvent.Payload["source_actor_id"] = input.ActorID
	input.InputMessage.FromActor = input.ActorID
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(clarification reply): %v", err)
	}
	workID, dedupeKey, kind, sourceEventID, sessionID, state := "", "", "", "", "", ""
	if err := store.db.QueryRowContext(ctx, `SELECT work_id,dedupe_key,kind,source_event_id,session_id,state FROM live_work_outbox WHERE source_event_id=?`,
		string(accepted.InputEvent.EventID)).Scan(&workID, &dedupeKey, &kind, &sourceEventID, &sessionID, &state); err != nil {
		t.Fatalf("load accepted answer projection work: %v", err)
	}
	if workID != ConversationProjectionWorkID(accepted.InputEvent.EventID) || dedupeKey != conversationProjectionDedupeKey(accepted.InputEvent.EventID) ||
		kind != conversationProjectionWorkKind || sourceEventID != string(accepted.InputEvent.EventID) || sessionID != string(accepted.SessionID) || state != "pending" {
		t.Fatalf("accepted answer projection work = %q/%q/%q/%q/%q/%q", workID, dedupeKey, kind, sourceEventID, sessionID, state)
	}
	var receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_clarification_reply_receipt WHERE reply_turn_id=? AND reply_message_id=?`,
		string(accepted.TurnID), string(accepted.InputMessageID)).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatalf("clarification reply receipt count = %d, err=%v; want 1 with queued projection", receiptCount, err)
	}
	if claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute}); err != nil || len(claims) != 0 {
		t.Fatalf("ClaimWork(before public delivery attempt) = %#v, err=%v; want no claims", claims, err)
	}
	pending, err := store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 4)
	if err != nil || len(pending) != 1 || pending[0].WorkID != workID || pending[0].SourceEventID != accepted.InputEvent.EventID ||
		pending[0].SessionID != accepted.SessionID || pending[0].Event.EventID != accepted.InputEvent.EventID || pending[0].Event.EventSeq != accepted.InputEvent.EventSeq {
		t.Fatalf("ListUnmarkedClarificationReplyProjections() = %#v, err=%v; want the owner-validated accepted answer", pending, err)
	}
}

func TestClarificationReplyProjectionDeliveryRestartAndOrdering(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clarification-projection.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture, prompt := createClarificationPrompt(t, store)
	input := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"projection-lifecycle-accept", "projection-lifecycle-submission", "Use the staging region.")
	input.ActorID = "viewer-user"
	input.InputEvent.Payload["source_actor_id"] = input.ActorID
	input.InputMessage.FromActor = input.ActorID
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(clarification reply): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(EventStore before failed delivery retry): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen EventStore after failed delivery: %v", err)
	}
	defer store.Close()
	pending, err := store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 1)
	if err != nil || len(pending) != 1 || pending[0].Event.EventID != accepted.InputEvent.EventID || pending[0].Event.Payload["content"] != "Use the staging region." {
		t.Fatalf("pending answer after failed delivery/restart = %#v, err=%v", pending, err)
	}
	if claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute}); err != nil || len(claims) != 0 {
		t.Fatalf("ClaimWork(after failed delivery/restart) = %#v, err=%v; want no claims", claims, err)
	}

	// The prior history is already materialized in this scenario. The answer
	// then reaches the exact 12-message thread boundary as its own user source.
	if _, err := store.db.ExecContext(ctx, `UPDATE live_conversation_session SET projection_watermark=? WHERE session_id=?`,
		int64(accepted.InputEvent.EventSeq-1), string(accepted.SessionID)); err != nil {
		t.Fatalf("seed prior projection watermark: %v", err)
	}
	oldThreadID := modulecore.NewThreadID()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO live_conversation_projection_thread(session_id,thread_id,thread_seq,message_count,status)
		VALUES(?,?,1,11,'active')`, string(accepted.SessionID), string(oldThreadID)); err != nil {
		t.Fatalf("seed 11-message active thread: %v", err)
	}

	continuationInput := clarificationReplyContinuationInput(fixture, accepted.TurnID, prompt.Message.MessageID,
		"projection-lifecycle-continue", 1, "Use the staging region and keep the requested scope.")
	if _, err := store.CommitClarificationReplyContinuation(ctx, continuationInput); err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(): %v", err)
	}
	readback, err := store.AcceptReadback(ctx, readbackForRevision(fixture, 2, domconv.ReadbackStatusAccepted, "projection-lifecycle-readback"))
	if err != nil {
		t.Fatalf("AcceptReadback(after clarification continuation): %v", err)
	}
	if readback.Event.TurnID != fixture.turnID {
		t.Fatalf("later readback TurnID = %s, want original Turn %s", readback.Event.TurnID, fixture.turnID)
	}
	readbackWorkID := ConversationProjectionWorkID(readback.Event.EventID)
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: readbackWorkID, SourceEventID: readback.Event.EventID, OwnerID: "user-1",
	}); err != nil {
		t.Fatalf("mark later readback delivered: %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute,
		Now: time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)})
	if err != nil || len(claims) != 1 || claims[0].SourceEventID != readback.Event.EventID {
		t.Fatalf("ClaimWork(later readback before answer delivery) = %#v, err=%v", claims, err)
	}
	readbackLease := WorkLeaseInput{WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version,
		Now: time.Date(2026, 10, 22, 6, 0, 1, 0, time.UTC)}
	if _, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: readbackLease, OwnerID: "user-1"}); !errors.Is(err, ErrConversationProjectionBlocked) {
		t.Fatalf("LoadLeasedConversationProjection(later readback before answer apply) = %v, want ordering block", err)
	}

	answer := pending[0]
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: answer.WorkID, SourceEventID: answer.SourceEventID, OwnerID: "user-1",
	}); err != nil {
		t.Fatalf("mark accepted answer delivered: %v", err)
	}
	claims, err = store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute,
		Now: time.Date(2026, 10, 22, 6, 0, 2, 0, time.UTC)})
	if err != nil || len(claims) != 1 || claims[0].SourceEventID != answer.SourceEventID {
		t.Fatalf("ClaimWork(answer after marker) = %#v, err=%v", claims, err)
	}
	answerLease := WorkLeaseInput{WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version,
		Now: time.Date(2026, 10, 22, 6, 0, 3, 0, time.UTC)}
	projection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: answerLease, OwnerID: "user-1"})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(answer): %v", err)
	}
	if len(projection.Items) != 1 || projection.Items[0].Speaker != "user" || projection.Items[0].ActorID != "viewer-user" ||
		projection.Items[0].TurnID != accepted.TurnID || projection.Items[0].ThreadID != oldThreadID || projection.Items[0].ThreadSeq != 12 {
		t.Fatalf("answer singleton assignment = %#v; want owner user-1, saved actor viewer-user, and sequence 12", projection.Items)
	}
	l1Path := filepath.Join(t.TempDir(), "clarification-projection-l1.sqlite")
	l1Store, err := l1sqlite.NewL1SQLiteStore(l1Path)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	applyReceipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(answer): %v", err)
	}
	if err := l1Store.Close(); err != nil {
		t.Fatalf("Close(L1 after answer apply): %v", err)
	}
	l1Store, err = l1sqlite.NewL1SQLiteStore(l1Path)
	if err != nil {
		t.Fatalf("reopen L1 after answer apply: %v", err)
	}
	defer l1Store.Close()
	retryReceipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items)
	if err != nil || retryReceipt.AppliedThrough != applyReceipt.AppliedThrough {
		t.Fatalf("ApplyCanonicalConversationProjection(answer after restart) = %#v, %v", retryReceipt, err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: answerLease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark, Receipt: retryReceipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(answer): %v", err)
	}
	readbackProjection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: readbackLease, OwnerID: "user-1"})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(later readback after answer apply): %v", err)
	}
	if len(readbackProjection.Items) != 1 || readbackProjection.Items[0].TurnID != fixture.turnID ||
		readbackProjection.Items[0].EventID != readback.Event.EventID || readbackProjection.Items[0].ThreadID == oldThreadID {
		t.Fatalf("later readback projection = %#v; want its original Turn on the next thread", readbackProjection.Items)
	}
	readbackReceipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, readbackProjection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(later readback): %v", err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: readbackLease, OwnerID: "user-1", ExpectedWatermark: readbackProjection.ExpectedWatermark, Receipt: readbackReceipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(later readback): %v", err)
	}
}

func TestAcceptTurnClarificationProjectionOutboxFailureRollsBackReplyReceipt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	input := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"projection-reply-atomic-accept", "projection-reply-atomic-submission", "Use the staging region.")
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_clarification_projection_insert BEFORE INSERT ON live_work_outbox
		WHEN NEW.kind='conversation_projection'
		BEGIN SELECT RAISE(ABORT, 'projection outbox unavailable'); END`); err != nil {
		t.Fatalf("create outbox failure trigger: %v", err)
	}
	if _, err := store.AcceptTurn(ctx, input); err == nil {
		t.Fatal("AcceptTurn() succeeded despite projection outbox failure; expected the reply acceptance transaction to roll back")
	}
	for _, check := range []struct {
		query string
		args  []any
		name  string
	}{
		{`SELECT COUNT(*) FROM live_clarification_reply_receipt WHERE reply_turn_id=?`, []any{string(input.InputEvent.TurnID)}, "reply receipt"},
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE turn_id=?`, []any{string(input.InputEvent.TurnID)}, "reply turn"},
		{`SELECT COUNT(*) FROM live_message_index WHERE message_id=?`, []any{string(input.InputEvent.MessageID)}, "reply message index"},
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id=?`, []any{string(input.InputEvent.EventID)}, "answer event"},
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after projection outbox failure = %d, err=%v; want no foreground acceptance rows", check.name, count, err)
		}
	}
}

func TestConversationProjectionRejectsForgedNonReplyInputSource(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	accepted := acceptLiveTurn(t, store, modulecore.NewSessionID(), "forged-answer-projection", true, 1)
	workID := ConversationProjectionWorkID(accepted.InputEvent.EventID)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO live_work_outbox
		(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES(?,?,?,?,?,10,'pending',0,'',1)`, workID, conversationProjectionDedupeKey(accepted.InputEvent.EventID),
		conversationProjectionWorkKind, string(accepted.InputEvent.EventID), string(accepted.SessionID)); err != nil {
		t.Fatalf("insert forged projection work for accepted nonreply input: %v", err)
	}
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: workID, SourceEventID: accepted.InputEvent.EventID, OwnerID: accepted.ActorID,
	}); !errors.Is(err, ErrConversationProjectionConflict) {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(nonreply input) = %v, want projection conflict", err)
	}
	if pending, err := store.ListUnmarkedClarificationReplyProjections(ctx, accepted.ActorID, 4); err != nil || len(pending) != 0 {
		t.Fatalf("ListUnmarkedClarificationReplyProjections(nonreply input) = %#v, %v; want none", pending, err)
	}
}

func TestClarificationReplyDeliveryRetryBackoffKeepsClaimGateClosed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	accepted, err := store.AcceptTurn(ctx, clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"projection-retry-accept", "projection-retry-submission", "Use the staging region."))
	if err != nil {
		t.Fatalf("AcceptTurn(clarification reply): %v", err)
	}
	workID := ConversationProjectionWorkID(accepted.InputEvent.EventID)
	now := time.Now().UTC()
	if err := store.RetryUnmarkedClarificationReplyProjection(ctx, "user-1", workID, accepted.InputEvent.EventID, now, time.Minute); err != nil {
		t.Fatalf("RetryUnmarkedClarificationReplyProjection(first delivery failure): %v", err)
	}
	var state, notBefore string
	var attempt int
	if err := store.db.QueryRowContext(ctx, `SELECT state,attempt,not_before FROM live_work_outbox WHERE work_id=?`, workID).Scan(&state, &attempt, &notBefore); err != nil {
		t.Fatalf("read retry state: %v", err)
	}
	if state != "pending" || attempt != 1 || notBefore == "" {
		t.Fatalf("after failed public delivery state/attempt/not_before = %s/%d/%q; want pending/1/delayed", state, attempt, notBefore)
	}
	if pending, err := store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 1); err != nil || len(pending) != 0 {
		t.Fatalf("ListUnmarkedClarificationReplyProjections(during backoff) = %#v, %v; want none", pending, err)
	}
	if claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute}); err != nil || len(claims) != 0 {
		t.Fatalf("ClaimWork(during pre-marker backoff) = %#v, %v; want no claims", claims, err)
	}
	for expected := 2; expected <= maxWorkAttempts; expected++ {
		if _, err := store.db.ExecContext(ctx, `UPDATE live_work_outbox SET not_before='' WHERE work_id=? AND state='pending'`, workID); err != nil {
			t.Fatalf("make retry attempt %d due: %v", expected, err)
		}
		err := store.RetryUnmarkedClarificationReplyProjection(ctx, "user-1", workID, accepted.InputEvent.EventID, time.Now().UTC(), time.Second)
		if expected < maxWorkAttempts && err != nil {
			t.Fatalf("RetryUnmarkedClarificationReplyProjection(attempt %d): %v", expected, err)
		}
		if expected == maxWorkAttempts && !errors.Is(err, ErrConversationProjectionTerminalAttention) {
			t.Fatalf("RetryUnmarkedClarificationReplyProjection(exhausted) = %v, want terminal attention", err)
		}
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state,attempt FROM live_work_outbox WHERE work_id=?`, workID).Scan(&state, &attempt); err != nil {
		t.Fatalf("read terminal retry state: %v", err)
	}
	if state != "needs_attention" || attempt != maxWorkAttempts {
		t.Fatalf("exhausted delivery state/attempt = %s/%d, want needs_attention/%d", state, attempt, maxWorkAttempts)
	}
	var markerCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_conversation_projection_delivery WHERE work_id=?`, workID).Scan(&markerCount); err != nil || markerCount != 0 {
		t.Fatalf("delivery marker after failures = %d, err=%v; want absent", markerCount, err)
	}
}

func TestRepeatedClarificationReplyProjectionPreservesInterleavedEventOrder(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	fixture, prompt1 := createClarificationPrompt(t, store)
	reply1Input := clarificationReplyAcceptInput(fixture, prompt1.Message.MessageID,
		"projection-repeat-reply1", "projection-repeat-submission1", "Use the staging region.")
	reply1, err := store.AcceptTurn(ctx, reply1Input)
	if err != nil {
		t.Fatalf("AcceptTurn(first clarification reply): %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE live_conversation_session SET projection_watermark=? WHERE session_id=?`,
		int64(reply1.InputEvent.EventSeq-1), string(reply1.SessionID)); err != nil {
		t.Fatalf("seed pre-reply projection watermark: %v", err)
	}
	if _, err := store.CommitClarificationReplyContinuation(ctx, clarificationReplyContinuationInput(fixture, reply1.TurnID,
		prompt1.Message.MessageID, "projection-repeat-continue1", 1, "Use staging and keep the requested scope.")); err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(first reply): %v", err)
	}
	readback2, err := store.AcceptReadback(ctx, readbackForRevision(fixture, 2, domconv.ReadbackStatusNeedsClarification, "projection-repeat-readback2"))
	if err != nil || readback2.ControlTurn == nil {
		t.Fatalf("AcceptReadback(second needs-clarification revision) = %#v, %v", readback2, err)
	}
	decision2 := clarificationDecisionInputForRevision(fixture, readback2.ControlTurn.TurnID, 2, domconv.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: domconv.ClarificationDecisionAskUser,
		Question: "Which source should Shiro prioritize?",
	})
	prompt2, err := store.CommitClarificationDecision(ctx, decision2)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(second prompt): %v", err)
	}
	reply2Input := clarificationReplyAcceptInput(fixture, prompt2.Message.MessageID,
		"projection-repeat-reply2", "projection-repeat-submission2", "Prioritize the canonical report.")
	reply2, err := store.AcceptTurn(ctx, reply2Input)
	if err != nil {
		t.Fatalf("AcceptTurn(second clarification reply): %v", err)
	}
	if !(reply1.InputEvent.EventSeq < prompt2.Event.EventSeq && prompt2.Event.EventSeq < reply2.InputEvent.EventSeq) {
		t.Fatalf("interleaved event sequence = reply1 %d, prompt2 %d, reply2 %d", reply1.InputEvent.EventSeq, prompt2.Event.EventSeq, reply2.InputEvent.EventSeq)
	}
	pending, err := store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 2)
	if err != nil || len(pending) != 2 || pending[0].SourceEventID != reply1.InputEvent.EventID || pending[1].SourceEventID != reply2.InputEvent.EventID {
		t.Fatalf("unmarked reply projection order = %#v, %v; want reply1 then reply2", pending, err)
	}
	if err := store.RetryUnmarkedClarificationReplyProjection(ctx, "user-1", pending[0].WorkID, pending[0].SourceEventID, time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("schedule first answer after simulated failed delivery: %v", err)
	}
	duePending, err := store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 1)
	if err != nil || len(duePending) != 1 || duePending[0].SourceEventID != reply2.InputEvent.EventID {
		t.Fatalf("bounded due reply list after first delivery failure = %#v, %v; want later answer while first backs off", duePending, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE live_work_outbox SET not_before='' WHERE work_id=?`, pending[0].WorkID); err != nil {
		t.Fatalf("make first answer eligible for successful retry: %v", err)
	}
	pending, err = store.ListUnmarkedClarificationReplyProjections(ctx, "user-1", 2)
	if err != nil || len(pending) != 2 || pending[0].SourceEventID != reply1.InputEvent.EventID || pending[1].SourceEventID != reply2.InputEvent.EventID {
		t.Fatalf("unmarked reply list after backoff = %#v, %v; want event-sequence order", pending, err)
	}
	for _, item := range pending {
		if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
			WorkID: item.WorkID, SourceEventID: item.SourceEventID, OwnerID: "user-1",
		}); err != nil {
			t.Fatalf("mark clarification reply %s delivered: %v", item.SourceEventID, err)
		}
	}
	promptWorkID := ConversationProjectionWorkID(prompt2.Event.EventID)
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: promptWorkID, SourceEventID: prompt2.Event.EventID, OwnerID: "user-1",
	}); err != nil {
		t.Fatalf("mark interleaved second prompt delivered: %v", err)
	}
	now := time.Date(2026, 10, 22, 7, 0, 0, 0, time.UTC)
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 3, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 3 {
		t.Fatalf("ClaimWork(interleaved reply/prompt/reply) = %#v, %v; want all three marked sources", claims, err)
	}
	leases := make(map[modulecore.EventID]WorkLeaseInput, len(claims))
	for _, claim := range claims {
		leases[claim.SourceEventID] = WorkLeaseInput{WorkID: claim.WorkID, LeaseToken: claim.LeaseToken, Version: claim.Version, Now: now.Add(time.Second)}
	}
	l1Store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "interleaved-clarification-l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer l1Store.Close()
	ordered := []modulecore.EventEnvelope{reply1.InputEvent, prompt2.Event, reply2.InputEvent}
	for _, event := range ordered {
		lease, found := leases[event.EventID]
		if !found {
			t.Fatalf("ClaimWork omitted ordered source event %s", event.EventID)
		}
		projection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: lease, OwnerID: "user-1"})
		if err != nil {
			t.Fatalf("LoadLeasedConversationProjection(%s): %v", event.EventID, err)
		}
		if len(projection.Items) != 1 || projection.Items[0].EventID != event.EventID {
			t.Fatalf("interleaved projection unit for %s = %#v, want one matching singleton", event.EventID, projection.Items)
		}
		receipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items)
		if err != nil {
			t.Fatalf("ApplyCanonicalConversationProjection(%s): %v", event.EventID, err)
		}
		if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
			Lease: lease, OwnerID: "user-1", ExpectedWatermark: projection.ExpectedWatermark, Receipt: receipt,
		}); err != nil {
			t.Fatalf("CompleteConversationProjection(%s): %v", event.EventID, err)
		}
	}
	var watermark int64
	if err := store.db.QueryRowContext(ctx, `SELECT projection_watermark FROM live_conversation_session WHERE session_id=?`, string(reply1.SessionID)).Scan(&watermark); err != nil {
		t.Fatalf("read repeated clarification projection watermark: %v", err)
	}
	if watermark != int64(reply2.InputEvent.EventSeq) {
		t.Fatalf("repeated clarification projection watermark = %d, want second answer seq %d", watermark, reply2.InputEvent.EventSeq)
	}
}

func TestInterleavedRegularTurnsKeepPairsAndAdvanceContiguousWatermark(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	first := acceptLiveTurn(t, store, modulecore.NewSessionID(), "interleaved-first", true, 1)
	secondEvent := liveInputEvent(first.SessionID, modulecore.NewTurnID(), "interleaved second input")
	second, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-interleaved-second", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "interleaved-second", ActorID: "user-1", TriggerKind: "user_message", GenerationEpoch: 1,
		InputEvent: secondEvent,
		InputMessage: MessageIndex{MessageID: secondEvent.MessageID, EventID: secondEvent.EventID, SessionID: first.SessionID,
			TurnID: secondEvent.TurnID, FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(second interleaved input): %v", err)
	}
	commitOutput := func(accepted AcceptedTurn, operationID, content string) modulecore.EventEnvelope {
		t.Helper()
		output := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
		output.CausationEventID = accepted.InputEvent.EventID
		output.SessionID, output.TurnID, output.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
		output.Payload = map[string]any{"kind": "reply", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user", "content": content, "text": content}
		committed, err := store.CommitTurnBundle(ctx, CommitInput{
			OperationID: operationID, SessionID: accepted.SessionID, TurnID: accepted.TurnID, GenerationEpoch: 1,
			ResultState: "completed", Events: []modulecore.EventEnvelope{output},
			MessageIndexes: []MessageIndex{{MessageID: output.MessageID, EventID: output.EventID, SessionID: output.SessionID,
				TurnID: output.TurnID, FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message",
				ReplyToMessageID: accepted.InputMessageID, Visibility: "public"}},
			Outbox: []WorkItem{{WorkID: ConversationProjectionWorkID(output.EventID), DedupeKey: conversationProjectionDedupeKey(output.EventID),
				Kind: conversationProjectionWorkKind, SourceEventID: output.EventID, SessionID: accepted.SessionID, Priority: 10}},
		})
		if err != nil {
			t.Fatalf("CommitTurnBundle(%s): %v", operationID, err)
		}
		return committed.Events[0]
	}
	firstOutput := commitOutput(first, "commit-interleaved-first", "first response")
	secondOutput := commitOutput(second, "commit-interleaved-second", "second response")
	if !(first.InputEvent.EventSeq < second.InputEvent.EventSeq && second.InputEvent.EventSeq < firstOutput.EventSeq && firstOutput.EventSeq < secondOutput.EventSeq) {
		t.Fatalf("interleaved canonical sequence = A input %d, B input %d, A output %d, B output %d",
			first.InputEvent.EventSeq, second.InputEvent.EventSeq, firstOutput.EventSeq, secondOutput.EventSeq)
	}
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	for _, output := range []modulecore.EventEnvelope{firstOutput, secondOutput} {
		if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
			WorkID: ConversationProjectionWorkID(output.EventID), SourceEventID: output.EventID, OwnerID: "user-1", AttemptedAt: now,
		}); err != nil {
			t.Fatalf("mark interleaved output %s delivered: %v", output.EventID, err)
		}
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 2, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 2 {
		t.Fatalf("ClaimWork(interleaved output pairs) = %#v, %v; want both output work items", claims, err)
	}
	leases := make(map[modulecore.EventID]WorkLeaseInput, len(claims))
	for _, claim := range claims {
		leases[claim.SourceEventID] = WorkLeaseInput{WorkID: claim.WorkID, LeaseToken: claim.LeaseToken, Version: claim.Version, Now: now.Add(time.Second)}
	}
	secondLease := leases[secondOutput.EventID]
	if _, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: secondLease, OwnerID: "user-1"}); !errors.Is(err, ErrConversationProjectionBlocked) {
		t.Fatalf("LoadLeasedConversationProjection(B before A) = %v, want earlier output ordering block", err)
	}
	firstProjection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: leases[firstOutput.EventID], OwnerID: "user-1"})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(A with B input interleaved): %v", err)
	}
	if len(firstProjection.Items) != 2 || firstProjection.Items[0].MessageID != first.InputMessageID || firstProjection.Items[1].EventID != firstOutput.EventID {
		t.Fatalf("A projection unit = %#v; want original same-Turn pair", firstProjection.Items)
	}
	l1Store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "interleaved-turns-l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer l1Store.Close()
	firstReceipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, firstProjection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(A pair): %v", err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: leases[firstOutput.EventID], OwnerID: "user-1", ExpectedWatermark: firstProjection.ExpectedWatermark, Receipt: firstReceipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(A pair): %v", err)
	}
	state, found, err := store.ReadSessionState(ctx, first.SessionID, "user-1")
	if err != nil || !found || state.ProjectionWatermark != first.InputEvent.EventSeq {
		t.Fatalf("watermark after A pair across B input = %#v found=%v err=%v; want contiguous prefix through A input %d",
			state, found, err, first.InputEvent.EventSeq)
	}
	tail, err := store.ReadCompleteSessionTail(ctx, first.SessionID, state.ProjectionWatermark)
	if err != nil || !tail.Complete {
		t.Fatalf("ReadCompleteSessionTail(after A prefix) complete=%t err=%v", tail.Complete, err)
	}
	if len(tail.Messages) != 3 || tail.Messages[0].Index.MessageID != second.InputMessageID || tail.Messages[1].Index.EventID != firstOutput.EventID || tail.Messages[2].Index.EventID != secondOutput.EventID {
		t.Fatalf("canonical tail after contiguous watermark = %#v; want B input, A output, B output in event order", tail.Messages)
	}
	secondProjection, err := store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: secondLease, OwnerID: "user-1"})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(B after A apply): %v", err)
	}
	if len(secondProjection.Items) != 2 || secondProjection.Items[0].MessageID != second.InputMessageID || secondProjection.Items[1].EventID != secondOutput.EventID {
		t.Fatalf("B projection unit = %#v; want original same-Turn pair", secondProjection.Items)
	}
	secondReceipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, secondProjection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(B pair): %v", err)
	}
	if err := store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: secondLease, OwnerID: "user-1", ExpectedWatermark: secondProjection.ExpectedWatermark, Receipt: secondReceipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(B pair): %v", err)
	}
	state, found, err = store.ReadSessionState(ctx, first.SessionID, "user-1")
	if err != nil || !found || state.ProjectionWatermark != secondOutput.EventSeq {
		t.Fatalf("final interleaved projection watermark = %#v found=%v err=%v; want %d", state, found, err, secondOutput.EventSeq)
	}
}

func TestConversationProjectionUsesTrustedTurnActorWhenActorDiffersFromOwner(t *testing.T) {
	fixture := newLeasedConversationProjectionFixture(t, "ren", "viewer-user")
	projection, err := fixture.store.LoadLeasedConversationProjection(context.Background(), LoadConversationProjectionInput{
		Lease: fixture.lease, OwnerID: fixture.ownerID,
	})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(owner != actor): %v", err)
	}
	if len(projection.Items) != 2 {
		t.Fatalf("projection item count = %d, want user/response pair: %#v", len(projection.Items), projection.Items)
	}
	if got := projection.Items[0]; got.Speaker != "user" || got.ActorID != "viewer-user" {
		t.Fatalf("input projection identity = speaker %q actor %q, want user/viewer-user", got.Speaker, got.ActorID)
	}
}

func TestConversationProjectionRejectsInputFromActorOtherThanTrustedTurnActor(t *testing.T) {
	fixture := newLeasedConversationProjectionFixture(t, "forged-actor", "forged-actor")
	if _, err := fixture.store.db.ExecContext(context.Background(), `
		UPDATE live_conversation_turn SET actor_id='viewer-user' WHERE turn_id=?`, string(fixture.accepted.TurnID)); err != nil {
		t.Fatalf("set trusted turn actor: %v", err)
	}
	_, err := fixture.store.LoadLeasedConversationProjection(context.Background(), LoadConversationProjectionInput{
		Lease: fixture.lease, OwnerID: fixture.ownerID,
	})
	if !errors.Is(err, ErrConversationProjectionConflict) {
		t.Fatalf("LoadLeasedConversationProjection(actor differs from trusted turn actor) = %v, want projection conflict", err)
	}
}

func TestConversationProjectionDeliveryAttemptCompletedReplayIsReadOnly(t *testing.T) {
	fixture := newLeasedConversationProjectionFixture(t, "user-1", "user-1")
	projection := completeConversationProjectionFixture(t, fixture)
	before := readConversationProjectionRetrySnapshot(t, fixture)

	retry := ConversationProjectionDeliveryAttemptInput{
		WorkID: fixture.workID, SourceEventID: fixture.output.EventID, OwnerID: fixture.ownerID,
		AttemptedAt: fixture.now.Add(time.Hour),
	}
	if err := fixture.store.MarkConversationProjectionDeliveryAttempt(context.Background(), retry); err != nil {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(completed exact replay): %v", err)
	}
	after := readConversationProjectionRetrySnapshot(t, fixture)
	if after != before {
		t.Fatalf("completed marker replay changed persisted state: before=%#v after=%#v", before, after)
	}
	if before.Watermark != int64(projection.AppliedThrough) {
		t.Fatalf("snapshot watermark = %d, want applied-through %d", before.Watermark, projection.AppliedThrough)
	}
}

func TestConversationProjectionDeliveryAttemptCompletedRequiresExactMarker(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture leasedConversationProjectionFixture)
	}{
		{
			name: "missing marker",
			mutate: func(t *testing.T, fixture leasedConversationProjectionFixture) {
				if _, err := fixture.store.db.ExecContext(context.Background(), `DELETE FROM live_conversation_projection_delivery WHERE event_id=?`, string(fixture.output.EventID)); err != nil {
					t.Fatalf("delete projection marker: %v", err)
				}
			},
		},
		{
			name: "event mismatch",
			mutate: func(t *testing.T, fixture leasedConversationProjectionFixture) {
				if _, err := fixture.store.db.ExecContext(context.Background(), `UPDATE live_conversation_projection_delivery SET event_id=? WHERE event_id=?`,
					string(fixture.accepted.InputEvent.EventID), string(fixture.output.EventID)); err != nil {
					t.Fatalf("change marker event: %v", err)
				}
			},
		},
		{
			name: "work mismatch",
			mutate: func(t *testing.T, fixture leasedConversationProjectionFixture) {
				alternateWorkID := fixture.workID + ":wrong"
				if _, err := fixture.store.db.ExecContext(context.Background(), `INSERT INTO live_work_outbox
					(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
					VALUES(?,?,?,?,?,10,'pending',0,'',1)`, alternateWorkID, alternateWorkID, conversationProjectionWorkKind,
					string(fixture.output.EventID), string(fixture.accepted.SessionID)); err != nil {
					t.Fatalf("insert alternate projection work: %v", err)
				}
				if _, err := fixture.store.db.ExecContext(context.Background(), `UPDATE live_conversation_projection_delivery SET work_id=? WHERE event_id=?`,
					alternateWorkID, string(fixture.output.EventID)); err != nil {
					t.Fatalf("change marker work: %v", err)
				}
			},
		},
		{
			name: "session mismatch",
			mutate: func(t *testing.T, fixture leasedConversationProjectionFixture) {
				alternateSessionID := modulecore.NewSessionID()
				if _, err := fixture.store.db.ExecContext(context.Background(), `INSERT INTO live_conversation_session
					(session_id,owner_id,runtime_version,cutover_seq,admission_seq,projection_watermark,epoch)
					VALUES(?,?,'v2',0,0,0,1)`, string(alternateSessionID), fixture.ownerID); err != nil {
					t.Fatalf("insert alternate session: %v", err)
				}
				if _, err := fixture.store.db.ExecContext(context.Background(), `UPDATE live_conversation_projection_delivery SET session_id=? WHERE event_id=?`,
					string(alternateSessionID), string(fixture.output.EventID)); err != nil {
					t.Fatalf("change marker session: %v", err)
				}
			},
		},
		{
			name: "owner mismatch",
			mutate: func(t *testing.T, fixture leasedConversationProjectionFixture) {
				if _, err := fixture.store.db.ExecContext(context.Background(), `UPDATE live_conversation_projection_delivery SET owner_id='other-owner' WHERE event_id=?`, string(fixture.output.EventID)); err != nil {
					t.Fatalf("change marker owner: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLeasedConversationProjectionFixture(t, "user-1", "user-1")
			_ = completeConversationProjectionFixture(t, fixture)
			test.mutate(t, fixture)
			before := readConversationProjectionRetrySnapshot(t, fixture)
			err := fixture.store.MarkConversationProjectionDeliveryAttempt(context.Background(), ConversationProjectionDeliveryAttemptInput{
				WorkID: fixture.workID, SourceEventID: fixture.output.EventID, OwnerID: fixture.ownerID, AttemptedAt: fixture.now.Add(time.Hour),
			})
			if !errors.Is(err, ErrConversationProjectionConflict) {
				t.Fatalf("MarkConversationProjectionDeliveryAttempt(completed %s) = %v, want projection conflict", test.name, err)
			}
			if after := readConversationProjectionRetrySnapshot(t, fixture); after != before {
				t.Fatalf("rejected completed marker replay changed persisted state: before=%#v after=%#v", before, after)
			}
		})
	}
}

type leasedConversationProjectionFixture struct {
	store    *SQLiteStore
	ownerID  string
	accepted AcceptedTurn
	output   modulecore.EventEnvelope
	workID   string
	now      time.Time
	lease    WorkLeaseInput
}

func newLeasedConversationProjectionFixture(t *testing.T, ownerID, actorID string) leasedConversationProjectionFixture {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	sessionID := modulecore.NewSessionID()
	inputEvent := liveInputEvent(sessionID, modulecore.NewTurnID(), "projection-actor-identity")
	inputEvent.Payload = map[string]any{"from": "user", "source_actor_id": actorID, "to": "mio", "content": "actor scoped input", "text": "actor scoped input"}
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-projection-actor-identity", OwnerID: ownerID, RuntimeVersion: "v2",
		ClientSubmissionID: "projection-actor-identity", ActorID: actorID, TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: inputEvent.TurnID,
			FromActor: actorID, ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(owner=%q actor=%q): %v", ownerID, actorID, err)
	}
	output := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
	output.CausationEventID = accepted.InputEvent.EventID
	output.SessionID, output.TurnID, output.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
	output.Payload = map[string]any{"kind": "reply", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user", "content": "public answer", "text": "public answer"}
	workID := ConversationProjectionWorkID(output.EventID)
	committed, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-projection-actor-identity", SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		GenerationEpoch: 1, ResultState: "completed", Events: []modulecore.EventEnvelope{output},
		MessageIndexes: []MessageIndex{{
			MessageID: output.MessageID, EventID: output.EventID, SessionID: output.SessionID, TurnID: output.TurnID,
			FromActor: "mio", ToKind: "user", ToActor: ownerID, Kind: "assistant_message",
			ReplyToMessageID: accepted.InputMessageID, Visibility: "public",
		}},
		Outbox: []WorkItem{{
			WorkID: workID, DedupeKey: conversationProjectionDedupeKey(output.EventID), Kind: conversationProjectionWorkKind,
			SourceEventID: output.EventID, SessionID: accepted.SessionID, Priority: 10,
		}},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	output = committed.Events[0]
	now := time.Date(2026, 10, 22, 5, 0, 0, 0, time.UTC)
	if err := store.MarkConversationProjectionDeliveryAttempt(ctx, ConversationProjectionDeliveryAttemptInput{
		WorkID: workID, SourceEventID: output.EventID, OwnerID: ownerID, AttemptedAt: now,
	}); err != nil {
		t.Fatalf("MarkConversationProjectionDeliveryAttempt(): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: conversationProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork() = %#v, err=%v; want one projection claim", claims, err)
	}
	lease := WorkLeaseInput{WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version, Now: now.Add(time.Second)}
	return leasedConversationProjectionFixture{store: store, ownerID: ownerID, accepted: accepted, output: output, workID: workID, now: now, lease: lease}
}

func completeConversationProjectionFixture(t *testing.T, fixture leasedConversationProjectionFixture) LeasedConversationProjection {
	t.Helper()
	ctx := context.Background()
	projection, err := fixture.store.LoadLeasedConversationProjection(ctx, LoadConversationProjectionInput{Lease: fixture.lease, OwnerID: fixture.ownerID})
	if err != nil {
		t.Fatalf("LoadLeasedConversationProjection(): %v", err)
	}
	l1Store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-marker-l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer l1Store.Close()
	receipt, err := l1Store.ApplyCanonicalConversationProjection(ctx, projection.Items)
	if err != nil {
		t.Fatalf("ApplyCanonicalConversationProjection(): %v", err)
	}
	if err := fixture.store.CompleteConversationProjection(ctx, CompleteConversationProjectionInput{
		Lease: fixture.lease, OwnerID: fixture.ownerID, ExpectedWatermark: projection.ExpectedWatermark, Receipt: receipt,
	}); err != nil {
		t.Fatalf("CompleteConversationProjection(): %v", err)
	}
	return projection
}

type conversationProjectionRetrySnapshot struct {
	WorkState      string
	Attempt        int
	LeaseToken     string
	LeaseExpiresAt string
	Version        int
	Watermark      int64
	AttemptedAt    string
}

func readConversationProjectionRetrySnapshot(t *testing.T, fixture leasedConversationProjectionFixture) conversationProjectionRetrySnapshot {
	t.Helper()
	var snapshot conversationProjectionRetrySnapshot
	if err := fixture.store.db.QueryRowContext(context.Background(), `SELECT state,attempt,COALESCE(lease_token,''),COALESCE(lease_expires_at,''),version
		FROM live_work_outbox WHERE work_id=?`, fixture.workID).Scan(&snapshot.WorkState, &snapshot.Attempt, &snapshot.LeaseToken, &snapshot.LeaseExpiresAt, &snapshot.Version); err != nil {
		t.Fatalf("read projection work snapshot: %v", err)
	}
	if err := fixture.store.db.QueryRowContext(context.Background(), `SELECT projection_watermark FROM live_conversation_session WHERE session_id=?`, string(fixture.accepted.SessionID)).Scan(&snapshot.Watermark); err != nil {
		t.Fatalf("read projection watermark snapshot: %v", err)
	}
	if err := fixture.store.db.QueryRowContext(context.Background(), `SELECT attempted_at FROM live_conversation_projection_delivery WHERE event_id=?`, string(fixture.output.EventID)).Scan(&snapshot.AttemptedAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read projection marker snapshot: %v", err)
	}
	return snapshot
}

func (s *SQLiteStore) insertTestPrivateMessageIndex(ctx context.Context, event modulecore.EventEnvelope, fromActor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertMessageIndex(ctx, tx, MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: event.SessionID, TurnID: event.TurnID,
		FromActor: fromActor, ToKind: "agent", ToActor: "mio", Kind: "agent_report", Visibility: "private",
	}); err != nil {
		return err
	}
	return tx.Commit()
}
