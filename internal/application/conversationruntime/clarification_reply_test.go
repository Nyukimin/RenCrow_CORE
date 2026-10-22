package conversationruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunAcceptedTurnContinuesExplicitClarificationReplyOnceAndPrivately(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	store := &clarificationReplyStoreFake{context: fixture.stored}
	generator := &clarificationReplyGeneratorFake{instruction: "Use the explicitly selected staging region and retain the existing scope."}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || !result.Committed || result.ResultState != "awaiting_readback" {
		t.Fatalf("RunAcceptedTurn() = %+v, want committed private continuation awaiting readback", result)
	}
	if store.contextCalls != 1 || store.lastContextRequest != (ClarificationReplyContextRequest{
		SessionID: fixture.request.Session.SessionID, ReplyTurnID: fixture.request.Accepted.TurnID, OwnerID: fixture.ownerID,
	}) || store.commitCalls != 1 || store.outcomeCalls != 1 {
		t.Fatalf("reply store calls context=%d commit=%d outcome=%d request=%+v", store.contextCalls, store.commitCalls, store.outcomeCalls, store.lastContextRequest)
	}
	if generator.replyCalls != 1 || generator.emissionCalls != 0 || generator.scope.ActorID != fixture.actorID ||
		generator.scope.Purpose != conversation.PurposeClarificationReply || generator.replyContext.QuestionText != "Which deployment region should I use?" ||
		generator.snapshot.Messages[len(generator.snapshot.Messages)-1].Content != fixture.stored.AnswerText {
		t.Fatalf("clarification reply generation = calls:%d/%d scope:%+v context:%+v snapshot:%+v", generator.replyCalls, generator.emissionCalls, generator.scope, generator.replyContext, generator.snapshot)
	}
	if fixture.stored.AnswerEvent.TraceID == fixture.stored.Handoff.SourceTraceID ||
		store.result.ContextEvent.TraceID != fixture.stored.Handoff.SourceTraceID || store.result.Event.TraceID != fixture.stored.Handoff.SourceTraceID ||
		store.result.Event.CausationEventID != store.result.ContextEvent.EventID ||
		store.result.ReceiptEvent.TraceID != fixture.stored.AnswerEvent.TraceID ||
		store.result.ReceiptEvent.CausationEventID != fixture.stored.AnswerEvent.EventID {
		t.Fatalf("clarification reply continuation did not preserve source and reply trace boundaries: answer=%s handoff=%s context=%s instruction=%s receipt=%s",
			fixture.stored.AnswerEvent.TraceID, fixture.stored.Handoff.SourceTraceID, store.result.ContextEvent.TraceID,
			store.result.Event.TraceID, store.result.ReceiptEvent.TraceID)
	}
	if store.lastCommit.OwnerID != fixture.ownerID || store.lastCommit.ReplyTurnID != fixture.request.Accepted.TurnID ||
		store.lastCommit.PromptMessageID != fixture.stored.Receipt.PromptMessageID || store.lastCommit.Revision != conversation.MaxClarificationRevision ||
		store.lastCommit.Instruction != generator.instruction || store.lastCommit.InvocationReceipt != generator.receipt {
		t.Fatalf("continuation commit input = %+v", store.lastCommit)
	}
	if publisher.publishCount() != 1 || result.Batch.TurnID != fixture.request.Accepted.TurnID || len(result.Batch.Events) != 3 {
		t.Fatalf("published reply result batch=%+v publishes=%d", result.Batch, publisher.publishCount())
	}
	if !reflect.DeepEqual(store.result.NextHandoff.Constraints, fixture.stored.Handoff.Constraints) ||
		!reflect.DeepEqual(store.result.NextHandoff.AllowedCapabilities, fixture.stored.Handoff.AllowedCapabilities) ||
		store.result.NextHandoff.Goal != fixture.stored.Handoff.Goal || store.result.NextHandoff.ExpectedOutput != fixture.stored.Handoff.ExpectedOutput ||
		store.result.NextHandoff.ToActor != fixture.stored.Handoff.ToActor || store.result.NextRevision != conversation.MaxClarificationRevision+1 {
		t.Fatalf("continuation changed canonical handoff or blocked explicit answer past automatic limit: %+v", store.result.NextHandoff)
	}
	for _, event := range result.Batch.Events {
		if event.EventType == "agent.response" || event.ComponentID == "orchestrator" || strings.Contains(fmt.Sprint(event.Payload), fixture.stored.QuestionEvent.Payload["text"].(string)) {
			t.Fatalf("continuation output exposed private question or created a public reply: %+v", event)
		}
	}
	if result.Batch.Events[1].EventType != "conversation.delegation_instruction" ||
		payloadString(result.Batch.Events[1].Payload, "visibility") != "private" ||
		result.Batch.Events[2].EventType != "conversation.mio_emission_receipt" {
		t.Fatalf("published events did not separate private instruction and receipt: %+v", result.Batch.Events)
	}
	if store.result.InvocationReceipt != (agent.InvocationReceipt{
		RequestID: generator.receipt.RequestID, ResponseID: generator.receipt.ResponseID, Status: generator.receipt.Status,
	}) {
		t.Fatalf("continuation outcome receipt should contain only persisted fields: %+v", store.result.InvocationReceipt)
	}
	contextMessages := decodeClarificationReplyTestMessages(t, result.Batch.Events[0].Payload["messages"])
	if len(contextMessages) != len(fixture.stored.Context)+1 || contextMessages[len(contextMessages)-1].MessageID != fixture.stored.Reply.InputMessageID ||
		contextMessages[len(contextMessages)-1].Text != fixture.stored.AnswerText {
		t.Fatalf("committed canonical context did not append the explicit answer exactly once: %+v", contextMessages)
	}
}

