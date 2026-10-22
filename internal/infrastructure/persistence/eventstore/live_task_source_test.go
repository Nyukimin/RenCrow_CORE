package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLoadDelegationTaskRequiresCurrentLeaseAcceptedReadbackAndPublicContext(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
		UnderstoodGoal:             fixture.handoff.Goal,
		AcknowledgedConstraintKeys: []string{"keep_scope"},
		ExpectedOutput:             fixture.handoff.ExpectedOutput,
		Text:                       "I understand the assignment and will report within its scope.",
	}
	accepted, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID:  "task-source-readback-" + string(fixture.turnID),
		DelegationID: fixture.delegationID, Revision: 1, Assignee: fixture.assignee,
		Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		OccurredAt: fixture.now,
	})
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(start): claims=%#v err=%v", claims, err)
	}
	lease := claims[0]
	if lease.Assignee != fixture.assignee {
		t.Fatalf("task-start lease assignee=%q, want %q", lease.Assignee, fixture.assignee)
	}
	input := DelegationTaskSourceInput{
		WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(time.Second)},
		SessionID:      fixture.sessionID, Assignee: fixture.assignee,
	}
	source, err := store.LoadDelegationTask(ctx, input)
	if err != nil {
		t.Fatalf("LoadDelegationTask(): %v", err)
	}
	if source.Work.WorkID != lease.WorkID || source.OwnerID != "user-1" || !reflect.DeepEqual(source.Handoff, fixture.handoff) ||
		source.Readback.Status != conversation.ReadbackStatusAccepted || source.Readback.Sender != fixture.assignee ||
		len(source.Context) != 1 || source.Context[0].Role != "user" || source.Context[0].MessageID != fixture.inputMessageID ||
		source.Context[0].Text != "please delegate this task" || source.Work.SourceEventID != accepted.Event.EventID {
		t.Fatalf("loaded Task source=%#v, want accepted handoff/readback and one authorized public context message", source)
	}

	wrongActor := input
	wrongActor.Assignee = "kuro"
	if _, err := store.LoadDelegationTask(ctx, wrongActor); !errors.Is(err, ErrDelegationWorkConflict) {
		t.Fatalf("LoadDelegationTask(wrong actor): %v, want ErrDelegationWorkConflict", err)
	}
	expired := input
	expired.WorkLeaseInput.Now = fixture.now.Add(time.Minute + time.Second)
	if _, err := store.LoadDelegationTask(ctx, expired); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadDelegationTask(expired lease): %v, want ErrWorkConflict", err)
	}
	if _, err := store.LoadDelegationTask(ctx, DelegationTaskSourceInput{
		WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(time.Minute + 2*time.Second)},
		SessionID:      fixture.sessionID, Assignee: fixture.assignee,
	}); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadDelegationTask(at expiry): %v, want ErrWorkConflict", err)
	}
}

func TestLoadDelegationTaskAcceptsExplicitEmptyConstraintAcknowledgements(t *testing.T) {
	store := newTestStore(t)
	fixture, accepted, input := acceptTaskSourceReadbackWithoutConstraints(t, store)

	source, err := store.LoadDelegationTask(context.Background(), input)
	if err != nil {
		t.Fatalf("LoadDelegationTask() for empty constraints: %v", err)
	}
	if source.Work.SourceEventID != accepted.Event.EventID || source.Handoff.DelegationID != fixture.delegationID ||
		source.Readback.AcknowledgedConstraintKeys == nil || len(source.Readback.AcknowledgedConstraintKeys) != 0 {
		t.Fatalf("loaded empty-constraint source=%#v; want accepted event, canonical handoff, and explicit empty acknowledgements", source)
	}
	encoded, err := json.Marshal(accepted.Event.Payload["acknowledged_constraint_keys"])
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("stored acknowledgement payload=%s err=%v, want []", encoded, err)
	}
}

func TestPayloadStringSliceRejectsMissingOrNullConstraintAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "missing"},
		{name: "null", value: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{}
			if test.name != "missing" {
				payload["acknowledged_constraint_keys"] = test.value
			}
			if _, ok := payloadStringSlice(payload["acknowledged_constraint_keys"]); ok {
				t.Fatalf("payloadStringSlice(%s) accepted a missing/null value", test.name)
			}
		})
	}
}

