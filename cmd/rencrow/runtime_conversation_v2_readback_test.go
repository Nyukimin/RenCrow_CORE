package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResponseFirstReadbackWiringExcludesKuroBeforeGeneration(t *testing.T) {
	shiro := &readbackWiringProvider{name: "shiro"}
	midori := &readbackWiringProvider{name: "midori"}
	kuro := &readbackWiringProvider{name: "kuro-danger-route"}
	wiring := responseFirstReadbackWiringFor(&config.Config{Prompts: &config.LoadedPrompts{
		Worker: "Shiro trusted persona", Wild: "Midori trusted persona",
		StableRuntimeContexts: map[string]string{"shiro": "Shiro runtime", "midori": "Midori runtime"},
	}}, map[string]agent.ConversationGenerationProvider{
		"shiro": shiro, "midori": midori, "kuro": kuro,
	})

	if got, want := responseFirstAssigneeIDs(filterResponseFirstReadbackAssignees(wiring.Assignees)), []string{"shiro", "midori"}; !equalStrings(got, want) {
		t.Fatalf("readback assignees = %v, want %v", got, want)
	}
	if _, ok := wiring.Assignees["kuro"]; ok {
		t.Fatal("Kuro was added to the readback generator configuration")
	}
	if kuro.calls != 0 {
		t.Fatalf("Kuro provider calls = %d, want zero", kuro.calls)
	}
	if wiring.Assignees["shiro"].StableRuntimeContext != "Shiro runtime" || wiring.Assignees["midori"].StableRuntimeContext != "Midori runtime" {
		t.Fatalf("trusted stable runtime context was not preserved: %#v", wiring.Assignees)
	}
}

func TestResponseFirstDelegationDispatchPublishesOriginBeforeGeneration(t *testing.T) {
	store, handoff, _, now := newReadbackWiringEventStore(t)
	store.outcome = responseFirstDelegationNoticeBatch(handoff, now)
	publisher := &readbackWiringPublisher{failFirst: true}
	generator := &readbackWiringGenerator{}
	dispatchStore := responseFirstDelegationDispatchStore{
		store: store, outcomes: readbackWiringOutcomes{store: store}, publisher: publisher,
	}
	dispatcher, err := conversationruntime.NewDelegationDispatcher(conversationruntime.DelegationDispatcherDependencies{
		Store: dispatchStore, Generator: generator, Publisher: publisher,
		Config: conversationruntime.DelegationDispatcherConfig{
			PolicyVersion: responseFirstPolicyVer, LeaseDuration: time.Minute, Now: func() time.Time { return now },
		},
	})
	if err != nil {
		t.Fatalf("NewDelegationDispatcher(): %v", err)
	}

	if _, err := dispatcher.DispatchOne(context.Background()); err == nil {
		t.Fatal("DispatchOne() error = nil, want origin publisher failure")
	}
	if generator.calls != 0 {
		t.Fatalf("readback generation calls after origin publication failure = %d, want zero", generator.calls)
	}
	if store.retries != 1 || store.acks != 0 || store.accepts != 0 {
		t.Fatalf("work state after origin publication failure: retries=%d acks=%d accepts=%d, want retry only", store.retries, store.acks, store.accepts)
	}
	if store.retryInput.WorkID != store.lease.WorkID || store.retryInput.LeaseToken != store.lease.LeaseToken || store.retryInput.Version != store.lease.Version {
		t.Fatalf("EventStore retry input = %+v, want claimed fencing tuple", store.retryInput.WorkLeaseInput)
	}
	if len(publisher.turns) != 1 || publisher.turns[0] != handoff.OriginTurnID {
		t.Fatalf("published turns before generation failure = %v, want only origin turn %s", publisher.turns, handoff.OriginTurnID)
	}
}

