package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLoadClarificationControlContextReturnsCanonicalPrivateSource(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}

	loaded, err := store.LoadClarificationControlContext(context.Background(), ClarificationControlContextInput{
		SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID,
		OwnerID: "user-1", Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(): %v", err)
	}
	if loaded.ControlTurn.TurnID != accepted.ControlTurn.TurnID || loaded.OriginUserActorID != "user-1" ||
		loaded.Handoff.DelegationID != fixture.delegationID ||
		loaded.Handoff.Revision != fixture.revision || loaded.Handoff.ToActor != fixture.assignee ||
		loaded.QuestionMessage.MessageID != accepted.Message.MessageID || loaded.QuestionMessage.Visibility != "private" ||
		loaded.QuestionEvent.EventID != accepted.Event.EventID || len(loaded.Context) != 1 ||
		loaded.Context[0].MessageID != loaded.Handoff.OriginUserMessageID || loaded.Context[0].Role != "user" ||
		loaded.Context[0].Text != "please delegate this task" {
		// The context assertion below is checked by exact source identity; avoid
		// accepting a context reconstructed from the private question text.
		t.Fatalf("loaded clarification context = %#v", loaded)
	}
}

func TestCommitClarificationAskUserPersistsPromptWithoutReplyAssociation(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser,
		Question: "Which date range should Shiro use?",
	})

	committed, err := store.CommitClarificationDecision(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(ask_user): %v", err)
	}
	if committed.Kind != conversation.ClarificationDecisionAskUser || committed.Event.EventType != "agent.response" ||
		committed.Event.ComponentID != "orchestrator" || committed.Event.RequestID != "" ||
		committed.Event.ActorKind != "" || committed.Event.ActorID != "" ||
		committed.Message.Kind != "clarification_prompt" || committed.Message.Visibility != "public" ||
		committed.Message.ToKind != "user" || committed.Message.ToActor != "user-1" || committed.DispatchQueued || committed.NextHandoff != nil {
		t.Fatalf("ask-user commit = %#v, want public clarification prompt with no continuation", committed)
	}
	if payloadString(committed.Event.Payload, "kind") != "clarification_prompt" ||
		payloadString(committed.Event.Payload, "text") != input.Decision.Question ||
		payloadString(committed.Event.Payload, "visibility") != "public" ||
		payloadString(committed.Event.Payload, "availability") != "free" ||
		payloadString(committed.Event.Payload, "delegation_id") != string(fixture.delegationID) ||
		payloadString(committed.Event.Payload, "reply_to_message_id") != string(accepted.Message.ReplyToMessageID) {
		t.Fatalf("ask-user event payload = %#v", committed.Event.Payload)
	}
	if len(committed.Events) != 2 || committed.Events[1].EventType != "conversation.mio_emission_receipt" ||
		committed.Events[1].SessionID != input.SessionID || committed.Events[1].TurnID != input.ControlTurnID ||
		committed.Events[1].ComponentID != "conversation_runtime" ||
		committed.Events[1].CausationEventID != accepted.Event.EventID ||
		payloadString(committed.Events[1].Payload, "status") != "completed" {
		t.Fatalf("ask-user output receipt event = %#v, want private non-message Mio receipt on the control Turn", committed.Events)
	}
	if committed.InvocationReceipt != input.InvocationReceipt || committed.ReceiptEvent.EventID != input.ReceiptEventID ||
		payloadString(committed.ReceiptEvent.Payload, "request_id") != input.InvocationReceipt.RequestID ||
		payloadString(committed.ReceiptEvent.Payload, "response_id") != input.InvocationReceipt.ResponseID ||
		committed.ReceiptEvent.MessageID != "" {
		t.Fatalf("ask-user output omitted typed InvocationReceipt or its non-message event: %#v", committed)
	}
	if _, exists := committed.ReceiptEvent.Payload["provider_label"]; exists {
		t.Fatal("minimal Mio receipt event exposed provider_label")
	}
	if _, exists := committed.ReceiptEvent.Payload["tokens_used"]; exists {
		t.Fatal("minimal Mio receipt event exposed tokens_used")
	}
	if committed.Message.Kind == "user_message" {
		t.Fatal("clarification prompt masquerades as a user_message")
	}
	var controlState, rootState, delegationState string
	if err := store.db.QueryRow(`SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(input.ControlTurnID)).Scan(&controlState); err != nil {
		t.Fatalf("read control Turn state: %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(fixture.turnID)).Scan(&rootState); err != nil {
		t.Fatalf("read origin Turn state: %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil {
		t.Fatalf("read delegation state: %v", err)
	}
	if controlState != "completed" || rootState != "completed" || delegationState != string(conversation.ReadbackStatusNeedsClarification) {
		t.Fatalf("states after ask-user commit = control:%q root:%q delegation:%q", controlState, rootState, delegationState)
	}
	var taskStarts, dispatches int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='start_delegated_task'`).Scan(&taskStarts); err != nil {
		t.Fatalf("count Task starts: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='dispatch_delegation' AND state='pending'`).Scan(&dispatches); err != nil {
		t.Fatalf("count dispatch work: %v", err)
	}
	if taskStarts != 0 || dispatches != 1 {
		t.Fatalf("outbox after ask-user commit = Task:%d dispatch:%d, want no Task and only the original dispatch", taskStarts, dispatches)
	}
	userReplyTurn := modulecore.NewTurnID()
	userReplyEvent := liveInputEvent(fixture.sessionID, userReplyTurn, "Use the current quarter.")
	if _, err := store.AcceptTurn(context.Background(), AcceptInput{
		OperationID: "accept-user-reply-" + string(userReplyTurn), OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "clarification-reply-" + string(userReplyTurn), ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, InputEvent: userReplyEvent,
		InputMessage: MessageIndex{
			MessageID: userReplyEvent.MessageID, EventID: userReplyEvent.EventID, SessionID: fixture.sessionID, TurnID: userReplyTurn,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message",
			ReplyToMessageID: committed.Message.MessageID, Visibility: "public",
		},
	}); err != nil {
		t.Fatalf("AcceptTurn(explicit reply to prompt): %v", err)
	}
	var linkedDelegation sql.NullString
	if err := store.db.QueryRow(`SELECT delegation_id FROM live_conversation_turn WHERE turn_id=?`, string(userReplyTurn)).Scan(&linkedDelegation); err != nil {
		t.Fatalf("read user reply delegation association: %v", err)
	}
	if linkedDelegation.Valid {
		t.Fatalf("user reply was automatically associated with delegation %q", linkedDelegation.String)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil || delegationState != string(conversation.ReadbackStatusNeedsClarification) {
		t.Fatalf("user reply changed unresolved delegation state=%q err=%v", delegationState, err)
	}
}

func TestCommitClarificationContinuationAtomicallyCreatesFencedRevisionAndDispatch(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	control, err := store.LoadClarificationControlContext(context.Background(), ClarificationControlContextInput{
		SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID, OwnerID: "user-1", Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
		Instruction: "Use the date range from the user's original request and explain any uncertainty.",
	})
	input.ContextEventID = modulecore.NewEventID()

	committed, err := store.CommitClarificationDecision(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(continue): %v", err)
	}
	if committed.Kind != conversation.ClarificationDecisionContinueDelegation || !committed.DispatchQueued || committed.NextHandoff == nil {
		t.Fatalf("continuation commit = %#v, want next Handoff and dispatch", committed)
	}
	old := control.Handoff
	next := *committed.NextHandoff
	if next.DelegationID != old.DelegationID || next.Revision != old.Revision+1 || next.OriginTurnID != old.OriginTurnID ||
		next.OriginUserMessageID != old.OriginUserMessageID || next.SessionID != old.SessionID || next.SourceTraceID != old.SourceTraceID ||
		next.FromActor != old.FromActor || next.ToActor != old.ToActor || next.ContextSnapshotHash != old.ContextSnapshotHash ||
		next.AllowedCapabilities == nil || !reflect.DeepEqual(next.AllowedCapabilities, old.AllowedCapabilities) ||
		!reflect.DeepEqual(next.Constraints, old.Constraints) || next.Goal != old.Goal || next.ExpectedOutput != old.ExpectedOutput ||
		next.Instruction != input.Decision.Instruction || next.InstructionMessageID != input.MessageID {
		t.Fatalf("next Handoff = %#v, want same delegation/assignee/constraints/ACL with only instruction and revision advanced", next)
	}
	if committed.Event.EventType != "conversation.delegation_instruction" || committed.Message.Visibility != "private" ||
		committed.Message.ToKind != "agent" || committed.Message.ToActor != fixture.assignee || committed.Event.ComponentID == "orchestrator" {
		t.Fatalf("continuation instruction = event:%#v index:%#v, want private same-assignee event", committed.Event, committed.Message)
	}
	if len(committed.Events) != 3 || committed.Events[2].EventType != "conversation.mio_emission_receipt" ||
		committed.Events[2].SessionID != input.SessionID || committed.Events[2].TurnID != input.ControlTurnID ||
		committed.Events[2].CausationEventID != control.QuestionEvent.EventID || len(committed.MessageIndexes) != 1 {
		t.Fatalf("continuation output receipt event = %#v, want non-message receipt on control Turn after origin-Turn events", committed.Events)
	}
	if committed.InvocationReceipt != input.InvocationReceipt || committed.ReceiptEvent.EventID != input.ReceiptEventID ||
		committed.ReceiptEvent.EventSeq <= committed.Events[1].EventSeq || committed.ReceiptEvent.MessageID != "" {
		t.Fatalf("continuation output omitted typed Mio receipt or receipt event ordering: %#v", committed)
	}
	var oldState, newState, controlState, rootState string
	for _, row := range []struct {
		query string
		args  []any
		out   *string
	}{
		{`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=1`, []any{string(fixture.delegationID)}, &oldState},
		{`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=2`, []any{string(fixture.delegationID)}, &newState},
		{`SELECT state FROM live_conversation_turn WHERE turn_id=?`, []any{string(input.ControlTurnID)}, &controlState},
		{`SELECT state FROM live_conversation_turn WHERE turn_id=?`, []any{string(fixture.turnID)}, &rootState},
	} {
		if err := store.db.QueryRow(row.query, row.args...).Scan(row.out); err != nil {
			t.Fatalf("read continuation state: %v", err)
		}
	}
	if oldState != string(conversation.ReadbackStatusNeedsClarification) || newState != "dispatch_accepted" ||
		controlState != "awaiting_readback" || rootState != "completed" {
		t.Fatalf("continuation states old:%q new:%q control:%q root:%q", oldState, newState, controlState, rootState)
	}
	var dispatchCount, taskStarts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='dispatch_delegation' AND dedupe_key=? AND source_event_id=? AND state='pending'`,
		"delegation:"+string(fixture.delegationID)+":2:start", string(committed.Event.EventID)).Scan(&dispatchCount); err != nil {
		t.Fatalf("read revision-2 dispatch work: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='start_delegated_task'`).Scan(&taskStarts); err != nil {
		t.Fatalf("count Task starts: %v", err)
	}
	if dispatchCount != 1 || taskStarts != 0 {
		t.Fatalf("continuation outbox dispatch:%d Task starts:%d, want 1/0", dispatchCount, taskStarts)
	}

	work, err := store.ClaimWork(context.Background(), WorkClaimInput{Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: fixture.input.OccurredAt.Add(time.Minute)})
	if err != nil || len(work) != 1 {
		t.Fatalf("ClaimWork(revision 2): items=%#v err=%v", work, err)
	}
	dispatch, err := store.LoadDelegationDispatch(context.Background(), DelegationDispatchInput{
		WorkID: work[0].WorkID, LeaseToken: work[0].LeaseToken, Version: work[0].Version,
		Now: fixture.input.OccurredAt.Add(time.Minute), SessionID: fixture.sessionID, Assignee: fixture.assignee,
	})
	if err != nil || dispatch.Handoff.Revision != 2 || dispatch.Handoff.Instruction != input.Decision.Instruction {
		t.Fatalf("LoadDelegationDispatch(revision 2): handoff=%#v err=%v", dispatch.Handoff, err)
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 2, Sender: fixture.assignee, SchemaVersion: "agent.readback.v1",
		Status: conversation.ReadbackStatusAccepted, UnderstoodGoal: next.Goal,
		AcknowledgedConstraintKeys: []string{"keep_scope"}, ExpectedOutput: next.ExpectedOutput,
		Text: "I understand the revised instruction and the original scope.",
	}
	readbackResult, err := store.AcceptReadback(context.Background(), ReadbackInput{
		OperationID: "readback-revised-" + string(fixture.turnID), DelegationID: fixture.delegationID, Revision: 2,
		Assignee: fixture.assignee, Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		OccurredAt: fixture.input.OccurredAt.Add(2 * time.Minute),
	})
	if err != nil || !readbackResult.TaskStartQueued || readbackResult.Status != conversation.ReadbackStatusAccepted {
		t.Fatalf("AcceptReadback(revision 2): acceptance=%#v err=%v", readbackResult, err)
	}
}

