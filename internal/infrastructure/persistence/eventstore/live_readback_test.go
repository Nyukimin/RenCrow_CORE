package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAcceptReadbackCommitsPublicReadbackAndTaskStartAtomically(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)

	accepted, err := store.AcceptReadback(ctx, fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	if accepted.Status != conversation.ReadbackStatusAccepted || accepted.Availability != "free" || !accepted.TaskStartQueued {
		t.Fatalf("acceptance = %#v, want accepted/free with task start queued", accepted)
	}
	event := accepted.Event
	if event.EventSeq <= 0 || event.SessionID != fixture.sessionID || event.TurnID != fixture.turnID || event.TraceID != fixture.traceID || event.RequestID != fixture.delegationID {
		t.Fatalf("readback event correlation/sequence = %#v", event)
	}
	if event.ComponentID != "orchestrator" || event.ActorKind != "" || event.ActorID != "" || event.TaskID != "" || event.RunID != "" {
		t.Fatalf("public readback event has execution identity or wrong component: %#v", event)
	}
	for key, want := range map[string]string{
		"kind": "readback", "visibility": "public", "from": fixture.assignee,
		"source_actor_id": fixture.assignee, "to": "user", "availability": "free",
		"delegation_id": string(fixture.delegationID), "status": "accepted",
	} {
		if got := payloadString(event.Payload, key); got != want {
			t.Errorf("event payload %s = %q, want %q", key, got, want)
		}
	}
	for _, privateKey := range []string{"handoff", "goal", "instruction", "constraints", "context_snapshot_hash"} {
		if _, leaked := event.Payload[privateKey]; leaked {
			t.Errorf("public readback event leaks private handoff field %q", privateKey)
		}
	}

	stored, found, err := store.GetByID(ctx, event.EventID)
	if err != nil || !found || !reflect.DeepEqual(stored, event) {
		t.Fatalf("stored readback event found=%t err=%v event=%#v", found, err, stored)
	}
	var fromActor, toKind, toActor, kind, visibility, turnState, delegationState, resultReceiptID string
	var sourceEventID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT m.from_actor, m.to_kind, m.to_actor, m.kind, m.visibility,
		       t.state, d.state, t.result_receipt_id
		FROM live_message_index m
		JOIN live_conversation_turn t ON t.turn_id = m.turn_id
		JOIN live_delegation d ON d.delegation_id = ? AND d.revision = ?
		WHERE m.message_id = ?`, string(fixture.delegationID), fixture.revision, string(event.MessageID),
	).Scan(&fromActor, &toKind, &toActor, &kind, &visibility, &turnState, &delegationState, &resultReceiptID); err != nil {
		t.Fatalf("read committed readback indexes/state: %v", err)
	}
	if fromActor != fixture.assignee || toKind != "user" || toActor != "user-1" || kind != "readback" || visibility != "public" || turnState != "completed" || delegationState != "accepted" {
		t.Fatalf("readback indexes/state = from:%q to:%s/%s kind:%q visibility:%q turn:%q delegation:%q", fromActor, toKind, toActor, kind, visibility, turnState, delegationState)
	}
	if want := "commit-readback-handoff-" + string(fixture.turnID); resultReceiptID != want {
		t.Fatalf("origin turn result receipt = %q, want original output receipt %q", resultReceiptID, want)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT source_event_id FROM live_work_outbox WHERE kind = 'start_delegated_task' AND dedupe_key = ?`, readbackStartDedupeKey(fixture.delegationID, fixture.revision)).Scan(&sourceEventID); err != nil {
		t.Fatalf("read task-start outbox: %v", err)
	}
	if sourceEventID != string(event.EventID) {
		t.Fatalf("task-start source event = %q, want committed readback %q", sourceEventID, event.EventID)
	}
	var taskStarts, receipts int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox WHERE kind = 'start_delegated_task'`).Scan(&taskStarts); err != nil {
		t.Fatalf("count task starts: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = 'accept_readback'`, fixture.input.OperationID).Scan(&receipts); err != nil {
		t.Fatalf("count readback receipts: %v", err)
	}
	if taskStarts != 1 || receipts != 1 {
		t.Fatalf("task starts=%d receipts=%d, want one each", taskStarts, receipts)
	}
}