func TestResponseFirstDelegationDispatchAdaptsAcceptanceAndAcknowledgement(t *testing.T) {
	store, handoff, _, now := newReadbackWiringEventStore(t)
	store.outcome = responseFirstDelegationNoticeBatch(handoff, now)
	publisher := &readbackWiringPublisher{}
	generator := &readbackWiringGenerator{result: conversation.ReadbackEnvelope{
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
		UnderstoodGoal: handoff.Goal, AcknowledgedConstraintKeys: []string{"keep_scope"},
		ExpectedOutput: handoff.ExpectedOutput, Text: "I understand the assignment.",
	}}
	dispatcher, err := conversationruntime.NewDelegationDispatcher(conversationruntime.DelegationDispatcherDependencies{
		Store:     responseFirstDelegationDispatchStore{store: store, outcomes: readbackWiringOutcomes{store: store}, publisher: publisher},
		Generator: generator, Publisher: publisher,
		Config: conversationruntime.DelegationDispatcherConfig{
			PolicyVersion: responseFirstPolicyVer, LeaseDuration: time.Minute, Now: func() time.Time { return now },
		},
	})
	if err != nil {
		t.Fatalf("NewDelegationDispatcher(): %v", err)
	}

	result, err := dispatcher.DispatchOne(context.Background())
	if err != nil {
		t.Fatalf("DispatchOne(): %v", err)
	}
	if !result.Claimed || !result.Accepted || !result.Published || !result.Acknowledged {
		t.Fatalf("DispatchOne() result = %+v, want fully completed readback", result)
	}
	if generator.calls != 1 || store.accepts != 1 || store.acks != 1 || store.retries != 0 {
		t.Fatalf("dispatch counts generator=%d accept=%d ack=%d retry=%d, want 1/1/1/0", generator.calls, store.accepts, store.acks, store.retries)
	}
	if store.acceptedInput == nil || store.acceptedInput.Assignee != "shiro" || store.acceptedInput.DelegationID != handoff.DelegationID || store.acceptedInput.Revision != handoff.Revision || store.acceptedInput.Readback.Sender != "shiro" {
		t.Fatalf("EventStore acceptance input = %#v, want trusted assignee and delegation metadata", store.acceptedInput)
	}
	if store.claim.Kind != conversationruntime.DispatchDelegationWorkKind || store.claim.Limit != 1 || store.claim.LeaseDuration != time.Minute ||
		store.load.WorkID != store.lease.WorkID || store.load.LeaseToken != store.lease.LeaseToken || store.load.Version != store.lease.Version {
		t.Fatalf("EventStore claim/load inputs = %+v / %+v, want one fenced delegation lease", store.claim, store.load)
	}
	if store.ackInput.WorkID != store.lease.WorkID || store.ackInput.LeaseToken != store.lease.LeaseToken || store.ackInput.Version != store.lease.Version {
		t.Fatalf("EventStore ack input = %+v, want claimed fencing tuple", store.ackInput)
	}
	if len(publisher.turns) != 2 || publisher.turns[0] != handoff.OriginTurnID || publisher.turns[1] != handoff.OriginTurnID {
		t.Fatalf("publication order = %v, want origin notice before public readback", publisher.turns)
	}
}

