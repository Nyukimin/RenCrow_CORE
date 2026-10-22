package eventstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestAcceptClarificationReadbackCommitsPrivateQuestionAndMioControlTurn(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)

	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	if accepted.Status != conversation.ReadbackStatusNeedsClarification || accepted.Availability != "free" || accepted.TaskStartQueued {
		t.Fatalf("acceptance = %#v, want needs_clarification/free with no Task start", accepted)
	}
	if accepted.ControlTurn == nil || accepted.ControlTurn.TriggerKind != "clarification_control" ||
		accepted.ControlTurn.State != "accepted" || accepted.ControlTurn.InputMessageID != accepted.Message.MessageID {
		t.Fatalf("accepted control Turn = %#v, want durable typed input Turn", accepted.ControlTurn)
	}
	if got := payloadString(accepted.Event.Payload, "kind"); got != "clarification_question" {
		t.Fatalf("question kind = %q, want clarification_question", got)
	}
	if accepted.Event.EventType != "conversation.clarification_question" {
		t.Fatalf("question event type = %q, want private clarification event", accepted.Event.EventType)
	}
	if got := payloadString(accepted.Event.Payload, "visibility"); got != "private" {
		t.Fatalf("question visibility = %q, want private", got)
	}
	if got := payloadString(accepted.Event.Payload, "to"); got != "mio" {
		t.Fatalf("question recipient = %q, want mio", got)
	}
	if got := payloadString(accepted.Event.Payload, "text"); got != fixture.input.Readback.Text {
		t.Fatalf("question text = %q, want retained assignee question %q", got, fixture.input.Readback.Text)
	}
	for _, privateKey := range []string{"handoff", "goal", "instruction", "constraints", "context_snapshot_hash"} {
		if _, leaked := accepted.Event.Payload[privateKey]; leaked {
			t.Errorf("clarification question leaks private handoff field %q", privateKey)
		}
	}
	if accepted.Message.Visibility != "private" || accepted.Message.ToKind != "agent" || accepted.Message.ToActor != "mio" || accepted.Message.FromActor != fixture.assignee {
		t.Fatalf("question index = %#v, want private assignee-to-Mio message", accepted.Message)
	}

	var controlTurnID, actorID, triggerKind, submissionID, state, sourceTurnID, delegationID string
	var controlInputMessageID string
	var generationEpoch int64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT turn_id, actor_id, trigger_kind, client_submission_id, state, source_turn_id, delegation_id,
		       input_message_id, generation_epoch
		FROM live_conversation_turn
		WHERE session_id = ? AND trigger_kind = 'clarification_control'`, string(fixture.sessionID),
	).Scan(&controlTurnID, &actorID, &triggerKind, &submissionID, &state, &sourceTurnID, &delegationID, &controlInputMessageID, &generationEpoch); err != nil {
		t.Fatalf("read accepted clarification control turn: %v", err)
	}
	if controlTurnID == string(fixture.turnID) || actorID != fixture.assignee || triggerKind != "clarification_control" ||
		state != "accepted" || sourceTurnID != string(fixture.turnID) || delegationID != string(fixture.delegationID) ||
		controlInputMessageID != string(accepted.Message.MessageID) || generationEpoch != 1 {
		t.Fatalf("control turn = id:%q actor:%q trigger:%q state:%q source:%q delegation:%q input:%q epoch:%d", controlTurnID, actorID, triggerKind, state, sourceTurnID, delegationID, controlInputMessageID, generationEpoch)
	}
	if submissionID != "clarification:"+string(fixture.delegationID)+":1" {
		t.Fatalf("control submission id = %q, want stable delegation/revision identity", submissionID)
	}

	var taskCount int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_work_outbox WHERE kind = 'start_delegated_task'`).Scan(&taskCount); err != nil {
		t.Fatalf("count Task start work: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("Task start work count = %d, want zero for clarification", taskCount)
	}
}

func TestAcceptClarificationReadbackAdvancesSessionAdmissionBoundaryAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clarification-admission.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	before, found, err := store.ReadSessionState(context.Background(), fixture.sessionID, "user-1")
	if err != nil || !found {
		t.Fatalf("ReadSessionState(before): state=%#v found=%t err=%v", before, found, err)
	}
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	if accepted.ControlTurn == nil || before.AdmissionSeq >= accepted.ControlTurn.AdmissionSeq {
		t.Fatalf("fixture admission boundary %d did not precede control Turn %v", before.AdmissionSeq, accepted.ControlTurn)
	}
	after, found, err := store.ReadSessionState(context.Background(), fixture.sessionID, "user-1")
	if err != nil || !found || after.AdmissionSeq != accepted.Event.EventSeq || after.AdmissionSeq != accepted.ControlTurn.AdmissionSeq {
		t.Fatalf("ReadSessionState(after): state=%#v found=%t err=%v; want admission_seq=%d", after, found, err, accepted.Event.EventSeq)
	}
	if after.RuntimeVersion != before.RuntimeVersion || after.CutoverSeq != before.CutoverSeq ||
		after.ProjectionWatermark != before.ProjectionWatermark || after.GenerationEpoch != before.GenerationEpoch {
		t.Fatalf("control Turn changed session version/cutover/projection/epoch: before=%#v after=%#v", before, after)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(before restart): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	afterRestart, found, err := store.ReadSessionState(context.Background(), fixture.sessionID, "user-1")
	if err != nil || !found || !reflect.DeepEqual(afterRestart, after) {
		t.Fatalf("ReadSessionState(after restart): state=%#v found=%t err=%v; want exact committed boundary %#v", afterRestart, found, err, after)
	}
}