func TestRunAcceptedTurnReplaysCommittedClarificationReplyWithoutRegeneration(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	committed := clarificationReplyCommitResult(fixture.stored, clarificationReplyContinuationInputForTest(fixture, "Use staging."))
	committed.InvocationReceipt = persistedClarificationReplyReceipt(committed.InvocationReceipt)
	store := &clarificationReplyStoreFake{outcome: committed, outcomeFound: true}
	generator := &clarificationReplyGeneratorFake{}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || !result.Committed || generator.replyCalls != 0 || generator.emissionCalls != 0 ||
		store.contextCalls != 0 || store.commitCalls != 0 || store.outcomeCalls != 1 || publisher.publishCount() != 1 {
		t.Fatalf("committed reply replay = %+v generation=%d/%d context=%d commit=%d outcome=%d publish=%d",
			result, generator.replyCalls, generator.emissionCalls, store.contextCalls, store.commitCalls, store.outcomeCalls, publisher.publishCount())
	}
	for _, event := range publisher.batches[0].Events {
		if event.ComponentID == "orchestrator" || event.EventType == "agent.response" ||
			payloadString(event.Payload, "visibility") == "public" {
			t.Fatalf("replay published a private continuation as public output: %+v", event)
		}
	}
	if len(publisher.batches[0].Events) != 3 || publisher.batches[0].Events[1].EventType != "conversation.delegation_instruction" ||
		payloadString(publisher.batches[0].Events[1].Payload, "visibility") != "private" ||
		payloadString(publisher.batches[0].Events[1].Payload, "content") != committed.NextHandoff.Instruction {
		t.Fatalf("replay did not preserve the saved instruction as private dispatch: %+v", publisher.batches[0].Events)
	}
}