func TestResponseFirstDelegationDispatchAdaptsExpiredReadbackFailure(t *testing.T) {
	store, handoff, _, now := newReadbackWiringEventStore(t)
	store.acceptedAt = now.Add(-31 * time.Second)
	store.readbackDeadline = store.acceptedAt.Add(responseFirstReadbackTimeout)
	store.outcome = responseFirstDelegationNoticeBatch(handoff, now)
	publisher := &readbackWiringPublisher{}
	generator := &readbackWiringGenerator{}
	dispatcher, err := conversationruntime.NewDelegationDispatcher(conversationruntime.DelegationDispatcherDependencies{
		Store:     responseFirstDelegationDispatchStore{store: store, outcomes: readbackWiringOutcomes{store: store}, publisher: publisher},
		Generator: generator, Publisher: publisher,
		Config: conversationruntime.DelegationDispatcherConfig{
			PolicyVersion: responseFirstPolicyVer, LeaseDuration: time.Minute, Now: func() time.Time { return now },
		},
	})
	if err != nil {
		t.Fatalf("NewDelegationDispatcher(): %v", err)
	}

	result, err := dispatcher.DispatchOne(context.Background())
	if err != nil {
		t.Fatalf("DispatchOne(): %v", err)
	}
	if !result.Claimed || !result.Failed || !result.Published || !result.Acknowledged || result.FailureReason != conversationruntime.ReadbackFailureExpired {
		t.Fatalf("DispatchOne() result = %+v, want committed expired failure", result)
	}
	if generator.calls != 0 || store.failures != 1 || store.accepts != 0 || store.retries != 0 || store.acks != 0 {
		t.Fatalf("dispatch counts generator=%d fail=%d accept=%d retry=%d ack=%d, want 0/1/0/0/0", generator.calls, store.failures, store.accepts, store.retries, store.acks)
	}
	if store.failureInput == nil || store.failureInput.Reason != eventstore.ReadbackFailureExpired ||
		store.failureInput.OperationID != "delegation-readback-failure:"+string(handoff.DelegationID)+":1" ||
		store.failureInput.Work.WorkID != store.lease.WorkID || store.failureInput.Work.LeaseToken != store.lease.LeaseToken ||
		store.failureInput.Work.Version != store.lease.Version || store.failureInput.DelegationID != handoff.DelegationID ||
		store.failureInput.Revision != handoff.Revision || store.failureInput.Assignee != handoff.ToActor {
		t.Fatalf("EventStore failure input = %#v, want canonical lease/delegation mapping", store.failureInput)
	}
	if len(publisher.turns) != 2 || publisher.turns[0] != handoff.OriginTurnID || publisher.turns[1] != handoff.OriginTurnID {
		t.Fatalf("publication order = %v, want committed origin then terminal failure", publisher.turns)
	}
}

func TestResponseFirstReadbackDrainStartsFromProcessOwnedContextAfterRestart(t *testing.T) {
	dispatcher := &readbackDrainSpy{calls: make(chan context.Context, 2)}
	runtime := &responseFirstConversationRuntime{
		dispatcher: dispatcher, dispatchWake: make(chan struct{}, 1),
	}
	for attempt := 0; attempt < 2; attempt++ {
		stop := runtime.startReadbackDrain()
		select {
		case ctx := <-dispatcher.calls:
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("readback drain dispatch has no bounded process-owned deadline")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("readback drain did not resume on start attempt %d", attempt+1)
		}
		stop()
	}
}

type readbackWiringProvider struct {
	name  string
	calls int
}

func (p *readbackWiringProvider) GenerateConversation(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{Content: `{}`}, nil
}

func (p *readbackWiringProvider) Name() string { return p.name }

type readbackWiringGenerator struct {
	calls  int
	result conversation.ReadbackEnvelope
}

func (g *readbackWiringGenerator) GenerateReadback(_ context.Context, _ conversation.ConversationScope, _ conversation.HandoffEnvelope, _ string) (conversation.ReadbackEnvelope, agent.InvocationReceipt, error) {
	g.calls++
	if g.result.Status != "" {
		return g.result, agent.InvocationReceipt{Status: "completed"}, nil
	}
	return conversation.ReadbackEnvelope{}, agent.InvocationReceipt{}, errors.New("unexpected readback generation")
}

type readbackDrainSpy struct{ calls chan context.Context }

func (d *readbackDrainSpy) DispatchOne(ctx context.Context) (conversationruntime.DelegationDispatchResult, error) {
	d.calls <- ctx
	return conversationruntime.DelegationDispatchResult{}, nil
}