func acceptTaskSourceReadbackWithoutConstraints(t *testing.T, store *SQLiteStore) (dispatchFixture, ReadbackAcceptance, DelegationTaskSourceInput) {
	t.Helper()
	ctx := context.Background()
	fixture := newEmptyConstraintDispatchWorkFixture(t, store)

	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
		UnderstoodGoal: fixture.handoff.Goal, AcknowledgedConstraintKeys: []string{},
		ExpectedOutput: fixture.handoff.ExpectedOutput,
		Text:           "I understand and accept the assignment.",
	}
	accepted, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID:  "task-source-empty-readback-" + string(fixture.turnID),
		DelegationID: fixture.delegationID, Revision: 1, Assignee: fixture.assignee,
		Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		OccurredAt: fixture.now,
	})
	if err != nil {
		t.Fatalf("AcceptReadback(empty constraints): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(start): claims=%#v err=%v", claims, err)
	}
	lease := claims[0]
	input := DelegationTaskSourceInput{
		WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(time.Second)},
		SessionID:      fixture.sessionID, Assignee: fixture.assignee,
	}
	return fixture, accepted, input
}

func newEmptyConstraintDispatchWorkFixture(t *testing.T, store *SQLiteStore) dispatchFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "please delegate this task")
	inputEvent.OccurredAt = now
	if _, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-empty-constraint-task-" + string(turnID), OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-empty-constraint-" + string(turnID), ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
	}); err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	delegationID, assignee := modulecore.NewRequestID(), "shiro"
	instructionMessageID := modulecore.NewMessageID()
	contextMessages := []DelegationContextMessage{{MessageID: inputEvent.MessageID, Role: "user", Text: "please delegate this task"}}
	contextMaterial := struct {
		SourceRevision  string                     `json:"source_revision"`
		CutoverRevision string                     `json:"cutover_revision"`
		Messages        []DelegationContextMessage `json:"messages"`
	}{SourceRevision: "1", CutoverRevision: "0", Messages: contextMessages}
	encoded, err := json.Marshal(contextMaterial)
	if err != nil {
		t.Fatalf("marshal context hash material: %v", err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: turnID, OriginUserMessageID: inputEvent.MessageID,
		SessionID: sessionID, SourceTraceID: inputEvent.TraceID, FromActor: "mio", ToActor: assignee,
		InstructionMessageID: instructionMessageID, ContextSnapshotHash: hash, AllowedCapabilities: []string{}, ReplyTo: inputEvent.MessageID,
		Goal: "Summarize the task", Instruction: "Inspect and report.", Constraints: []conversation.AgentConstraint{},
		ExpectedOutput: "A concise report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	notice := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	notice.SessionID, notice.TurnID, notice.MessageID, notice.CausationEventID = sessionID, turnID, modulecore.NewMessageID(), inputEvent.EventID
	notice.OccurredAt = now
	notice.Payload = map[string]any{"kind": "delegation_notice", "from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public", "content": "I delegated this task.", "text": "I delegated this task."}
	contextEvent := eventFixture(inputEvent.TraceID, "conversation.delegation_context", "conversation_runtime")
	contextEvent.SessionID, contextEvent.TurnID, contextEvent.CausationEventID = sessionID, turnID, inputEvent.EventID
	contextEvent.OccurredAt = now
	contextEvent.Payload = map[string]any{"source_revision": "1", "cutover_revision": "0", "messages": contextMessages, "context_snapshot_hash": hash, "handoff": handoff}
	instruction := eventFixture(inputEvent.TraceID, "conversation.delegation_instruction", "delegation_mailbox")
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, turnID, instructionMessageID
	instruction.CausationEventID, instruction.ActorKind, instruction.ActorID = notice.EventID, "agent", "mio"
	instruction.OccurredAt = now
	instruction.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": assignee, "text": handoff.Instruction}
	outbox := []WorkItem{{WorkID: "dispatch:" + string(turnID), DedupeKey: "delegation:" + string(delegationID) + ":1:start", Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: sessionID, Priority: 5}}
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-empty-constraint-task-" + string(turnID), SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, contextEvent, instruction},
		MessageIndexes: []MessageIndex{
			{MessageID: notice.MessageID, EventID: notice.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "delegation_notice", Visibility: "public"},
			{MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "agent", ToActor: assignee, Kind: "delegation_instruction", Visibility: "private"},
		},
		Delegation: &DelegationRecord{DelegationID: string(delegationID), Revision: 1, OriginTurnID: turnID, Assignee: assignee,
			InstructionMessageID: instruction.MessageID, ContextEventID: contextEvent.EventID, State: "dispatch_accepted"},
		Outbox: outbox,
	}); err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	return dispatchFixture{now: now, sessionID: sessionID, turnID: turnID, delegationID: delegationID,
		assignee: assignee, inputMessageID: inputEvent.MessageID, contextEventID: contextEvent.EventID,
		instructionEventID: instruction.EventID, handoff: handoff}
}