func TestCommitClarificationDecisionReplayAndStaleRevisionFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clarification-decision.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which format should I use?",
	})
	first, err := store.CommitClarificationDecision(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(first): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// A retried semantic decision can arrive after a worker restart with newly
	// allocated output IDs and a new wall clock. The operation receipt owns the
	// canonical IDs and must replay those exact committed rows.
	retry := input
	retry.EventID = modulecore.NewEventID()
	retry.MessageID = modulecore.NewMessageID()
	retry.ReceiptEventID = modulecore.NewEventID()
	retry.OccurredAt = input.OccurredAt.Add(time.Hour)
	replayed, err := store.CommitClarificationDecision(context.Background(), retry)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("CommitClarificationDecision(replay) = %#v, err=%v; want original %#v", replayed, err, first)
	}
	changedReceipt := retry
	changedReceipt.InvocationReceipt.TokensUsed++
	if _, err := store.CommitClarificationDecision(context.Background(), changedReceipt); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("CommitClarificationDecision(same operation, changed invocation receipt): %v, want ErrOperationConflict", err)
	}
	loaded, found, err := store.LoadClarificationDecisionOutcome(context.Background(), input.ControlTurnID)
	if err != nil || !found || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("LoadClarificationDecisionOutcome() = %#v, found=%t err=%v; want original receipt", loaded, found, err)
	}
	stale := input
	stale.OperationID += "-stale"
	stale.Revision++
	if _, err := store.CommitClarificationDecision(context.Background(), stale); !errors.Is(err, ErrClarificationConflict) {
		t.Fatalf("CommitClarificationDecision(stale): %v, want ErrClarificationConflict", err)
	}
}

func TestLoadClarificationContinuationOutcomeAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clarification-continuation.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
		Instruction: "Continue from the canonical original request and retain every constraint.",
	})
	input.ContextEventID = modulecore.NewEventID()
	committed, err := store.CommitClarificationDecision(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(continue): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	loaded, found, err := store.LoadClarificationDecisionOutcome(context.Background(), input.ControlTurnID)
	if err != nil || !found || !reflect.DeepEqual(loaded, committed) {
		t.Fatalf("LoadClarificationDecisionOutcome(continue) = %#v, found=%t err=%v; want exact cross-Turn result %#v", loaded, found, err, committed)
	}
	if loaded.Event.TurnID != fixture.turnID || loaded.ControlTurnID == loaded.Event.TurnID || len(loaded.Events) != 3 ||
		loaded.Events[0].EventType != "conversation.delegation_context" || loaded.Events[1].EventType != "conversation.delegation_instruction" ||
		loaded.Events[2].EventType != "conversation.mio_emission_receipt" || loaded.ReceiptEvent.TurnID != loaded.ControlTurnID {
		t.Fatalf("recovered continuation outcome lost original Turn binding/full event bundle: %#v", loaded)
	}
}

func TestCommitClarificationContinuationRollsBackAllRowsWhenDispatchCannotCommit(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
		Instruction: "Use the original request and preserve the scope.",
	})
	input.ContextEventID = modulecore.NewEventID()
	if _, err := store.db.Exec(`CREATE TRIGGER reject_clarification_dispatch
		BEFORE INSERT ON live_work_outbox WHEN NEW.kind='dispatch_delegation'
		BEGIN SELECT RAISE(ABORT, 'injected clarification dispatch failure'); END`); err != nil {
		t.Fatalf("install dispatch failure trigger: %v", err)
	}
	if _, err := store.CommitClarificationDecision(context.Background(), input); err == nil {
		t.Fatal("CommitClarificationDecision() error = nil, want injected dispatch failure")
	}
	var eventCount, indexCount, revisionCount, receiptCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id IN (?,?,?)`, []any{string(input.EventID), string(input.ContextEventID), string(input.ReceiptEventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_message_index WHERE message_id=?`, []any{string(input.MessageID)}, &indexCount},
		{`SELECT COUNT(*) FROM live_delegation WHERE delegation_id=?`, []any{string(fixture.delegationID)}, &revisionCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=?`, []any{input.OperationID}, &receiptCount},
	} {
		if err := store.db.QueryRow(check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("read clarification rollback state: %v", err)
		}
	}
	var controlState string
	if err := store.db.QueryRow(`SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(input.ControlTurnID)).Scan(&controlState); err != nil {
		t.Fatalf("read control Turn after rollback: %v", err)
	}
	if eventCount != 0 || indexCount != 0 || revisionCount != 1 || receiptCount != 0 || controlState != "accepted" {
		t.Fatalf("rollback left events:%d index:%d revisions:%d receipt:%d control:%q", eventCount, indexCount, revisionCount, receiptCount, controlState)
	}
}