func TestAcceptClarificationReadbackRollsBackWhenSessionAdmissionCannotAdvance(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	before, found, err := store.ReadSessionState(context.Background(), fixture.sessionID, "user-1")
	if err != nil || !found {
		t.Fatalf("ReadSessionState(before): state=%#v found=%t err=%v", before, found, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_clarification_admission_advance
		BEFORE UPDATE OF admission_seq ON live_conversation_session
		WHEN NEW.admission_seq > OLD.admission_seq
		BEGIN SELECT RAISE(ABORT, 'injected clarification admission failure'); END`); err != nil {
		t.Fatalf("install admission failure trigger: %v", err)
	}
	if _, err := store.AcceptReadback(context.Background(), fixture.input); err == nil {
		t.Fatal("AcceptReadback() error = nil, want injected session admission failure")
	}
	assertReadbackNotAccepted(t, store, fixture)
	after, found, err := store.ReadSessionState(context.Background(), fixture.sessionID, "user-1")
	if err != nil || !found || !reflect.DeepEqual(after, before) {
		t.Fatalf("ReadSessionState(after failed admission): state=%#v found=%t err=%v; want unchanged %#v", after, found, err, before)
	}
}

func TestAcceptClarificationReadbackIsIdempotentAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	first, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(first): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(restarted store): %v", err)
		}
	})

	retry := fixture.input
	retry.EventID = modulecore.NewEventID()
	retry.MessageID = modulecore.NewMessageID()
	replayed, err := store.AcceptReadback(context.Background(), retry)
	if err != nil {
		t.Fatalf("AcceptReadback(retry): %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("restart replay = %#v, want original durable result %#v", replayed, first)
	}
	var turns, questions, receipts, taskStarts int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE trigger_kind='clarification_control'`, nil, &turns},
		{`SELECT COUNT(*) FROM live_message_index WHERE kind='clarification_question' AND visibility='private'`, nil, &questions},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=? AND operation_kind='accept_readback'`, []any{fixture.input.OperationID}, &receipts},
		{`SELECT COUNT(*) FROM live_work_outbox WHERE kind='start_delegated_task'`, nil, &taskStarts},
	} {
		if err := store.db.QueryRow(check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count durable clarification rows: %v", err)
		}
	}
	if turns != 1 || questions != 1 || receipts != 1 || taskStarts != 0 {
		t.Fatalf("durable clarification rows = turns:%d questions:%d receipts:%d taskStarts:%d; want 1/1/1/0", turns, questions, receipts, taskStarts)
	}
	pending, err := store.ReadPendingAcceptedTurns(context.Background(), fixture.sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(after restart): %v", err)
	}
	if len(pending) != 1 || pending[0].TriggerKind != "clarification_control" || pending[0].TurnID != first.ControlTurn.TurnID || pending[0].ActorID != fixture.assignee {
		t.Fatalf("restart accepted Turns = %#v, want exactly the durable clarification control Turn", pending)
	}
}

func TestAcceptClarificationReadbackRejectsMismatchedAssigneeAndStaleRevision(t *testing.T) {
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
			fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
			test.mutate(&fixture)
			if _, err := store.AcceptReadback(context.Background(), fixture.input); !errors.Is(err, test.want) {
				t.Fatalf("AcceptReadback() error = %v, want %v", err, test.want)
			}
			assertReadbackNotAccepted(t, store, fixture)
		})
	}
}

func TestAcceptClarificationReadbackRollsBackPrivateQuestionWhenControlTurnCannotCommit(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_clarification_control_turn
		BEFORE INSERT ON live_conversation_turn
		WHEN NEW.trigger_kind = 'clarification_control'
		BEGIN SELECT RAISE(ABORT, 'injected clarification control Turn failure'); END`); err != nil {
		t.Fatalf("install control Turn failure trigger: %v", err)
	}
	if _, err := store.AcceptReadback(context.Background(), fixture.input); err == nil {
		t.Fatal("AcceptReadback() error = nil, want injected control Turn failure")
	}
	assertReadbackNotAccepted(t, store, fixture)
}