func TestAcceptReadbackIsIdempotentAndConflictsOnChangedContent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)

	first, err := store.AcceptReadback(ctx, fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(first): %v", err)
	}
	retry := fixture.input
	retry.EventID = modulecore.NewEventID()
	retry.MessageID = modulecore.NewMessageID()
	retry.OccurredAt = fixture.input.OccurredAt.Add(time.Minute)
	replayed, err := store.AcceptReadback(ctx, retry)
	if err != nil {
		t.Fatalf("AcceptReadback(retry): %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("retry acceptance = %#v, want original receipt/events %#v", replayed, first)
	}

	changed := fixture.input
	changed.Readback.Text += " changed"
	if _, err := store.AcceptReadback(ctx, changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("AcceptReadback(changed content) error = %v, want ErrOperationConflict", err)
	}
	newOperation := fixture.input
	newOperation.OperationID += "-again"
	if _, err := store.AcceptReadback(ctx, newOperation); !errors.Is(err, ErrReadbackConflict) {
		t.Fatalf("AcceptReadback(already completed delegation) error = %v, want ErrReadbackConflict", err)
	}
	var eventCount, receiptCount, taskCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, string(first.Event.EventID)).Scan(&eventCount); err != nil {
		t.Fatalf("count readback event: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, fixture.input.OperationID).Scan(&receiptCount); err != nil {
		t.Fatalf("count receipt: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox WHERE kind = 'start_delegated_task'`).Scan(&taskCount); err != nil {
		t.Fatalf("count task starts: %v", err)
	}
	if eventCount != 1 || receiptCount != 1 || taskCount != 1 {
		t.Fatalf("after retry/conflict rows = event:%d receipt:%d task:%d, want 1 each", eventCount, receiptCount, taskCount)
	}
}

func TestAcceptReadbackRejectsUntrustedAssigneeAndStaleRevision(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   error
		mutate func(*readbackFixture)
	}{
		{name: "assignee mismatch", want: ErrReadbackAssigneeMismatch, mutate: func(f *readbackFixture) { f.input.Assignee = "kuro" }},
		{name: "stale revision", want: ErrReadbackConflict, mutate: func(f *readbackFixture) { f.input.Revision++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
			test.mutate(&fixture)
			if _, err := store.AcceptReadback(context.Background(), fixture.input); !errors.Is(err, test.want) {
				t.Fatalf("AcceptReadback() error = %v, want %v", err, test.want)
			}
			assertReadbackNotAccepted(t, store, fixture)
		})
	}
}

func TestAcceptRejectedReadbackFreesMioWithoutStartingTask(t *testing.T) {
	for _, status := range []conversation.ReadbackStatus{conversation.ReadbackStatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			store := newTestStore(t)
			fixture := newReadbackFixture(t, store, status)
			accepted, err := store.AcceptReadback(context.Background(), fixture.input)
			if err != nil {
				t.Fatalf("AcceptReadback(): %v", err)
			}
			if accepted.Status != status || accepted.Availability != "free" || accepted.TaskStartQueued {
				t.Fatalf("acceptance = %#v, want %q/free without task start", accepted, status)
			}
			if got := payloadString(accepted.Event.Payload, "kind"); got != "handoff_error" || accepted.Message.Kind != "handoff_error" {
				t.Fatalf("non-accepted public kind = payload:%q index:%q, want handoff_error", got, accepted.Message.Kind)
			}
			if got := payloadString(accepted.Event.Payload, "availability"); got != "free" || !strings.Contains(payloadString(accepted.Event.Payload, "content"), fixture.input.Readback.Text) {
				t.Fatalf("non-accepted public content/availability = %q/%q", payloadString(accepted.Event.Payload, "content"), got)
			}
			var turnState, delegationState string
			if err := store.db.QueryRowContext(context.Background(), `SELECT state FROM live_conversation_turn WHERE turn_id = ?`, string(fixture.turnID)).Scan(&turnState); err != nil {
				t.Fatalf("read turn state: %v", err)
			}
			if err := store.db.QueryRowContext(context.Background(), `SELECT state FROM live_delegation WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil {
				t.Fatalf("read delegation state: %v", err)
			}
			var taskCount int
			if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_work_outbox WHERE kind = 'start_delegated_task'`).Scan(&taskCount); err != nil {
				t.Fatalf("count task starts: %v", err)
			}
			if turnState != "completed" || delegationState != string(status) || taskCount != 0 {
				t.Fatalf("turn=%q delegation=%q starts=%d; want completed/%q/0", turnState, delegationState, taskCount, status)
			}
		})
	}
}