func TestClarificationContinuationStopsAtConfiguredRevisionLimit(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(revision 1): %v", err)
	}
	contextView, err := store.LoadClarificationControlContext(context.Background(), ClarificationControlContextInput{
		SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID, OwnerID: "user-1", Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(revision 1): %v", err)
	}
	handoff := contextView.Handoff
	for revision := uint64(1); revision <= conversation.MaxClarificationRevision; revision++ {
		if revision > 1 {
			readback := conversation.ReadbackEnvelope{
				DelegationID: fixture.delegationID, Revision: revision, Sender: fixture.assignee, SchemaVersion: "agent.readback.v1",
				Status: conversation.ReadbackStatusNeedsClarification, UnderstoodGoal: handoff.Goal,
				ExpectedOutput: handoff.ExpectedOutput, Text: "One required detail is still missing.",
			}
			accepted, err = store.AcceptReadback(context.Background(), ReadbackInput{
				OperationID:  "readback-limit-" + string(fixture.delegationID) + "-" + fmt.Sprint(revision),
				DelegationID: fixture.delegationID, Revision: revision, Assignee: fixture.assignee, Readback: readback,
				EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.input.OccurredAt.Add(time.Duration(revision) * time.Minute),
			})
			if err != nil {
				t.Fatalf("AcceptReadback(revision %d): %v", revision, err)
			}
			contextView, err = store.LoadClarificationControlContext(context.Background(), ClarificationControlContextInput{
				SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID, OwnerID: "user-1", Assignee: fixture.assignee,
			})
			if err != nil {
				t.Fatalf("LoadClarificationControlContext(revision %d): %v", revision, err)
			}
			handoff = contextView.Handoff
		}
		decision := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
			SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
			Instruction: "Continue with the original constraints at revision " + fmt.Sprint(revision) + ".",
		})
		decision.Revision = revision
		decision.OperationID += "-" + fmt.Sprint(revision)
		decision.ContextEventID = modulecore.NewEventID()
		if revision == conversation.MaxClarificationRevision {
			if _, err := store.CommitClarificationDecision(context.Background(), decision); !errors.Is(err, ErrClarificationRevisionLimit) {
				t.Fatalf("CommitClarificationDecision(revision limit): %v, want ErrClarificationRevisionLimit", err)
			}
			terminalInput := clarificationTerminalInput(fixture, accepted.ControlTurn.TurnID,
				conversation.ClarificationTerminalRevisionLimit, conversation.ClarificationDecisionContinueDelegation, "completed")
			terminalInput.Revision = revision
			terminalInput.OperationID = decision.OperationID
			terminal, err := store.CommitClarificationTerminal(context.Background(), terminalInput)
			if err != nil {
				t.Fatalf("CommitClarificationTerminal(revision limit): %v", err)
			}
			if terminal.TerminalReason != conversation.ClarificationTerminalRevisionLimit || terminal.Kind != "" ||
				terminal.ResultState != "completed" || terminal.DispatchQueued || terminal.NextHandoff != nil {
				t.Fatalf("revision-limit terminal = %#v", terminal)
			}
			if _, found, err := store.LoadClarificationDecisionOutcome(context.Background(), accepted.ControlTurn.TurnID); err != nil || !found {
				t.Fatalf("LoadClarificationDecisionOutcome(revision limit): found=%t err=%v", found, err)
			}
			break
		}
		continued, err := store.CommitClarificationDecision(context.Background(), decision)
		if err != nil {
			t.Fatalf("CommitClarificationDecision(revision %d): %v", revision, err)
		}
		handoff = *continued.NextHandoff
	}
	var revisionCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_delegation WHERE delegation_id=?`, string(fixture.delegationID)).Scan(&revisionCount); err != nil {
		t.Fatalf("count bounded revisions: %v", err)
	}
	if revisionCount != int(conversation.MaxClarificationRevision) {
		t.Fatalf("delegation revision rows = %d, want exactly %d", revisionCount, conversation.MaxClarificationRevision)
	}
}

func TestCommitClarificationTerminalPersistsSafeBundleAndReplaysAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clarification-terminal.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationTerminalInput(fixture, accepted.ControlTurn.TurnID,
		conversation.ClarificationTerminalInvalidOutput, "", "invalid_output")
	first, err := store.CommitClarificationTerminal(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationTerminal(): %v", err)
	}
	if first.OperationID != input.OperationID || first.SessionID != fixture.sessionID || first.ControlTurnID != accepted.ControlTurn.TurnID ||
		first.DelegationID != fixture.delegationID || first.Revision != fixture.revision ||
		first.TerminalReason != conversation.ClarificationTerminalInvalidOutput || first.Kind != "" ||
		first.ResultState != "completed" || first.DispatchQueued || first.NextHandoff != nil || len(first.Events) != 2 || len(first.MessageIndexes) != 1 {
		t.Fatalf("terminal result = %#v", first)
	}
	if first.Event.EventType != "agent.response" || first.Event.ComponentID != "orchestrator" ||
		first.Event.TurnID != accepted.ControlTurn.TurnID || first.Event.RequestID != "" ||
		payloadString(first.Event.Payload, "kind") != "turn_error" || payloadString(first.Event.Payload, "availability") != "free" ||
		payloadString(first.Event.Payload, "visibility") != "public" || payloadString(first.Event.Payload, "delegation_id") != string(fixture.delegationID) ||
		payloadUint64Field(first.Event.Payload, "delegation_revision") != fixture.revision ||
		payloadString(first.Event.Payload, "text") != conversation.ClarificationTerminalUserText ||
		payloadString(first.Event.Payload, "reply_to_message_id") != payloadString(accepted.Event.Payload, "reply_to_message_id") {
		t.Fatalf("terminal public event = %#v", first.Event)
	}
	if first.Message.Visibility != "public" || first.Message.Kind != "turn_error" || first.Message.ToKind != "user" ||
		first.Message.ToActor != "user-1" || first.Message.TurnID != accepted.ControlTurn.TurnID ||
		!reflect.DeepEqual(first.Events[0], first.Event) || !reflect.DeepEqual(first.Events[1], first.ReceiptEvent) ||
		first.ReceiptEvent.EventType != "conversation.mio_emission_receipt" || first.ReceiptEvent.ComponentID != "conversation_runtime" ||
		first.ReceiptEvent.TurnID != accepted.ControlTurn.TurnID || payloadString(first.ReceiptEvent.Payload, "status") != "invalid_output" {
		t.Fatalf("terminal message/event bundle = %#v / %#v", first.Message, first.Events)
	}
	var turnState, delegationState string
	if err := store.db.QueryRow(`SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(accepted.ControlTurn.TurnID)).Scan(&turnState); err != nil {
		t.Fatalf("read terminal control Turn state: %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil {
		t.Fatalf("read unresolved delegation state: %v", err)
	}
	var taskCount, dispatchCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='start_delegated_task'`).Scan(&taskCount); err != nil {
		t.Fatalf("count work after clarification terminal: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='dispatch_delegation'`).Scan(&dispatchCount); err != nil {
		t.Fatalf("count dispatch work after clarification terminal: %v", err)
	}
	if turnState != "completed" || delegationState != "needs_attention" || taskCount != 0 || dispatchCount != 1 {
		t.Fatalf("terminal side effects: turn=%q delegation=%q task_work=%d dispatch_work=%d", turnState, delegationState, taskCount, dispatchCount)
	}
	loaded, found, err := store.LoadClarificationDecisionOutcome(context.Background(), accepted.ControlTurn.TurnID)
	if err != nil || !found || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("LoadClarificationDecisionOutcome(): result=%#v found=%t err=%v; want exact first result", loaded, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(before restart): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	afterRestart, found, err := store.LoadClarificationDecisionOutcome(context.Background(), accepted.ControlTurn.TurnID)
	if err != nil || !found || !reflect.DeepEqual(afterRestart, first) {
		t.Fatalf("LoadClarificationDecisionOutcome(after restart): result=%#v found=%t err=%v; want exact terminal outcome", afterRestart, found, err)
	}
	replayed, err := store.CommitClarificationTerminal(context.Background(), input)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("CommitClarificationTerminal(replay): result=%#v err=%v; want exact first result", replayed, err)
	}
	input.Reason = conversation.ClarificationTerminalGenerationError
	input.InvocationReceipt.Status = "generation_failed"
	if _, err := store.CommitClarificationTerminal(context.Background(), input); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("CommitClarificationTerminal(changed retry): %v, want ErrOperationConflict", err)
	}
}

