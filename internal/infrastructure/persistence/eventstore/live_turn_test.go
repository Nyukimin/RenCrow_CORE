package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestFreshSchemaContainsLiveConversationJournalTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-journal.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	defer store.Close()

	for table, columns := range map[string][]string{
		"live_conversation_session": {"session_id", "owner_id", "runtime_version", "cutover_seq", "admission_seq", "projection_watermark", "epoch"},
		"live_conversation_turn":    {"turn_id", "session_id", "owner_id", "actor_id", "trigger_kind", "client_submission_id", "submission_sha256", "accept_receipt_id", "input_message_id", "admission_seq", "state", "generation_epoch", "source_turn_id", "delegation_id", "result_receipt_id"},
		"live_message_index":        {"message_id", "event_id", "session_id", "turn_id", "from_actor", "to_kind", "to_actor", "kind", "reply_to_message_id", "visibility"},
		"live_delegation":           {"delegation_id", "revision", "origin_turn_id", "assignee", "instruction_message_id", "context_event_id", "state", "work_task_id", "report_revision"},
		"live_work_outbox":          {"work_id", "dedupe_key", "kind", "source_event_id", "session_id", "priority", "state", "attempt", "not_before", "lease_token", "lease_expires_at", "version"},
		"live_operation_receipt":    {"operation_id", "operation_kind", "input_sha256", "result_json", "created_at"},
	} {
		assertLiveTableColumns(t, store.db, table, columns)
	}
}

func TestAcceptTurnIsIdempotentAndRejectsChangedSubmissionText(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	firstEvent := liveInputEvent(sessionID, turnID, "hello")
	first, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-1", "submission-1", firstEvent))
	if err != nil {
		t.Fatalf("AcceptTurn(first): %v", err)
	}
	if got := reflect.ValueOf(first).FieldByName("ClientSubmissionID").String(); got != "submission-1" {
		t.Fatalf("accepted receipt client submission id = %q, want stable submitted id", got)
	}

	// Runtime-generated event, message, turn, and trace IDs may differ on an
	// HTTP retry. The stable submission identity and input text still return
	// the first durable acceptance.
	retryEvent := liveInputEvent(sessionID, modulecore.NewTurnID(), "hello")
	retry, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-1", "submission-1", retryEvent))
	if err != nil {
		t.Fatalf("AcceptTurn(retry): %v", err)
	}
	assertJSONEqual(t, retry, first)

	// The trusted fresh-session assertion is only relevant when creating the
	// session. A retry after the session now exists must still match the first
	// admission receipt when the caller correctly changes the assertion to false.
	retryExistingSession, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFieldsWithNewSession("accept-op-1", "submission-1", firstEvent, false))
	if err != nil {
		t.Fatalf("AcceptTurn(retry after session creation): %v", err)
	}
	assertJSONEqual(t, retryExistingSession, first)

	changedEvent := liveInputEvent(sessionID, modulecore.NewTurnID(), "different text")
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-1", "submission-1", changedEvent)); err == nil || !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("AcceptTurn(same operation, changed text) error = %v, want conflict", err)
	}
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-2", "submission-1", changedEvent)); err == nil || !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("AcceptTurn(same submission, changed text) error = %v, want conflict", err)
	}

	var eventCount, turnCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE trace_id = ?`, string(firstEvent.TraceID)).Scan(&eventCount); err != nil {
		t.Fatalf("count accepted events: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_conversation_turn WHERE session_id = ? AND client_submission_id = ?`, string(sessionID), "submission-1").Scan(&turnCount); err != nil {
		t.Fatalf("count accepted turns: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, "accept-op-1").Scan(&receiptCount); err != nil {
		t.Fatalf("count acceptance receipts: %v", err)
	}
	if eventCount != 1 || turnCount != 1 || receiptCount != 1 {
		t.Fatalf("accepted rows = events:%d turns:%d receipts:%d, want 1 each", eventCount, turnCount, receiptCount)
	}
}