func TestRunAcceptedTurnReadsBackUncertainClarificationReplyCommitWithoutRegeneration(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	commitErr := errors.New("commit acknowledgement was lost")
	store := &clarificationReplyStoreFake{
		context: fixture.stored, commitErr: commitErr, saveBeforeCommitError: true,
	}
	generator := &clarificationReplyGeneratorFake{instruction: "Use staging."}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || generator.replyCalls != 1 || store.commitCalls != 1 || store.outcomeCalls != 2 || publisher.publishCount() != 1 {
		t.Fatalf("uncertain reply commit result=%+v generation=%d commit=%d outcome=%d publish=%d",
			result, generator.replyCalls, store.commitCalls, store.outcomeCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnDoesNotRegenerateWhenClarificationReplyCommitIsUnresolved(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	store := &clarificationReplyStoreFake{context: fixture.stored, commitErr: errors.New("commit acknowledgement was lost")}
	generator := &clarificationReplyGeneratorFake{instruction: "Use staging."}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusCommitUncertain || result.Committed || generator.replyCalls != 1 || store.commitCalls != 1 ||
		store.outcomeCalls != 2 || publisher.publishCount() != 0 {
		t.Fatalf("unresolved reply commit result=%+v generation=%d commit=%d outcome=%d publish=%d",
			result, generator.replyCalls, store.commitCalls, store.outcomeCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnRejectsMismatchedStoredClarificationReplyBeforeGeneration(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	fixture.stored.QuestionMessage.ToActor = "kuro"
	store := &clarificationReplyStoreFake{context: fixture.stored}
	generator := &clarificationReplyGeneratorFake{}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusFailedBeforeCommit || result.Cause == nil || generator.replyCalls != 0 || store.commitCalls != 0 || publisher.publishCount() != 0 {
		t.Fatalf("mismatched stored reply context result=%+v generation=%d commit=%d publish=%d", result, generator.replyCalls, store.commitCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnRejectsOversizedStoredClarificationAnswerBeforeGeneration(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	tooLong := strings.Repeat("x", conversation.MaxUserMessageBytes+1)
	fixture.stored.AnswerEvent.Payload["content"] = tooLong
	fixture.stored.AnswerEvent.Payload["text"] = tooLong
	fixture.stored.AnswerText = tooLong
	store := &clarificationReplyStoreFake{context: fixture.stored}
	generator := &clarificationReplyGeneratorFake{instruction: "Use staging."}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusFailedBeforeCommit || result.Cause == nil || generator.replyCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("oversized stored answer status=%s cause=%v generation=%d commit=%d", result.Status, result.Cause, generator.replyCalls, store.commitCalls)
	}
}

func TestRunAcceptedTurnDoesNotGenerateClarificationReplyWhenOutcomeLookupFails(t *testing.T) {
	fixture := newClarificationReplyRuntimeFixture(t)
	store := &clarificationReplyStoreFake{outcomeErr: errors.New("outcome store unavailable")}
	generator := &clarificationReplyGeneratorFake{}
	publisher := &fakePublisher{}
	runtime := newClarificationReplyRuntime(t, store, generator, publisher)

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusCommitUncertain || generator.replyCalls != 0 || store.contextCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("reply outcome lookup failure result=%+v generation=%d context=%d commit=%d", result, generator.replyCalls, store.contextCalls, store.commitCalls)
	}
}

type clarificationReplyRuntimeFixture struct {
	ownerID string
	actorID string
	request ReadViewRequest
	stored  StoredClarificationReplyContext
	answer  string
	when    time.Time
}

func newClarificationReplyRuntimeFixture(t *testing.T) clarificationReplyRuntimeFixture {
	t.Helper()
	const ownerID, actorID, assignee = "session-owner", "viewer-user", "shiro"
	now := time.Now().UTC().Truncate(time.Second)
	sessionID, originTurnID, controlTurnID, replyTurnID := modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewTurnID(), modulecore.NewTurnID()
	originTraceID := modulecore.NewTraceID()
	originMessageID, questionMessageID, promptMessageID, answerMessageID := modulecore.NewMessageID(), modulecore.NewMessageID(), modulecore.NewMessageID(), modulecore.NewMessageID()
	delegationID := modulecore.NewRequestID()
	originEvent := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", now, map[string]any{
		"from": "user", "source_actor_id": actorID, "to": "mio", "visibility": "public", "text": "original public task", "content": "original public task",
	})
	originEvent.TraceID, originEvent.SessionID, originEvent.TurnID, originEvent.MessageID, originEvent.EventSeq = originTraceID, sessionID, originTurnID, originMessageID, 2
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: conversation.MaxClarificationRevision, OriginTurnID: originTurnID,
		OriginUserMessageID: originMessageID, SessionID: sessionID, SourceTraceID: originTraceID,
		FromActor: "mio", ToActor: assignee, InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: strings.Repeat("a", 64), AllowedCapabilities: []string{"conversation.read"}, ReplyTo: originMessageID,
		Goal: "Investigate the deployment", Instruction: "Inspect the deployment without changing it.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the original request.", SourceMessageIDs: []modulecore.MessageID{originMessageID}}},
		ExpectedOutput: "An evidence-backed report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	questionEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.clarification_question", now.Add(time.Second), map[string]any{
		"kind": "clarification_question", "status": "needs_clarification", "delegation_id": string(delegationID),
		"delegation_revision": conversation.MaxClarificationRevision, "visibility": "private", "from": assignee,
		"source_actor_id": assignee, "to": "mio", "text": "Which deployment region should I use?", "content": "Which deployment region should I use?",
		"reply_to_message_id": string(originMessageID),
	})
	questionEvent.TraceID, questionEvent.SessionID, questionEvent.TurnID, questionEvent.RequestID, questionEvent.MessageID, questionEvent.EventSeq =
		originTraceID, sessionID, controlTurnID, delegationID, questionMessageID, 4
	questionIndex := MessageIndex{
		MessageID: questionMessageID, EventID: questionEvent.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: assignee, ToKind: "agent", ToActor: "mio", Kind: "clarification_question",
		ReplyToMessageID: originMessageID, Visibility: "private",
	}
	controlTurn := AcceptedTurn{
		SessionID: sessionID, TurnID: controlTurnID, ActorID: assignee, TriggerKind: "clarification_control",
		ClientSubmissionID: "clarification-control", InputMessageID: questionMessageID, AdmissionSeq: 4, GenerationEpoch: 7,
		State: "completed", InputEvent: questionEvent,
	}
	promptEvent := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", now.Add(2*time.Second), map[string]any{
		"kind": "clarification_prompt", "status": "awaiting_user_clarification", "availability": "free",
		"from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public",
		"text": "Which deployment region should I use?", "content": "Which deployment region should I use?",
		"delegation_id": string(delegationID), "delegation_revision": conversation.MaxClarificationRevision,
		"reply_to_message_id": string(handoff.ReplyTo),
	})
	promptEvent.TraceID, promptEvent.SessionID, promptEvent.TurnID, promptEvent.MessageID, promptEvent.EventSeq = originTraceID, sessionID, controlTurnID, promptMessageID, 5
	promptEvent.CausationEventID = questionEvent.EventID
	promptMessage := MessageIndex{
		MessageID: promptMessageID, EventID: promptEvent.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: "mio", ToKind: "user", ToActor: ownerID, Kind: "clarification_prompt",
		ReplyToMessageID: handoff.ReplyTo, Visibility: "public",
	}
	answer := "Use the staging region."
	answerEvent := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", now.Add(3*time.Second), map[string]any{
		"from": "user", "source_actor_id": actorID, "to": "mio", "visibility": "public", "text": answer, "content": answer,
		"reply_to_message_id": string(promptMessageID),
	})
	answerEvent.TraceID, answerEvent.SessionID, answerEvent.TurnID, answerEvent.MessageID, answerEvent.EventSeq = modulecore.NewTraceID(), sessionID, replyTurnID, answerMessageID, 8
	answerIndex := MessageIndex{
		MessageID: answerMessageID, EventID: answerEvent.EventID, SessionID: sessionID, TurnID: replyTurnID,
		FromActor: actorID, ToKind: "agent", ToActor: "mio", Kind: "user_message",
		ReplyToMessageID: promptMessageID, Visibility: "public",
	}
	accepted := AcceptedTurn{
		SessionID: sessionID, TurnID: replyTurnID, ActorID: actorID, TriggerKind: "clarification_reply",
		ClientSubmissionID: "clarification-reply-submission", InputMessageID: answerMessageID,
		AdmissionSeq: 8, GenerationEpoch: 7, State: "accepted", InputEvent: answerEvent,
	}
	stored := StoredClarificationReplyContext{
		Reply: accepted, OwnerID: ownerID,
		Receipt:     ClarificationReplyReceipt{PromptMessageID: promptMessageID, SourceTurnID: originTurnID, DelegationID: delegationID, Revision: conversation.MaxClarificationRevision},
		AnswerEvent: answerEvent, AnswerMessage: answerIndex,
		PromptEvent: promptEvent, PromptMessage: promptMessage,
		ControlTurn: controlTurn, QuestionEvent: questionEvent, QuestionMessage: questionIndex,
		Handoff: handoff, OriginTurnID: originTurnID,
		Context: []DelegationDispatchMessage{{MessageID: originMessageID, Role: "user", Text: "original public task"}}, AnswerText: answer,
	}
	request := ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: actorID},
		Session: SessionState{SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 1, AdmissionSeq: 8, ProjectionWatermark: 1, GenerationEpoch: 7},
		Accepted: accepted, Legacy: LegacyProjection{AppliedThrough: 1, Complete: true},
	}
	return clarificationReplyRuntimeFixture{ownerID: ownerID, actorID: actorID, request: request, stored: stored, answer: answer, when: now}
}