func TestAcceptReadbackRollsBackEveryWriteWhenTaskOutboxInsertFails(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
	workID := readbackStartWorkID(fixture.delegationID, fixture.revision)
	if _, err := store.db.Exec(`INSERT INTO live_work_outbox
		(work_id, dedupe_key, kind, source_event_id, session_id, priority)
		VALUES (?, ?, 'preexisting', ?, ?, 0)`, workID, "preexisting:"+workID, string(fixture.causeEventID), string(fixture.sessionID)); err != nil {
		t.Fatalf("insert conflicting work id: %v", err)
	}

	if _, err := store.AcceptReadback(context.Background(), fixture.input); err == nil {
		t.Fatal("AcceptReadback() error = nil, want task outbox uniqueness failure")
	}
	assertReadbackNotAccepted(t, store, fixture)
}

type readbackFixture struct {
	sessionID    modulecore.SessionID
	turnID       modulecore.TurnID
	traceID      modulecore.TraceID
	delegationID modulecore.RequestID
	causeEventID modulecore.EventID
	revision     uint64
	assignee     string
	input        ReadbackInput
}

func newReadbackFixture(t *testing.T, store *SQLiteStore, status conversation.ReadbackStatus) readbackFixture {
	t.Helper()
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "please delegate this task")
	if _, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-readback-input-" + string(turnID), OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-readback-" + string(turnID), ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	}); err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}

	delegationID, revision, assignee := modulecore.NewRequestID(), uint64(1), "shiro"
	instructionMessageID := modulecore.NewMessageID()
	contextMessages := []DelegationContextMessage{{MessageID: inputEvent.MessageID, Role: "user", Text: "please delegate this task"}}
	contextMaterial := struct {
		SourceRevision  string                     `json:"source_revision"`
		CutoverRevision string                     `json:"cutover_revision"`
		Messages        []DelegationContextMessage `json:"messages"`
	}{SourceRevision: "1", CutoverRevision: "0", Messages: contextMessages}
	contextBytes, err := json.Marshal(contextMaterial)
	if err != nil {
		t.Fatalf("marshal readback fixture context: %v", err)
	}
	contextDigest := sha256.Sum256(contextBytes)
	contextHash := hex.EncodeToString(contextDigest[:])
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: revision, OriginTurnID: turnID, OriginUserMessageID: inputEvent.MessageID,
		SessionID: sessionID, SourceTraceID: inputEvent.TraceID, FromActor: "mio", ToActor: assignee,
		InstructionMessageID: instructionMessageID, ContextSnapshotHash: contextHash,
		AllowedCapabilities: []string{}, ReplyTo: inputEvent.MessageID,
		Goal: "Summarize a repository behavior", Instruction: "Inspect the specified implementation and report evidence.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the assigned files.", SourceMessageIDs: []modulecore.MessageID{inputEvent.MessageID}}},
		ExpectedOutput: "A concise evidence-backed report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	notice := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	notice.SessionID, notice.TurnID, notice.MessageID = sessionID, turnID, modulecore.NewMessageID()
	notice.CausationEventID = inputEvent.EventID
	notice.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "I asked Shiro.", "text": "I asked Shiro."}
	contextEvent := eventFixture(inputEvent.TraceID, "conversation.delegation_context", "conversation_runtime")
	contextEvent.SessionID, contextEvent.TurnID = sessionID, turnID
	contextEvent.CausationEventID = inputEvent.EventID
	contextEvent.Payload = map[string]any{
		"source_revision": "1", "cutover_revision": "0", "messages": contextMessages,
		"context_snapshot_hash": contextHash, "handoff": handoff,
	}
	instruction := eventFixture(inputEvent.TraceID, "conversation.delegation_instruction", "delegation_mailbox")
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, turnID, instructionMessageID
	instruction.CausationEventID = notice.EventID
	instruction.ActorKind, instruction.ActorID = "agent", "mio"
	instruction.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": assignee, "text": handoff.Instruction}
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-readback-handoff-" + string(turnID), SessionID: sessionID, TurnID: turnID,
		GenerationEpoch: 1, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, contextEvent, instruction},
		MessageIndexes: []MessageIndex{
			{MessageID: notice.MessageID, EventID: notice.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "delegation_notice", Visibility: "public"},
			{MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "agent", ToActor: assignee, Kind: "delegation_instruction", Visibility: "private"},
		},
		Delegation: &DelegationRecord{
			DelegationID: string(delegationID), Revision: int64(revision), OriginTurnID: turnID,
			Assignee: assignee, InstructionMessageID: instruction.MessageID, ContextEventID: contextEvent.EventID,
			State: "dispatch_accepted",
		},
		Outbox: []WorkItem{{
			WorkID: "dispatch-readback-" + string(turnID), DedupeKey: "delegation:" + string(delegationID) + ":1:start",
			Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: sessionID, Priority: 5,
		}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(handoff): %v", err)
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: delegationID, Revision: revision, Sender: assignee, SchemaVersion: "agent.readback.v1",
		Status: status, UnderstoodGoal: handoff.Goal, AcknowledgedConstraintKeys: []string{"keep_scope"},
		ExpectedOutput: handoff.ExpectedOutput, Text: "I understand the assignment and will stay within the requested scope.",
	}
	if status != conversation.ReadbackStatusAccepted {
		readback.AcknowledgedConstraintKeys = nil
	}
	return readbackFixture{
		sessionID: sessionID, turnID: turnID, traceID: inputEvent.TraceID, delegationID: delegationID,
		causeEventID: inputEvent.EventID, revision: revision, assignee: assignee,
		input: ReadbackInput{
			OperationID: "readback-op-" + string(turnID), DelegationID: delegationID, Revision: revision,
			Assignee: assignee, Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
			OccurredAt: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
		},
	}
}