func TestAcceptTurnRequiresExplicitFreshSessionAndRejectsLegacySessionEvents(t *testing.T) {
	t.Run("requires fresh-session assertion", func(t *testing.T) {
		store := newTestStore(t)
		event := liveInputEvent(modulecore.NewSessionID(), modulecore.NewTurnID(), "hello")
		fields := acceptFieldsWithNewSession("accept-no-cutover-assertion", "submission-no-cutover-assertion", event, false)
		if _, err := callLiveStructMethod(t, store, "AcceptTurn", fields); !errors.Is(err, ErrConversationSessionConflict) {
			t.Fatalf("AcceptTurn() error = %v, want ErrConversationSessionConflict without fresh-session assertion", err)
		}
		var sessionCount int
		if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_conversation_session WHERE session_id = ?`, string(event.SessionID)).Scan(&sessionCount); err != nil {
			t.Fatalf("count session after rejected acceptance: %v", err)
		}
		if sessionCount != 0 {
			t.Fatalf("session rows after rejected acceptance = %d, want 0", sessionCount)
		}
	})

	t.Run("rejects v1 events without a journal cutover", func(t *testing.T) {
		store := newTestStore(t)
		ctx := context.Background()
		sessionID := modulecore.NewSessionID()
		legacyEvent := eventFixture(modulecore.NewTraceID(), "legacy.conversation.message", "orchestrator")
		legacyEvent.SessionID = sessionID
		if _, err := store.AppendSequenced(ctx, legacyEvent); err != nil {
			t.Fatalf("AppendSequenced(legacy): %v", err)
		}
		input := liveInputEvent(sessionID, modulecore.NewTurnID(), "new v2 message")
		fields := acceptFieldsWithNewSession("accept-legacy-session", "submission-legacy-session", input, true)
		if _, err := callLiveStructMethod(t, store, "AcceptTurn", fields); !errors.Is(err, ErrConversationSessionConflict) {
			t.Fatalf("AcceptTurn() error = %v, want ErrConversationSessionConflict for session with v1 events", err)
		}
		var sessionCount int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_conversation_session WHERE session_id = ?`, string(sessionID)).Scan(&sessionCount); err != nil {
			t.Fatalf("count session after rejected legacy cutover: %v", err)
		}
		if sessionCount != 0 {
			t.Fatalf("session rows after rejected legacy cutover = %d, want 0", sessionCount)
		}
	})
}

func TestAcceptTurnCanContinueAnExistingJournaledSession(t *testing.T) {
	store := newTestStore(t)
	sessionID := modulecore.NewSessionID()
	firstEvent := liveInputEvent(sessionID, modulecore.NewTurnID(), "first")
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFieldsWithNewSession("accept-new-session", "submission-first", firstEvent, true)); err != nil {
		t.Fatalf("AcceptTurn(first): %v", err)
	}
	secondEvent := liveInputEvent(sessionID, modulecore.NewTurnID(), "second")
	accepted, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFieldsWithNewSession("accept-existing-session", "submission-second", secondEvent, false))
	if err != nil {
		t.Fatalf("AcceptTurn(existing session): %v", err)
	}
	if got := reflect.ValueOf(accepted).FieldByName("AdmissionSeq").Int(); got <= 0 {
		t.Fatalf("second admission sequence = %d, want positive", got)
	}
}

func TestLivePublicMessagesReplayWithoutFakeTaskOrRunIdentity(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := eventFixture(modulecore.NewTraceID(), "conversation.user_message", "orchestrator")
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	input.Payload = map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "content": "hello",
	}
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-replay", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-replay", ActorID: "viewer-user", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(public user event): %v", err)
	}

	reply := eventFixture(input.TraceID, "agent.response", "orchestrator")
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, turnID, modulecore.NewMessageID()
	reply.CausationEventID = accepted.InputEvent.EventID
	reply.Payload = map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "hello back",
	}
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-replay", SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1,
		ResultState: "completed", Events: []modulecore.EventEnvelope{reply},
		MessageIndexes: []MessageIndex{{
			MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public",
		}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(public agent event): %v", err)
	}

	log, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog(): %v", err)
	}
	events, _, err := log.ReplayPage(ctx, 0, 0, 20)
	if err != nil {
		t.Fatalf("ReplayPage(): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("replayed event count = %d, want input and response", len(events))
	}
	if events[0].From != "user" || events[0].TaskID != "" || events[0].RunID != "" || events[0].ActorKind != "" || events[0].ActorID != "" {
		t.Fatalf("replayed input has wrong source or fabricated execution identity: %#v", events[0])
	}
	if events[1].From != "mio" || events[1].TaskID != "" || events[1].RunID != "" || events[1].ActorKind != "" || events[1].ActorID != "" {
		t.Fatalf("replayed response has wrong source or fabricated execution identity: %#v", events[1])
	}
}