func newClarificationReplyRuntime(t *testing.T, store ClarificationReplyStore, generator EmissionGenerator, publisher CommittedEventPublisher) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{}, Composer: identityPromptComposer{}, Generator: generator,
		Journal: &fakeJournal{}, Publisher: publisher, ClarificationReplies: store,
		Config: RuntimeConfig{PolicyVersion: "reply-policy-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}
	return runtime
}

type clarificationReplyGeneratorFake struct {
	instruction   string
	receipt       agent.InvocationReceipt
	generateErr   error
	replyCalls    int
	emissionCalls int
	scope         conversation.ConversationScope
	snapshot      agent.PromptSnapshot
	replyContext  conversation.ClarificationReplyGenerationContext
}

func (generator *clarificationReplyGeneratorFake) GenerateEmission(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	generator.emissionCalls++
	return conversation.AgentEmission{}, agent.InvocationReceipt{}, errors.New("clarification reply must not use the ordinary Mio emission route")
}

func (generator *clarificationReplyGeneratorFake) GenerateClarificationReplyInstruction(_ context.Context, scope conversation.ConversationScope, snapshot agent.PromptSnapshot, input conversation.ClarificationReplyGenerationContext) (string, agent.InvocationReceipt, error) {
	generator.replyCalls++
	generator.scope = scope
	generator.snapshot = snapshot.Clone()
	generator.replyContext = input
	receipt := generator.receipt
	if receipt.Status == "" {
		receipt = agent.InvocationReceipt{RequestID: "reply-request", ResponseID: "reply-response", ProviderLabel: "mio-test", TokensUsed: 12, Status: "completed"}
		generator.receipt = receipt
	}
	return generator.instruction, receipt, generator.generateErr
}