func TestCommitClarificationTerminalGenerationErrorAndInvalidMappings(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	bad := clarificationTerminalInput(fixture, accepted.ControlTurn.TurnID,
		conversation.ClarificationTerminalGenerationError, "", "invalid_output")
	if _, err := store.CommitClarificationTerminal(context.Background(), bad); err == nil {
		t.Fatal("CommitClarificationTerminal(mismatched reason/receipt): error=nil, want validation failure")
	}
	good := clarificationTerminalInput(fixture, accepted.ControlTurn.TurnID,
		conversation.ClarificationTerminalGenerationError, "", "generation_failed")
	committed, err := store.CommitClarificationTerminal(context.Background(), good)
	if err != nil {
		t.Fatalf("CommitClarificationTerminal(generation error): %v", err)
	}
	if committed.TerminalReason != conversation.ClarificationTerminalGenerationError ||
		payloadString(committed.ReceiptEvent.Payload, "status") != "generation_failed" ||
		payloadString(committed.Event.Payload, "text") != conversation.ClarificationTerminalUserText {
		t.Fatalf("generation-error terminal exposed or lost its safe outcome: %#v", committed)
	}
}

func TestCommitClarificationTerminalRollsBackAllWritesOnReceiptFailure(t *testing.T) {
	store := newTestStore(t)
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	input := clarificationTerminalInput(fixture, accepted.ControlTurn.TurnID,
		conversation.ClarificationTerminalInvalidOutput, "", "invalid_output")
	if _, err := store.db.Exec(`CREATE TRIGGER reject_clarification_terminal_receipt
		BEFORE INSERT ON live_operation_receipt
		BEGIN SELECT RAISE(ABORT, 'injected clarification terminal receipt failure'); END`); err != nil {
		t.Fatalf("install receipt failure trigger: %v", err)
	}
	if _, err := store.CommitClarificationTerminal(context.Background(), input); err == nil {
		t.Fatal("CommitClarificationTerminal() error=nil, want injected receipt failure")
	}
	var turnState, resultReceiptID, delegationState string
	var eventCount, indexCount, operationCount int
	if err := store.db.QueryRow(`SELECT state,COALESCE(result_receipt_id,'') FROM live_conversation_turn WHERE turn_id=?`, string(accepted.ControlTurn.TurnID)).Scan(&turnState, &resultReceiptID); err != nil {
		t.Fatalf("read control Turn after rollback: %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_delegation WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), fixture.revision).Scan(&delegationState); err != nil {
		t.Fatalf("read delegation after terminal rollback: %v", err)
	}
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id IN (?,?)`, []any{string(input.EventID), string(input.ReceiptEventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_message_index WHERE message_id=?`, []any{string(input.MessageID)}, &indexCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=?`, []any{input.OperationID}, &operationCount},
	} {
		if err := store.db.QueryRow(check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("read terminal rollback state: %v", err)
		}
	}
	if turnState != "accepted" || resultReceiptID != "" || delegationState != string(conversation.ReadbackStatusNeedsClarification) ||
		eventCount != 0 || indexCount != 0 || operationCount != 0 {
		t.Fatalf("terminal rollback state: turn=%q result=%q delegation=%q events=%d indexes=%d receipt=%d", turnState, resultReceiptID, delegationState, eventCount, indexCount, operationCount)
	}
}

func clarificationTerminalInput(fixture readbackFixture, controlTurnID modulecore.TurnID, reason conversation.ClarificationTerminalReason, attemptedKind conversation.ClarificationDecisionKind, receiptStatus string) ClarificationTerminalInput {
	return ClarificationTerminalInput{
		OperationID: "clarification.decision:" + string(controlTurnID), SessionID: fixture.sessionID,
		ControlTurnID: controlTurnID, DelegationID: fixture.delegationID, Revision: fixture.revision,
		GenerationEpoch: 1, Reason: reason, AttemptedKind: attemptedKind,
		InvocationReceipt: agent.InvocationReceipt{
			RequestID: "mio-request-" + string(controlTurnID), ResponseID: "mio-response-" + string(controlTurnID),
			ProviderLabel: "mio-local", TokensUsed: 17, Status: receiptStatus,
		},
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(),
		OccurredAt: fixture.input.OccurredAt.Add(time.Second),
	}
}

func clarificationDecisionInput(fixture readbackFixture, controlTurnID modulecore.TurnID, decision conversation.ClarificationDecision) ClarificationDecisionInput {
	return ClarificationDecisionInput{
		OperationID: "clarification-decision:" + string(controlTurnID), SessionID: fixture.sessionID,
		ControlTurnID: controlTurnID, DelegationID: fixture.delegationID, Revision: fixture.revision,
		GenerationEpoch: 1, Decision: decision,
		InvocationReceipt: agent.InvocationReceipt{
			RequestID: "mio-request-" + string(controlTurnID), ResponseID: "mio-response-" + string(controlTurnID),
			ProviderLabel: "mio-local", TokensUsed: 17, Status: "completed",
		},
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(),
		OccurredAt: fixture.input.OccurredAt.Add(time.Second),
	}
}