func TestAcceptTurnRejectsPublicActorIdentityInEnvelopeAndPayloadMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*modulecore.EventEnvelope)
	}{
		{
			name: "top-level execution actor",
			mutate: func(event *modulecore.EventEnvelope) {
				event.ActorKind, event.ActorID = "user", "viewer-user"
			},
		},
		{
			name: "payload source mismatch",
			mutate: func(event *modulecore.EventEnvelope) {
				event.Payload["source_actor_id"] = "another-user"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			defer store.Close()
			sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
			event := liveInputEvent(sessionID, turnID, "input")
			event.Payload["source_actor_id"] = "viewer-user"
			test.mutate(&event)
			_, err := store.AcceptTurn(context.Background(), AcceptInput{
				OperationID: "accept-invalid-public-actor", OwnerID: "user-1", RuntimeVersion: "v2",
				ClientSubmissionID: "submission-invalid-public-actor", ActorID: "viewer-user", TriggerKind: "user_message",
				GenerationEpoch: 1, CreateNewSession: true, InputEvent: event,
				InputMessage: MessageIndex{
					MessageID: event.MessageID, EventID: event.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
				},
			})
			if err == nil {
				t.Fatal("AcceptTurn() error = nil, want public actor identity mismatch rejection")
			}
		})
	}
}