type clarificationReplyStoreFake struct {
	context               StoredClarificationReplyContext
	contextErr            error
	lastContextRequest    ClarificationReplyContextRequest
	contextCalls          int
	outcome               ClarificationReplyContinuationResult
	outcomeFound          bool
	outcomeErr            error
	outcomeCalls          int
	lastCommit            ClarificationReplyContinuationInput
	commitCalls           int
	commitErr             error
	saveBeforeCommitError bool
	result                ClarificationReplyContinuationResult
}

func (store *clarificationReplyStoreFake) LoadClarificationReplyContext(_ context.Context, request ClarificationReplyContextRequest) (StoredClarificationReplyContext, error) {
	store.contextCalls++
	store.lastContextRequest = request
	return store.context, store.contextErr
}

func (store *clarificationReplyStoreFake) LoadClarificationReplyContinuationOutcome(_ context.Context, _ modulecore.TurnID) (ClarificationReplyContinuationResult, bool, error) {
	store.outcomeCalls++
	if store.outcomeErr != nil {
		return ClarificationReplyContinuationResult{}, false, store.outcomeErr
	}
	return store.outcome, store.outcomeFound, nil
}

func (store *clarificationReplyStoreFake) CommitClarificationReplyContinuation(_ context.Context, input ClarificationReplyContinuationInput) (ClarificationReplyContinuationResult, error) {
	store.commitCalls++
	store.lastCommit = input
	if store.result.OperationID == "" {
		stored := store.context
		stored.Handoff = store.context.Handoff
		store.result = clarificationReplyCommitResult(stored, input)
		store.result.InvocationReceipt = persistedClarificationReplyReceipt(input.InvocationReceipt)
	}
	if store.commitErr != nil {
		if store.saveBeforeCommitError {
			store.outcome, store.outcomeFound = store.result, true
		}
		return ClarificationReplyContinuationResult{}, store.commitErr
	}
	store.outcome, store.outcomeFound = store.result, true
	return store.result, nil
}

func persistedClarificationReplyReceipt(receipt agent.InvocationReceipt) agent.InvocationReceipt {
	return agent.InvocationReceipt{RequestID: receipt.RequestID, ResponseID: receipt.ResponseID, Status: receipt.Status}
}

func clarificationReplyContinuationInputForTest(fixture clarificationReplyRuntimeFixture, instruction string) ClarificationReplyContinuationInput {
	return ClarificationReplyContinuationInput{
		OperationID: clarificationReplyOperationPrefix + string(fixture.request.Accepted.TurnID), OwnerID: fixture.ownerID,
		SessionID: fixture.request.Session.SessionID, ReplyTurnID: fixture.request.Accepted.TurnID,
		PromptMessageID: fixture.stored.Receipt.PromptMessageID, DelegationID: fixture.stored.Handoff.DelegationID,
		Revision: fixture.stored.Handoff.Revision, GenerationEpoch: fixture.request.Accepted.GenerationEpoch,
		Instruction: instruction, InvocationReceipt: agent.InvocationReceipt{RequestID: "reply-request", ResponseID: "reply-response", ProviderLabel: "mio-test", TokensUsed: 12, Status: "completed"},
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ContextEventID: modulecore.NewEventID(), ReceiptEventID: modulecore.NewEventID(),
		OccurredAt: fixture.when.Add(time.Minute),
	}
}