type readbackWiringEventStore struct {
	responseFirstDelegationPersistence
	outcome          eventstore.CommittedBatch
	lease            eventstore.LeasedWorkItem
	handoff          conversation.HandoffEnvelope
	context          []eventstore.DelegationContextMessage
	acceptedAt       time.Time
	readbackDeadline time.Time
	claim            eventstore.WorkClaimInput
	load             eventstore.DelegationDispatchInput
	acceptedInput    *eventstore.ReadbackInput
	failureInput     *eventstore.ReadbackFailureInput
	ackInput         eventstore.WorkLeaseInput
	retryInput       eventstore.WorkRetryInput
	retries          int
	acks             int
	accepts          int
	failures         int
}

type readbackWiringOutcomes struct {
	responseFirstJournalStore
	store *readbackWiringEventStore
}

func (s readbackWiringOutcomes) LoadCommittedOutcome(ctx context.Context, turnID modulecore.TurnID) (eventstore.CommittedBatch, bool, error) {
	if turnID != s.store.handoff.OriginTurnID {
		return eventstore.CommittedBatch{}, false, errors.New("unexpected origin turn")
	}
	return s.store.outcome, true, nil
}

func newReadbackWiringEventStore(t *testing.T) (*readbackWiringEventStore, conversation.HandoffEnvelope, eventstore.LeasedWorkItem, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	handoff := conversation.HandoffEnvelope{
		DelegationID: modulecore.NewRequestID(), Revision: 1, OriginTurnID: modulecore.NewTurnID(),
		OriginUserMessageID: modulecore.NewMessageID(), SessionID: modulecore.NewSessionID(), SourceTraceID: modulecore.NewTraceID(),
		FromActor: "mio", ToActor: "shiro", InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: strings.Repeat("a", 64), AllowedCapabilities: []string{}, ReplyTo: modulecore.NewMessageID(),
		Goal: "Inspect the request", Instruction: "Review the request and report.",
		Constraints: []conversation.AgentConstraint{{Key: "keep_scope", Text: "Keep the requested scope."}}, ExpectedOutput: "A concise report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	lease := eventstore.LeasedWorkItem{
		WorkItem: eventstore.WorkItem{
			WorkID:    "dispatch:" + string(handoff.OriginTurnID),
			DedupeKey: fmt.Sprintf("delegation:%s:%d:start", handoff.DelegationID, handoff.Revision),
			Kind:      conversationruntime.DispatchDelegationWorkKind, SourceEventID: modulecore.NewEventID(), SessionID: handoff.SessionID,
			Priority: 5,
		},
		Attempt: 1, Assignee: handoff.ToActor, LeaseToken: string(modulecore.NewRequestID()),
		LeaseExpiresAt: now.Add(time.Minute), Version: 1,
	}
	return &readbackWiringEventStore{
		lease: lease, handoff: handoff, context: []eventstore.DelegationContextMessage{{
			MessageID: handoff.OriginUserMessageID, Role: "user", Text: "original request",
		}}, acceptedAt: now, readbackDeadline: now.Add(responseFirstReadbackTimeout),
	}, handoff, lease, now
}

func (s *readbackWiringEventStore) ClaimWork(_ context.Context, input eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error) {
	s.claim = input
	return []eventstore.LeasedWorkItem{s.lease}, nil
}

func (s *readbackWiringEventStore) LoadDelegationDispatch(_ context.Context, input eventstore.DelegationDispatchInput) (eventstore.DelegationDispatch, error) {
	s.load = input
	if input.WorkID != s.lease.WorkID || input.LeaseToken != s.lease.LeaseToken || input.SessionID != s.lease.SessionID || input.Assignee != s.lease.Assignee {
		return eventstore.DelegationDispatch{}, errors.New("unexpected guarded dispatch input")
	}
	return eventstore.DelegationDispatch{
		Work: s.lease, Handoff: s.handoff, Context: s.context,
		AcceptedAt: s.acceptedAt, ReadbackDeadline: s.readbackDeadline,
	}, nil
}