func TestCommitTurnBundleSharesSequenceAndReturnsCommittedResult(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	prior, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "prior.event", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(prior): %v", err)
	}
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "hello")
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-2", "submission-2", inputEvent)); err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	if inputEvent.EventSeq != 0 {
		t.Fatal("fixture event was mutated by AcceptTurn")
	}

	reply := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	reply.SessionID = sessionID
	reply.TurnID = turnID
	reply.MessageID = modulecore.NewMessageID()
	reply.CausationEventID = inputEvent.EventID
	reply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "hello back", "text": "hello back", "count": int64(4)}

	fields := commitFields("commit-op-1", sessionID, turnID, "completed", []modulecore.EventEnvelope{reply}, []map[string]any{
		messageFields(reply, "mio", "user", "user", "assistant_message", "public"),
	}, nil, []map[string]any{
		{"WorkID": "work-1", "DedupeKey": "postprocess:reply-1", "Kind": "conversation_projection", "SourceEventID": reply.EventID, "SessionID": sessionID, "Priority": 10},
	})
	committed, err := callLiveStructMethod(t, store, "CommitTurnBundle", fields)
	if err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	batch := decodeCommittedBatch(t, committed)
	if len(batch.Events) != 1 || batch.Events[0].EventSeq != prior.EventSeq+2 {
		t.Fatalf("committed events = %#v, want one event after prior append and accepted input", batch.Events)
	}
	stored, found, err := store.GetByID(ctx, reply.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(reply): found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(stored, batch.Events[0]) {
		t.Fatalf("committed event = %#v, stored event = %#v", batch.Events[0], stored)
	}

	replay, err := callLiveStructMethod(t, store, "CommitTurnBundle", fields)
	if err != nil {
		t.Fatalf("CommitTurnBundle(replay): %v", err)
	}
	assertJSONEqual(t, replay, committed)
	if _, err := callLiveStructMethod(t, store, "CommitTurnBundle", commitFields("commit-op-1", sessionID, turnID, "completed", []modulecore.EventEnvelope{changedReply(reply, "changed")}, []map[string]any{
		messageFields(reply, "mio", "user", "user", "assistant_message", "public"),
	}, nil, nil)); err == nil || !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("CommitTurnBundle(same operation, changed output) error = %v, want conflict", err)
	}

	lookupBatch, found, lookupErr := callLiveLookup(t, store, turnID)
	if lookupErr != nil || !found {
		t.Fatalf("LoadCommittedOutcome() found=%t err=%v, want found without error", found, lookupErr)
	}
	assertJSONEqual(t, lookupBatch, committed)

	after, err := store.AppendSequenced(ctx, eventFixture(inputEvent.TraceID, "after.commit", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(after): %v", err)
	}
	if after.EventSeq != batch.Events[0].EventSeq+1 {
		t.Fatalf("AppendSequenced after commit = %d, want %d", after.EventSeq, batch.Events[0].EventSeq+1)
	}
	var outboxCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox WHERE dedupe_key = ?`, "postprocess:reply-1").Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("postprocess outbox count = %d, err = %v, want 1", outboxCount, err)
	}
}

func TestCommitTurnBundleRollsBackWhenSecondEventReferenceIsInvalid(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "hello")
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-3", "submission-3", inputEvent)); err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}

	first := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	first.SessionID, first.TurnID, first.MessageID = sessionID, turnID, modulecore.NewMessageID()
	first.CausationEventID = inputEvent.EventID
	first.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "first", "text": "first"}
	second := eventFixture(inputEvent.TraceID, "conversation.internal.completed", "conversation_journal")
	second.SessionID, second.TurnID = sessionID, turnID
	second.CausationEventID = first.EventID
	second.DependencyEventIDs = []modulecore.EventID{modulecore.NewEventID()}
	second.Payload = map[string]any{"state": "completed"}

	_, err := callLiveStructMethod(t, store, "CommitTurnBundle", commitFields("commit-op-invalid", sessionID, turnID, "completed", []modulecore.EventEnvelope{first, second}, []map[string]any{
		messageFields(first, "mio", "user", "user", "assistant_message", "public"),
	}, nil, []map[string]any{
		{"WorkID": "work-invalid", "DedupeKey": "postprocess:invalid", "Kind": "conversation_projection", "SourceEventID": first.EventID, "SessionID": sessionID, "Priority": 10},
	}))
	if err == nil {
		t.Fatal("CommitTurnBundle() error = nil, want invalid dependency failure")
	}
	assertEventAbsent(t, store, first.EventID)
	assertEventAbsent(t, store, second.EventID)

	var turnState string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_conversation_turn WHERE turn_id = ?`, string(turnID)).Scan(&turnState); err != nil {
		t.Fatalf("read turn state: %v", err)
	}
	if turnState != "accepted" {
		t.Fatalf("turn state after failed commit = %q, want accepted", turnState)
	}
	for _, item := range []struct{ table, key, value string }{
		{"live_operation_receipt", "operation_id", "commit-op-invalid"},
		{"live_work_outbox", "dedupe_key", "postprocess:invalid"},
	} {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", item.table, item.key)
		if err := store.db.QueryRowContext(ctx, query, item.value).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", item.table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after failed commit = %d, want 0", item.table, count)
		}
	}
	_, found, lookupErr := callLiveLookup(t, store, turnID)
	if found || lookupErr != nil {
		t.Fatalf("LoadCommittedOutcome() after rollback found=%t err=%v, want not found", found, lookupErr)
	}
}

func TestCommitTurnBundleRequiresIndexForEveryMessageEvent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "hello")
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-index-required", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-index-required", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	public, publicIndex := liveOutputEvent(accepted, "mio", "user-1", "assistant_message", "public", "reply")
	private := eventFixture(inputEvent.TraceID, "conversation.delegation_instruction", "worker")
	private.SessionID, private.TurnID, private.MessageID = sessionID, turnID, modulecore.NewMessageID()
	private.ActorKind, private.ActorID = "agent", "mio"
	private.Payload = map[string]any{"to": "worker-1", "text": "private instruction"}
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-index-required", SessionID: sessionID, TurnID: turnID,
		GenerationEpoch: accepted.GenerationEpoch, ResultState: "completed",
		Events: []modulecore.EventEnvelope{public, private}, MessageIndexes: []MessageIndex{publicIndex},
	}); err == nil {
		t.Fatal("CommitTurnBundle() error = nil, want rejection for unindexed private message")
	}
	assertEventAbsent(t, store, public.EventID)
	assertEventAbsent(t, store, private.EventID)
}