func clarificationReplyCommitResult(stored StoredClarificationReplyContext, input ClarificationReplyContinuationInput) ClarificationReplyContinuationResult {
	next := stored.Handoff
	next.Revision = input.Revision + 1
	next.Instruction = input.Instruction
	next.InstructionMessageID = input.MessageID
	next.ContextSnapshotHash = strings.Repeat("b", 64)
	updatedContext := append(append([]DelegationDispatchMessage(nil), stored.Context...), DelegationDispatchMessage{
		MessageID: stored.AnswerMessage.MessageID, Role: "user", Text: stored.AnswerText,
	})
	contextEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.delegation_context", input.OccurredAt, map[string]any{
		"handoff": next, "context_snapshot_hash": next.ContextSnapshotHash, "messages": updatedContext,
	})
	contextEvent.TraceID, contextEvent.SessionID, contextEvent.TurnID, contextEvent.RequestID, contextEvent.EventSeq = stored.Handoff.SourceTraceID, input.SessionID, stored.OriginTurnID, input.DelegationID, 20
	instructionEvent := modulecore.NewRootEventEnvelope("delegation_mailbox", "conversation.delegation_instruction", input.OccurredAt, map[string]any{
		"kind": "delegation_instruction", "delegation_id": string(input.DelegationID), "delegation_revision": input.Revision + 1,
		"visibility": "private", "from": "mio", "source_actor_id": "mio", "to": stored.Handoff.ToActor,
		"content": input.Instruction, "text": input.Instruction,
	})
	instructionEvent.ActorKind, instructionEvent.ActorID = "agent", "mio"
	instructionEvent.TraceID, instructionEvent.CausationEventID, instructionEvent.SessionID = stored.Handoff.SourceTraceID, contextEvent.EventID, input.SessionID
	instructionEvent.TurnID, instructionEvent.RequestID, instructionEvent.MessageID, instructionEvent.EventSeq = stored.OriginTurnID, input.DelegationID, input.MessageID, 21
	message := MessageIndex{
		MessageID: input.MessageID, EventID: instructionEvent.EventID, SessionID: input.SessionID, TurnID: stored.OriginTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: stored.Handoff.ToActor, Kind: "delegation_instruction",
		ReplyToMessageID: stored.Handoff.ReplyTo, Visibility: "private",
	}
	receipt := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.mio_emission_receipt", input.OccurredAt, map[string]any{
		"status": input.InvocationReceipt.Status, "request_id": input.InvocationReceipt.RequestID, "response_id": input.InvocationReceipt.ResponseID,
	})
	receipt.TraceID, receipt.CausationEventID, receipt.SessionID, receipt.TurnID, receipt.EventSeq = stored.AnswerEvent.TraceID, stored.AnswerEvent.EventID, input.SessionID, input.ReplyTurnID, 22
	return ClarificationReplyContinuationResult{
		OperationID: input.OperationID, OwnerID: input.OwnerID, SessionID: input.SessionID, ReplyTurnID: input.ReplyTurnID,
		PromptMessageID: input.PromptMessageID, DelegationID: input.DelegationID, Revision: input.Revision, NextRevision: input.Revision + 1,
		ResultState: "awaiting_readback", InvocationReceipt: input.InvocationReceipt, ContextEvent: contextEvent,
		Event: instructionEvent, ReceiptEvent: receipt, Message: message,
		Events: []modulecore.EventEnvelope{contextEvent, instructionEvent, receipt}, MessageIndexes: []MessageIndex{message},
		NextHandoff: &next, DispatchQueued: true,
	}
}

func decodeClarificationReplyTestMessages(t *testing.T, raw any) []DelegationDispatchMessage {
	t.Helper()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal stored context messages: %v", err)
	}
	var messages []DelegationDispatchMessage
	if err := json.Unmarshal(encoded, &messages); err != nil {
		t.Fatalf("decode stored context messages: %v", err)
	}
	return messages
}

var _ ClarificationReplyInstructionGenerator = (*clarificationReplyGeneratorFake)(nil)