func (s *readbackWiringEventStore) AcceptReadback(_ context.Context, input eventstore.ReadbackInput) (eventstore.ReadbackAcceptance, error) {
	s.accepts++
	s.acceptedInput = &input
	content := input.Readback.Text
	event := modulecore.NewEventEnvelope(s.handoff.SourceTraceID, s.lease.SourceEventID, nil, "orchestrator", "agent.response", input.OccurredAt, map[string]any{
		"kind": "readback", "schema_version": input.Readback.SchemaVersion, "status": string(input.Readback.Status),
		"understood_goal": input.Readback.UnderstoodGoal, "acknowledged_constraint_keys": input.Readback.AcknowledgedConstraintKeys,
		"expected_output": input.Readback.ExpectedOutput, "delegation_id": string(input.DelegationID), "delegation_revision": input.Revision,
		"availability": "free", "visibility": "public", "from": input.Assignee, "source_actor_id": input.Assignee,
		"to": "user", "content": content, "text": content, "reply_to_message_id": string(s.handoff.ReplyTo),
	})
	event.SessionID, event.TurnID, event.RequestID, event.MessageID, event.EventSeq = s.handoff.SessionID, s.handoff.OriginTurnID, s.handoff.DelegationID, input.MessageID, 8
	return eventstore.ReadbackAcceptance{
		OperationID: input.OperationID, DelegationID: input.DelegationID, Revision: input.Revision,
		OriginTurnID: s.handoff.OriginTurnID, SessionID: s.handoff.SessionID, Status: input.Readback.Status,
		Availability: "free", TaskStartQueued: true, Event: event,
	}, nil
}

func (s *readbackWiringEventStore) AckWork(_ context.Context, input eventstore.WorkLeaseInput) error {
	s.acks++
	s.ackInput = input
	return nil
}

func (s *readbackWiringEventStore) FailReadback(_ context.Context, input eventstore.ReadbackFailureInput) (eventstore.ReadbackFailure, error) {
	s.failures++
	s.failureInput = &input
	content := "委譲先から有効な復唱を確認できませんでした。Mioは再び応答できます。"
	event := modulecore.NewEventEnvelope(s.handoff.SourceTraceID, s.lease.SourceEventID, nil, "orchestrator", "agent.response", input.OccurredAt, map[string]any{
		"kind": "handoff_error", "status": "failed", "reason": string(input.Reason),
		"delegation_id": string(input.DelegationID), "delegation_revision": input.Revision,
		"availability": "free", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user",
		"content": content, "text": content, "reply_to_message_id": string(s.handoff.ReplyTo),
	})
	event.SessionID, event.TurnID, event.RequestID, event.MessageID, event.EventSeq =
		s.handoff.SessionID, s.handoff.OriginTurnID, s.handoff.DelegationID, input.MessageID, 9
	return eventstore.ReadbackFailure{
		OperationID: input.OperationID, DelegationID: input.DelegationID, Revision: input.Revision,
		OriginTurnID: s.handoff.OriginTurnID, SessionID: s.handoff.SessionID,
		Reason: input.Reason, Availability: "free", Event: event,
	}, nil
}

func (s *readbackWiringEventStore) RetryWork(_ context.Context, input eventstore.WorkRetryInput) error {
	s.retries++
	s.retryInput = input
	return nil
}

type readbackWiringPublisher struct {
	turns     []modulecore.TurnID
	failFirst bool
}

func (p *readbackWiringPublisher) PublishCommitted(_ context.Context, bundle conversationruntime.CommittedBundle) error {
	p.turns = append(p.turns, bundle.TurnID)
	if p.failFirst && len(p.turns) == 1 {
		return errors.New("canonical publisher unavailable")
	}
	return nil
}