func TestDelegationBundleKeepsPrivateInstructionOutOfOrchestratorComponent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "please delegate")
	if _, err := callLiveStructMethod(t, store, "AcceptTurn", acceptFields("accept-op-4", "submission-4", inputEvent)); err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}

	notice := eventFixture(inputEvent.TraceID, "conversation.delegation_notice", "orchestrator")
	notice.SessionID, notice.TurnID, notice.MessageID = sessionID, turnID, modulecore.NewMessageID()
	notice.CausationEventID = inputEvent.EventID
	notice.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "I asked Shiro.", "text": "I asked Shiro."}
	instruction := eventFixture(inputEvent.TraceID, "conversation.delegation_instruction", "delegation_mailbox")
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, turnID, modulecore.NewMessageID()
	instruction.CausationEventID = notice.EventID
	instruction.ActorKind, instruction.ActorID = "agent", "mio"
	instruction.Payload = map[string]any{"to": "shiro", "text": "private task instructions"}
	delegation := map[string]any{
		"DelegationID": "delegation-1", "Revision": int64(1), "OriginTurnID": turnID,
		"Assignee": "shiro", "InstructionMessageID": instruction.MessageID,
		"ContextEventID": inputEvent.EventID, "State": "dispatch_accepted",
	}
	fields := commitFields("commit-op-delegate", sessionID, turnID, "awaiting_readback", []modulecore.EventEnvelope{notice, instruction}, []map[string]any{
		messageFields(notice, "mio", "user", "user", "delegation_notice", "public"),
		messageFields(instruction, "mio", "agent", "shiro", "delegation_instruction", "private"),
	}, delegation, []map[string]any{
		{"WorkID": "work-delegation-1", "DedupeKey": "delegation:delegation-1:1:start", "Kind": "dispatch_delegation", "SourceEventID": instruction.EventID, "SessionID": sessionID, "Priority": 5},
	})
	committed, err := callLiveStructMethod(t, store, "CommitTurnBundle", fields)
	if err != nil {
		t.Fatalf("CommitTurnBundle(delegation): %v", err)
	}
	batch := decodeCommittedBatch(t, committed)
	if len(batch.Events) != 2 || batch.Events[0].EventSeq+1 != batch.Events[1].EventSeq {
		t.Fatalf("delegation event order = %#v, want two adjacent persisted sequences", batch.Events)
	}
	if batch.Events[0].ComponentID != "orchestrator" || batch.Events[1].ComponentID == "orchestrator" {
		t.Fatalf("delegation components = %q, %q; private instruction must stay outside orchestrator", batch.Events[0].ComponentID, batch.Events[1].ComponentID)
	}
	orchestratorEvents, err := store.ListByComponent(ctx, "orchestrator", 20)
	if err != nil {
		t.Fatalf("ListByComponent(orchestrator): %v", err)
	}
	for _, event := range orchestratorEvents {
		if event.EventID == instruction.EventID {
			t.Fatalf("private instruction %q appears in orchestrator component replay", instruction.EventID)
		}
	}

	var delegationCount, outboxCount, publicIndexCount, privateIndexCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_delegation WHERE delegation_id = ? AND instruction_message_id = ?`, "delegation-1", string(instruction.MessageID)).Scan(&delegationCount); err != nil {
		t.Fatalf("read delegation row: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox WHERE dedupe_key = ? AND source_event_id = ?`, "delegation:delegation-1:1:start", string(instruction.EventID)).Scan(&outboxCount); err != nil {
		t.Fatalf("read delegation outbox row: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_message_index WHERE message_id = ? AND visibility = 'public'`, string(notice.MessageID)).Scan(&publicIndexCount); err != nil {
		t.Fatalf("read public message index: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_message_index WHERE message_id = ? AND visibility = 'private'`, string(instruction.MessageID)).Scan(&privateIndexCount); err != nil {
		t.Fatalf("read private message index: %v", err)
	}
	if delegationCount != 1 || outboxCount != 1 || publicIndexCount != 1 || privateIndexCount != 1 {
		t.Fatalf("delegation rows = delegation:%d outbox:%d public-index:%d private-index:%d, want 1 each", delegationCount, outboxCount, publicIndexCount, privateIndexCount)
	}
}

type committedBatchView struct {
	OperationID string                     `json:"operation_id"`
	SessionID   modulecore.SessionID       `json:"session_id"`
	TurnID      modulecore.TurnID          `json:"turn_id"`
	ResultState string                     `json:"result_state"`
	Events      []modulecore.EventEnvelope `json:"events"`
}

func decodeCommittedBatch(t *testing.T, value any) committedBatchView {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal committed batch: %v", err)
	}
	var batch committedBatchView
	if err := json.Unmarshal(encoded, &batch); err != nil {
		t.Fatalf("decode committed batch: %v", err)
	}
	return batch
}