func TestLoadDelegationTaskFailsClosedForStaleCanceledOrPrivateReadback(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, dispatchFixture, ReadbackAcceptance)
	}{
		{name: "stale revision", mutate: func(t *testing.T, store *SQLiteStore, fixture dispatchFixture, _ ReadbackAcceptance) {
			_, err := store.db.Exec(`INSERT INTO live_delegation (delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision) SELECT delegation_id,revision+1,origin_turn_id,assignee,instruction_message_id,context_event_id,'dispatch_accepted',NULL,0 FROM live_delegation WHERE delegation_id=? AND revision=1`, string(fixture.delegationID))
			if err != nil {
				t.Fatalf("insert newer revision: %v", err)
			}
		}},
		{name: "canceled", mutate: func(t *testing.T, store *SQLiteStore, fixture dispatchFixture, _ ReadbackAcceptance) {
			if _, err := store.db.Exec(`UPDATE live_delegation SET state='canceled' WHERE delegation_id=? AND revision=1`, string(fixture.delegationID)); err != nil {
				t.Fatalf("cancel delegation: %v", err)
			}
		}},
		{name: "private readback projection", mutate: func(t *testing.T, store *SQLiteStore, _ dispatchFixture, accepted ReadbackAcceptance) {
			if _, err := store.db.Exec(`UPDATE live_message_index SET visibility='private' WHERE event_id=?`, string(accepted.Event.EventID)); err != nil {
				t.Fatalf("hide readback index: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			fixture := newDispatchWorkFixture(t, store, false, false)
			readback := conversation.ReadbackEnvelope{
				DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
				SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
				UnderstoodGoal: fixture.handoff.Goal, AcknowledgedConstraintKeys: []string{"keep_scope"},
				ExpectedOutput: fixture.handoff.ExpectedOutput, Text: "I understand and accept the assignment.",
			}
			accepted, err := store.AcceptReadback(ctx, ReadbackInput{
				OperationID: "task-source-readback-" + string(fixture.turnID), DelegationID: fixture.delegationID,
				Revision: 1, Assignee: fixture.assignee, Readback: readback,
				EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.now,
			})
			if err != nil {
				t.Fatalf("AcceptReadback(): %v", err)
			}
			claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second)})
			if err != nil || len(claims) != 1 {
				t.Fatalf("ClaimWork(start): claims=%#v err=%v", claims, err)
			}
			test.mutate(t, store, fixture, accepted)
			lease := claims[0]
			_, err = store.LoadDelegationTask(ctx, DelegationTaskSourceInput{
				WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(time.Second)},
				SessionID:      fixture.sessionID, Assignee: fixture.assignee,
			})
			if !errors.Is(err, ErrDelegationWorkConflict) {
				t.Fatalf("LoadDelegationTask(): %v, want ErrDelegationWorkConflict", err)
			}
		})
	}
}

func TestBindDelegationTaskEnqueuesExecuteWorkExactlyOnce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
		UnderstoodGoal: fixture.handoff.Goal, AcknowledgedConstraintKeys: []string{"keep_scope"},
		ExpectedOutput: fixture.handoff.ExpectedOutput, Text: "I understand and accept the assignment.",
	}
	accepted, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID: "task-bind-readback-" + string(fixture.turnID), DelegationID: fixture.delegationID,
		Revision: 1, Assignee: fixture.assignee, Readback: readback,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.now,
	})
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second)})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(start): claims=%#v err=%v", claims, err)
	}
	lease := claims[0]
	bind := BindDelegationTaskInput{
		DelegationID: fixture.delegationID, Revision: 1, TaskID: modulecore.NewTaskID(),
		TaskOperationID: "delegation:" + string(fixture.delegationID) + ":1:start", WorkID: lease.WorkID,
		LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(time.Second),
	}
	if err := store.BindDelegationTask(ctx, bind); err != nil {
		t.Fatalf("BindDelegationTask(): %v", err)
	}
	if err := store.BindDelegationTask(ctx, bind); err != nil {
		t.Fatalf("BindDelegationTask(replay): %v", err)
	}
	var count, stateValid int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='execute_delegated_task' AND dedupe_key=?`, delegationExecuteDedupeKey(fixture.delegationID, 1)).Scan(&count); err != nil {
		t.Fatalf("count execute work: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_work_outbox WHERE kind='execute_delegated_task' AND dedupe_key=? AND work_id=? AND source_event_id=? AND session_id=? AND state='pending'`,
		delegationExecuteDedupeKey(fixture.delegationID, 1), delegationExecuteWorkID(fixture.delegationID, 1), string(accepted.Event.EventID), string(fixture.sessionID)).Scan(&stateValid); err != nil {
		t.Fatalf("validate execute work: %v", err)
	}
	if count != 1 || stateValid != 1 {
		t.Fatalf("execute outbox count=%d valid rows=%d, want one pending item sourced from the accepted readback", count, stateValid)
	}
	claims, err = store.ClaimWork(ctx, WorkClaimInput{Kind: "execute_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(2 * time.Second)})
	if err != nil || len(claims) != 1 || claims[0].SourceEventID != accepted.Event.EventID {
		t.Fatalf("ClaimWork(execute): claims=%#v err=%v, want one item behind accepted readback", claims, err)
	}
}