func responseFirstDelegationNoticeBatch(handoff conversation.HandoffEnvelope, now time.Time) eventstore.CommittedBatch {
	event := modulecore.NewEventEnvelope(handoff.SourceTraceID, "", nil, "orchestrator", "agent.response", now, map[string]any{
		"kind": "delegation_notice", "visibility": "public", "to": "user",
		"delegation_id": string(handoff.DelegationID), "delegation_revision": handoff.Revision,
	})
	event.SessionID, event.TurnID, event.MessageID, event.EventSeq = handoff.SessionID, handoff.OriginTurnID, modulecore.NewMessageID(), 7
	contextEvent := modulecore.NewEventEnvelope(handoff.SourceTraceID, "", nil, "conversation_runtime", "conversation.delegation_context", now, map[string]any{
		"handoff": handoff,
	})
	contextEvent.SessionID, contextEvent.TurnID, contextEvent.EventSeq =
		handoff.SessionID, handoff.OriginTurnID, event.EventSeq-1
	return eventstore.CommittedBatch{
		OperationID: "turn:" + string(handoff.OriginTurnID), SessionID: handoff.SessionID,
		TurnID: handoff.OriginTurnID, ResultState: "awaiting_readback", Events: []modulecore.EventEnvelope{contextEvent, event},
	}
}

func TestResponseFirstDelegationDispatchValidatesContinuedHandoffAgainstReceipt(t *testing.T) {
	ctx := context.Background()
	base, original, _, now := newReadbackWiringEventStore(t)
	current := original
	current.Revision++
	current.Instruction = "Use the staging region from the original request."
	current.InstructionMessageID = modulecore.NewMessageID()
	base.handoff = current
	base.lease.WorkID = fmt.Sprintf("dispatch:%s:%d", current.DelegationID, current.Revision)
	base.lease.DedupeKey = fmt.Sprintf("delegation:%s:%d:start", current.DelegationID, current.Revision)
	base.lease.SourceEventID = modulecore.NewEventID()

	question := modulecore.NewEventEnvelope(current.SourceTraceID, modulecore.NewEventID(), nil, "conversation_runtime", "conversation.clarification_question", now, map[string]any{
		"kind": "clarification_question", "visibility": "private", "delegation_id": string(current.DelegationID),
		"delegation_revision": current.Revision - 1,
	})
	question.SessionID, question.TurnID, question.RequestID, question.EventSeq = current.SessionID, modulecore.NewTurnID(), current.DelegationID, 10
	instruction := modulecore.NewEventEnvelope(current.SourceTraceID, question.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", now, map[string]any{
		"kind": "delegation_instruction", "visibility": "private", "delegation_id": string(current.DelegationID),
		"delegation_revision": current.Revision, "from": "mio", "source_actor_id": "mio", "to": current.ToActor,
		"content": current.Instruction, "text": current.Instruction,
	})
	instruction.SessionID, instruction.TurnID, instruction.RequestID = current.SessionID, current.OriginTurnID, current.DelegationID
	instruction.MessageID, instruction.ActorKind, instruction.ActorID, instruction.EventSeq = current.InstructionMessageID, "agent", "mio", 11
	base.lease.SourceEventID = instruction.EventID
	continuedContext := modulecore.NewEventEnvelope(current.SourceTraceID, modulecore.NewEventID(), nil, "conversation_runtime", "conversation.delegation_context", now, map[string]any{
		"handoff": current,
	})
	continuedContext.SessionID, continuedContext.TurnID, continuedContext.RequestID, continuedContext.EventSeq =
		current.SessionID, current.OriginTurnID, current.DelegationID, 12
	receipt := modulecore.NewEventEnvelope(current.SourceTraceID, question.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", now, map[string]any{
		"status": "completed",
	})
	receipt.SessionID, receipt.TurnID, receipt.EventSeq = current.SessionID, question.TurnID, 13
	message := eventstore.MessageIndex{
		MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: current.SessionID, TurnID: current.OriginTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: current.ToActor, Kind: "delegation_instruction",
		ReplyToMessageID: current.ReplyTo, Visibility: "private",
	}
	decision := eventstore.ClarificationDecisionCommit{
		OperationID: "clarification.decision:" + string(question.TurnID), SessionID: current.SessionID,
		ControlTurnID: question.TurnID, DelegationID: current.DelegationID, Revision: current.Revision - 1,
		Kind: conversation.ClarificationDecisionContinueDelegation, ResultState: "awaiting_readback",
		InvocationReceipt: agent.InvocationReceipt{Status: "completed"}, Event: instruction, ReceiptEvent: receipt,
		Message: message, Events: []modulecore.EventEnvelope{continuedContext, instruction, receipt},
		MessageIndexes: []eventstore.MessageIndex{message}, NextHandoff: &current, DispatchQueued: true,
	}
	eventStore := &responseFirstContinuationEventStore{
		readbackWiringEventStore: base,
		events:                   map[modulecore.EventID]modulecore.EventEnvelope{question.EventID: question, instruction.EventID: instruction},
	}
	journal := &responseFirstContinuationOutcomeJournal{
		origin: responseFirstDelegationNoticeBatch(original, now), decision: decision, found: true,
	}
	publication := &readbackWiringPublisher{}
	dispatchStore := responseFirstDelegationDispatchStore{store: eventStore, outcomes: journal, publisher: publication}
	lease := conversationruntime.DelegationDispatchLease{
		WorkID: base.lease.WorkID, DedupeKey: base.lease.DedupeKey, Kind: base.lease.Kind,
		SourceEventID: base.lease.SourceEventID, SessionID: base.lease.SessionID, Priority: base.lease.Priority,
		Attempt: base.lease.Attempt, LeaseToken: base.lease.LeaseToken, LeaseExpires: base.lease.LeaseExpiresAt,
		Version: base.lease.Version, Assignee: base.lease.Assignee,
	}
	loaded, err := dispatchStore.LoadDelegationDispatch(ctx, lease, now)
	if err != nil {
		t.Fatalf("LoadDelegationDispatch(revision 2): %v", err)
	}
	if !reflect.DeepEqual(loaded.Handoff, current) {
		t.Fatalf("loaded handoff = %#v, want persisted revision-2 handoff %#v", loaded.Handoff, current)
	}
	if !reflect.DeepEqual(publication.turns, []modulecore.TurnID{original.OriginTurnID}) {
		t.Fatalf("published turns = %v, want origin public notice only", publication.turns)
	}
}