func acceptFields(operationID, submissionID string, event modulecore.EventEnvelope) map[string]any {
	return acceptFieldsWithNewSession(operationID, submissionID, event, true)
}

func acceptFieldsWithNewSession(operationID, submissionID string, event modulecore.EventEnvelope, createNewSession bool) map[string]any {
	return map[string]any{
		"OperationID": operationID, "OwnerID": "user-1", "RuntimeVersion": "v2",
		"ClientSubmissionID": submissionID, "ActorID": "user-1", "TriggerKind": "user_message",
		"GenerationEpoch": int64(1), "InputEvent": event, "CreateNewSession": createNewSession,
		"InputMessage": messageFields(event, "user-1", "agent", "mio", "user_message", "public"),
	}
}

func commitFields(operationID string, sessionID modulecore.SessionID, turnID modulecore.TurnID, resultState string, events []modulecore.EventEnvelope, messages []map[string]any, delegation map[string]any, outbox []map[string]any) map[string]any {
	return map[string]any{
		"OperationID": operationID, "SessionID": sessionID, "TurnID": turnID,
		"GenerationEpoch": int64(1), "ResultState": resultState, "Events": events,
		"MessageIndexes": messages, "Delegation": delegation, "Outbox": outbox,
	}
}

func messageFields(event modulecore.EventEnvelope, fromActor, toKind, toActor, kind, visibility string) map[string]any {
	return map[string]any{
		"MessageID": event.MessageID, "EventID": event.EventID, "SessionID": event.SessionID,
		"TurnID": event.TurnID, "FromActor": fromActor, "ToKind": toKind,
		"ToActor": toActor, "Kind": kind, "Visibility": visibility,
	}
}

func liveInputEvent(sessionID modulecore.SessionID, turnID modulecore.TurnID, text string) modulecore.EventEnvelope {
	event := eventFixture(modulecore.NewTraceID(), "conversation.user_message", "orchestrator")
	event.SessionID, event.TurnID, event.MessageID = sessionID, turnID, modulecore.NewMessageID()
	event.Payload = map[string]any{"from": "user", "source_actor_id": "user-1", "to": "mio", "content": text, "text": text}
	return event
}

func changedReply(event modulecore.EventEnvelope, text string) modulecore.EventEnvelope {
	copy := event
	copy.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": text, "text": text}
	return copy
}