func assertReadbackNotAccepted(t *testing.T, store *SQLiteStore, fixture readbackFixture) {
	t.Helper()
	var eventCount, indexCount, receiptCount, taskCount, controlTurnCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(fixture.input.EventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_message_index WHERE message_id = ?`, []any{string(fixture.input.MessageID)}, &indexCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, []any{fixture.input.OperationID}, &receiptCount},
		{`SELECT COUNT(*) FROM live_work_outbox WHERE kind = 'start_delegated_task'`, nil, &taskCount},
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE trigger_kind = 'clarification_control' AND delegation_id = ?`, []any{string(fixture.delegationID)}, &controlTurnCount},
	} {
		if err := store.db.QueryRow(check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count after failed readback: %v", err)
		}
	}
	var turnState, delegationState string
	if err := store.db.QueryRow(`SELECT state FROM live_conversation_turn WHERE turn_id = ?`, string(fixture.turnID)).Scan(&turnState); err != nil {
		t.Fatalf("read turn state after failed readback: %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_delegation WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil {
		t.Fatalf("read delegation state after failed readback: %v", err)
	}
	if eventCount != 0 || indexCount != 0 || receiptCount != 0 || taskCount != 0 || controlTurnCount != 0 || turnState != "awaiting_readback" || delegationState != "dispatch_accepted" {
		t.Fatalf("failed readback left event:%d index:%d receipt:%d task:%d control_turn:%d turn:%q delegation:%q", eventCount, indexCount, receiptCount, taskCount, controlTurnCount, turnState, delegationState)
	}
}