func TestResponseFirstDelegationDispatchRejectsBrokenContinuationReceipt(t *testing.T) {
	ctx := context.Background()
	base, original, _, now := newReadbackWiringEventStore(t)
	current := original
	current.Revision++
	current.Instruction = "Use the staging region from the original request."
	current.InstructionMessageID = modulecore.NewMessageID()
	base.handoff = current
	base.lease.WorkID = fmt.Sprintf("dispatch:%s:%d", current.DelegationID, current.Revision)
	base.lease.DedupeKey = fmt.Sprintf("delegation:%s:%d:start", current.DelegationID, current.Revision)

	question := modulecore.NewEventEnvelope(current.SourceTraceID, modulecore.NewEventID(), nil, "conversation_runtime", "conversation.clarification_question", now, map[string]any{
		"kind": "clarification_question", "visibility": "private", "delegation_id": string(current.DelegationID),
		"delegation_revision": current.Revision - 1,
	})
	question.SessionID, question.TurnID, question.RequestID, question.EventSeq = current.SessionID, modulecore.NewTurnID(), current.DelegationID, 10
	instruction := modulecore.NewEventEnvelope(current.SourceTraceID, question.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", now, map[string]any{
		"kind": "delegation_instruction", "visibility": "private", "delegation_id": string(current.DelegationID),
		"delegation_revision": current.Revision, "from": "mio", "source_actor_id": "mio", "to": current.ToActor,
		"content": current.Instruction, "text": current.Instruction,
	})
	instruction.SessionID, instruction.TurnID, instruction.RequestID = current.SessionID, current.OriginTurnID, current.DelegationID
	instruction.MessageID, instruction.ActorKind, instruction.ActorID, instruction.EventSeq = current.InstructionMessageID, "agent", "mio", 11
	base.lease.SourceEventID = instruction.EventID
	continuedContext := modulecore.NewEventEnvelope(current.SourceTraceID, modulecore.NewEventID(), nil, "conversation_runtime", "conversation.delegation_context", now, map[string]any{"handoff": current})
	continuedContext.SessionID, continuedContext.TurnID, continuedContext.RequestID, continuedContext.EventSeq = current.SessionID, current.OriginTurnID, current.DelegationID, 12
	receipt := modulecore.NewEventEnvelope(current.SourceTraceID, question.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", now, map[string]any{"status": "completed"})
	receipt.SessionID, receipt.TurnID, receipt.EventSeq = current.SessionID, question.TurnID, 13
	decision := eventstore.ClarificationDecisionCommit{
		OperationID: "clarification.decision:" + string(question.TurnID), SessionID: current.SessionID,
		ControlTurnID: question.TurnID, DelegationID: current.DelegationID, Revision: current.Revision - 1,
		Kind: conversation.ClarificationDecisionContinueDelegation, ResultState: "awaiting_readback", DispatchQueued: true,
		Event: instruction, ReceiptEvent: receipt, Events: []modulecore.EventEnvelope{continuedContext, instruction, receipt},
		NextHandoff: &current,
	}
	wrongNext := current
	wrongNext.Instruction = "Tampered instruction"
	decision.NextHandoff = &wrongNext
	eventStore := &responseFirstContinuationEventStore{readbackWiringEventStore: base, events: map[modulecore.EventID]modulecore.EventEnvelope{
		question.EventID: question, instruction.EventID: instruction,
	}}
	journal := &responseFirstContinuationOutcomeJournal{origin: responseFirstDelegationNoticeBatch(original, now), decision: decision, found: true}
	dispatchStore := responseFirstDelegationDispatchStore{store: eventStore, outcomes: journal, publisher: &readbackWiringPublisher{}}
	lease := conversationruntime.DelegationDispatchLease{
		WorkID: base.lease.WorkID, DedupeKey: base.lease.DedupeKey, Kind: base.lease.Kind,
		SourceEventID: base.lease.SourceEventID, SessionID: base.lease.SessionID, Priority: base.lease.Priority,
		Attempt: base.lease.Attempt, LeaseToken: base.lease.LeaseToken, LeaseExpires: base.lease.LeaseExpiresAt,
		Version: base.lease.Version, Assignee: base.lease.Assignee,
	}
	if _, err := dispatchStore.LoadDelegationDispatch(ctx, lease, now); err == nil {
		t.Fatal("LoadDelegationDispatch() error=nil for tampered durable NextHandoff")
	}
}

type responseFirstContinuationEventStore struct {
	*readbackWiringEventStore
	events map[modulecore.EventID]modulecore.EventEnvelope
}

func (s *responseFirstContinuationEventStore) GetByID(_ context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	event, found := s.events[eventID]
	return event, found, nil
}

type responseFirstContinuationOutcomeJournal struct {
	responseFirstJournalStore
	origin   eventstore.CommittedBatch
	decision eventstore.ClarificationDecisionCommit
	found    bool
}

func (j *responseFirstContinuationOutcomeJournal) LoadCommittedOutcome(_ context.Context, _ modulecore.TurnID) (eventstore.CommittedBatch, bool, error) {
	return j.origin, true, nil
}

func (j *responseFirstContinuationOutcomeJournal) LoadClarificationDecisionOutcome(_ context.Context, _ modulecore.TurnID) (eventstore.ClarificationDecisionCommit, bool, error) {
	return j.decision, j.found, nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