func callLiveStructMethod(t *testing.T, store *SQLiteStore, methodName string, fields map[string]any) (any, error) {
	t.Helper()
	method := reflect.ValueOf(store).MethodByName(methodName)
	if !method.IsValid() {
		t.Fatalf("eventstore public API %s is missing", methodName)
	}
	if method.Type().NumIn() != 2 || method.Type().In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		t.Fatalf("%s signature = %v, want context.Context and one input struct", methodName, method.Type())
	}
	inputType := method.Type().In(1)
	if inputType.Kind() != reflect.Struct {
		t.Fatalf("%s input type = %v, want struct", methodName, inputType)
	}
	input := reflect.New(inputType).Elem()
	for name, raw := range fields {
		assignJournalField(t, input, name, raw)
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), input})
	if len(results) != 2 {
		t.Fatalf("%s result count = %d, want value and error", methodName, len(results))
	}
	if !results[1].IsNil() {
		err, ok := results[1].Interface().(error)
		if !ok {
			t.Fatalf("%s second result %v is not error", methodName, results[1].Type())
		}
		return results[0].Interface(), err
	}
	return results[0].Interface(), nil
}

func callLiveLookup(t *testing.T, store *SQLiteStore, turnID modulecore.TurnID) (any, bool, error) {
	t.Helper()
	method := reflect.ValueOf(store).MethodByName("LoadCommittedOutcome")
	if !method.IsValid() {
		t.Fatalf("eventstore public API LoadCommittedOutcome is missing")
	}
	if method.Type().NumIn() != 2 || method.Type().In(1) != reflect.TypeOf(turnID) {
		t.Fatalf("LoadCommittedOutcome signature = %v, want (context.Context, core.TurnID)", method.Type())
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(turnID)})
	if len(results) != 3 {
		t.Fatalf("LoadCommittedOutcome result count = %d, want value, found, and error", len(results))
	}
	var err error
	if !results[2].IsNil() {
		err = results[2].Interface().(error)
	}
	return results[0].Interface(), results[1].Bool(), err
}

func assignJournalField(t *testing.T, target reflect.Value, name string, raw any) {
	t.Helper()
	field := target.FieldByName(name)
	if !field.IsValid() && name == "CreateNewSession" {
		return
	}
	if !field.IsValid() || !field.CanSet() {
		t.Fatalf("journal input %s has no settable %s field", target.Type(), name)
	}
	rawValue := reflect.ValueOf(raw)
	if !rawValue.IsValid() || ((rawValue.Kind() == reflect.Ptr || rawValue.Kind() == reflect.Map || rawValue.Kind() == reflect.Slice || rawValue.Kind() == reflect.Interface) && rawValue.IsNil()) {
		field.Set(reflect.Zero(field.Type()))
		return
	}
	if rawValue.Type().AssignableTo(field.Type()) {
		field.Set(rawValue)
		return
	}
	switch {
	case field.Kind() == reflect.Struct && reflect.TypeOf(raw).Kind() == reflect.Map:
		assignJournalFields(t, field, raw.(map[string]any))
	case field.Kind() == reflect.Ptr && reflect.TypeOf(raw).Kind() == reflect.Map:
		value := reflect.New(field.Type().Elem()).Elem()
		assignJournalFields(t, value, raw.(map[string]any))
		pointer := reflect.New(value.Type())
		pointer.Elem().Set(value)
		field.Set(pointer)
	case field.Kind() == reflect.Slice:
		maps, ok := raw.([]map[string]any)
		if !ok || field.Type().Elem().Kind() != reflect.Struct {
			t.Fatalf("cannot assign %T to journal slice %s", raw, field.Type())
		}
		values := reflect.MakeSlice(field.Type(), len(maps), len(maps))
		for index, valuesByName := range maps {
			assignJournalFields(t, values.Index(index), valuesByName)
		}
		field.Set(values)
	default:
		if rawValue.Type().ConvertibleTo(field.Type()) {
			field.Set(rawValue.Convert(field.Type()))
			return
		}
		t.Fatalf("cannot assign %T to journal field %s.%s (%s)", raw, target.Type(), name, field.Type())
	}
}

func assignJournalFields(t *testing.T, target reflect.Value, fields map[string]any) {
	t.Helper()
	for name, value := range fields {
		assignJournalField(t, target, name, value)
	}
}

func assertLiveTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("table_info(%s) scan: %v", table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s) rows: %v", table, err)
	}
	for _, column := range want {
		if !found[column] {
			t.Errorf("%s.%s is missing", table, column)
		}
	}
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got value: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want value: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON values differ:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
