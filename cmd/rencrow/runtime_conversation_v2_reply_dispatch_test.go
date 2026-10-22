package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResponseFirstReplyContinuationDispatchesRevisionTwoAndQueuesTaskStart(t *testing.T) {
	ctx := context.Background()
	store, l1 := openProjectionTestStores(t, t.TempDir())
	fixture := seedReplyDispatchClarification(t, store)

	mio := &replyDispatchConversationProvider{contents: []string{
		`{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which deployment region should I use?"}`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Use the explicitly selected staging region and preserve the existing deployment constraints."}`,
	}}
	readback := &replyDispatchConversationProvider{contents: []string{
		replyDispatchReadbackJSON(string(conversation.ReadbackStatusAccepted), []string{"staging_only"}),
	}}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntimeWithProjection(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), mio, publisher, responseFirstTestOwnerID, l1,
		responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
			"shiro": {Provider: readback, SystemPrompt: "test Shiro readback prompt"},
		}},
	)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}

	initialWork, err := store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil || len(initialWork) != 1 || initialWork[0].DedupeKey != "delegation:"+string(fixture.readback.DelegationID)+":1:start" {
		t.Fatalf("ClaimWork(initial dispatch) = %+v, err=%v; want the seeded revision-1 work item", initialWork, err)
	}
	first, err := store.AcceptReadback(ctx, fixture.readback)
	if err != nil || first.Status != conversation.ReadbackStatusNeedsClarification || first.ControlTurn == nil || first.TaskStartQueued {
		t.Fatalf("AcceptReadback(revision 1) = %+v, %v; want a durable clarification control Turn", first, err)
	}
	if err := store.AckWork(ctx, eventstore.WorkLeaseInput{
		WorkID: initialWork[0].WorkID, LeaseToken: initialWork[0].LeaseToken, Version: initialWork[0].Version, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AckWork(initial dispatch after durable clarification seed): %v", err)
	}
	if responseFirstTestOwnerID == responseFirstActorID {
		t.Fatal("test must keep the session owner and authenticated Viewer actor distinct")
	}

	control, found := replyDispatchPendingTurn(t, store, fixture.sessionID, "clarification_control")
	if !found {
		t.Fatal("accepted needs_clarification readback did not persist the Mio control Turn")
	}
	controlAccepted := viewer.V2AcceptedTurn{
		SessionID: control.SessionID, TurnID: control.TurnID, TraceID: control.InputEvent.TraceID,
		InputMessageID: control.InputMessageID, ClientSubmissionID: control.ClientSubmissionID, ActorID: control.ActorID,
	}
	if err := runtime.Process(ctx, controlAccepted); err != nil {
		t.Fatalf("Process(clarification control Turn): %v", err)
	}
	drainReplyDispatchPublisher(publisher)
	decision, found, err := store.LoadClarificationDecisionOutcome(ctx, control.TurnID)
	if err != nil || !found || decision.Kind != conversation.ClarificationDecisionAskUser || decision.Message.MessageID == "" {
		t.Fatalf("LoadClarificationDecisionOutcome() = %+v, found=%t, err=%v; want persisted public clarification prompt", decision, found, err)
	}

	request := responseFirstRequest(fixture.sessionID, "reply-dispatch-user-answer", "Use ap-southeast-2.")
	request.ReplyToMessageID = decision.Message.MessageID
	acceptedReply, err := runtime.Accept(ctx, request)
	if err != nil {
		t.Fatalf("Accept(canonical clarification answer): %v", err)
	}
	if acceptedReply.ActorID != responseFirstActorID {
		t.Fatalf("accepted answer actor=%q, want %q", acceptedReply.ActorID, responseFirstActorID)
	}
	if err := runtime.Process(ctx, acceptedReply); err != nil {
		t.Fatalf("Process/Commit(canonical clarification answer): %v", err)
	}
	drainReplyDispatchPublisher(publisher)
	replyOutcome, found, err := store.LoadClarificationReplyContinuationOutcome(ctx, acceptedReply.TurnID)
	if err != nil || !found || replyOutcome.NextHandoff == nil || replyOutcome.NextHandoff.Revision != 2 {
		t.Fatalf("LoadClarificationReplyContinuationOutcome() = %+v, found=%t, err=%v; want durable revision-2 continuation", replyOutcome, found, err)
	}
	if replyOutcome.OwnerID != responseFirstTestOwnerID || replyOutcome.ReceiptEvent.CausationEventID == "" {
		t.Fatalf("reply continuation receipt owner/cause = %q/%q; want durable owner and answer-event cause", replyOutcome.OwnerID, replyOutcome.ReceiptEvent.CausationEventID)
	}
	contextMessages := replyDispatchContextMessages(t, replyOutcome.ContextEvent)
	tamperedHandoff := *replyOutcome.NextHandoff
	tamperedHandoff.ContextSnapshotHash = strings.Repeat("f", 64)
	var tamperedResponseTo *conversationruntime.DelegationDispatchReplySource
	if err := validateResponseFirstAcceptedReplyContext(
		ctx, store, tamperedHandoff, replyOutcome.ContextEvent, replyOutcome.Event, contextMessages, true, &tamperedResponseTo,
	); err == nil {
		t.Fatal("accepted clarification reply receipt authorized an arbitrary context snapshot hash")
	}

	dispatched, err := runtime.dispatcher.DispatchOne(ctx)
	if err != nil {
		t.Fatalf("DispatchOne(revision 2 after canonical clarification answer): %v", err)
	}
	if !dispatched.Claimed || !dispatched.Accepted || !dispatched.Published || !dispatched.Acknowledged ||
		dispatched.Revision != 2 || dispatched.Status != conversation.ReadbackStatusAccepted {
		t.Fatalf("DispatchOne(revision 2) = %+v; want accepted, published, acknowledged revision-2 readback", dispatched)
	}
	if readback.calls != 1 {
		t.Fatalf("readback provider calls = %d, want one actual revision-2 acceptance", readback.calls)
	}

	started, err := store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil || len(started) != 1 {
		t.Fatalf("ClaimWork(start_delegated_task) = %d items, err=%v; want the durable task-start item from revision 2", len(started), err)
	}
	if started[0].Assignee != "shiro" || started[0].SessionID != fixture.sessionID ||
		started[0].DedupeKey != "delegation:"+string(replyOutcome.NextHandoff.DelegationID)+":2:start_task" {
		t.Fatalf("revision-2 task-start work = %+v; want the accepted Shiro delegation", started[0])
	}
}

func seedReplyDispatchClarification(t *testing.T, store *eventstore.SQLiteStore) replyDispatchClarificationFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	sessionID, originTurnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	userInput := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", now, map[string]any{
		"from": "user", "source_actor_id": responseFirstActorID, "to": "mio",
		"content": "Please deploy the service to the staging environment.", "text": "Please deploy the service to the staging environment.", "visibility": "public",
	})
	userInput.SessionID, userInput.TurnID, userInput.MessageID = sessionID, originTurnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(ctx, eventstore.AcceptInput{
		OperationID: "accept-reply-dispatch-input:" + string(originTurnID), OwnerID: responseFirstTestOwnerID,
		RuntimeVersion: responseFirstRuntimeVer, ClientSubmissionID: "reply-dispatch-source:" + string(originTurnID),
		ActorID: responseFirstActorID, TriggerKind: responseFirstTriggerKind, GenerationEpoch: 1, CreateNewSession: true,
		InputEvent: userInput,
		InputMessage: eventstore.MessageIndex{
			MessageID: userInput.MessageID, EventID: userInput.EventID, SessionID: sessionID, TurnID: originTurnID,
			FromActor: responseFirstActorID, ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	}); err != nil {
		t.Fatalf("AcceptTurn(source): %v", err)
	}

	delegationID := modulecore.NewRequestID()
	contextMessages := []eventstore.DelegationContextMessage{{MessageID: userInput.MessageID, Role: "user", Text: userInputText(userInput)}}
	contextJSON, err := json.Marshal(struct {
		SourceRevision  string                                `json:"source_revision"`
		CutoverRevision string                                `json:"cutover_revision"`
		Messages        []eventstore.DelegationContextMessage `json:"messages"`
	}{SourceRevision: "1", CutoverRevision: "0", Messages: contextMessages})
	if err != nil {
		t.Fatalf("marshal handoff context: %v", err)
	}
	contextDigest := sha256.Sum256(contextJSON)
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: originTurnID,
		OriginUserMessageID: userInput.MessageID, SessionID: sessionID, SourceTraceID: userInput.TraceID,
		FromActor: "mio", ToActor: "shiro", InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: hex.EncodeToString(contextDigest[:]), AllowedCapabilities: []string{},
		ReplyTo: userInput.MessageID, Goal: "Deploy the service safely", Instruction: "Prepare the staging deployment and report its result.",
		Constraints:    []conversation.AgentConstraint{{Key: "staging_only", Text: "Use staging only.", SourceMessageIDs: []modulecore.MessageID{userInput.MessageID}}},
		ExpectedOutput: "A concise deployment report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	notice := modulecore.NewEventEnvelope(userInput.TraceID, userInput.EventID, nil, "orchestrator", "agent.response", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "I asked Shiro to deploy it.", "text": "I asked Shiro to deploy it.",
		"kind": "delegation_notice", "visibility": "public", "delegation_id": string(delegationID), "delegation_revision": uint64(1),
	})
	notice.SessionID, notice.TurnID, notice.MessageID = sessionID, originTurnID, modulecore.NewMessageID()
	contextEvent := modulecore.NewEventEnvelope(userInput.TraceID, userInput.EventID, nil, "conversation_runtime", "conversation.delegation_context", now, map[string]any{
		"source_revision": "1", "cutover_revision": "0", "messages": contextMessages,
		"context_snapshot_hash": handoff.ContextSnapshotHash, "handoff": handoff,
	})
	contextEvent.SessionID, contextEvent.TurnID = sessionID, originTurnID
	instruction := modulecore.NewEventEnvelope(userInput.TraceID, notice.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": handoff.ToActor, "text": handoff.Instruction,
	})
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, originTurnID, handoff.InstructionMessageID
	instruction.ActorKind, instruction.ActorID = "agent", "mio"
	if _, err := store.CommitTurnBundle(ctx, eventstore.CommitInput{
		OperationID: "commit-reply-dispatch-handoff:" + string(originTurnID), SessionID: sessionID,
		TurnID: originTurnID, GenerationEpoch: 1, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, contextEvent, instruction},
		MessageIndexes: []eventstore.MessageIndex{
			{MessageID: notice.MessageID, EventID: notice.EventID, SessionID: sessionID, TurnID: originTurnID, FromActor: "mio", ToKind: "user", ToActor: responseFirstTestOwnerID, Kind: "delegation_notice", Visibility: "public"},
			{MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: sessionID, TurnID: originTurnID, FromActor: "mio", ToKind: "agent", ToActor: handoff.ToActor, Kind: "delegation_instruction", Visibility: "private"},
		},
		Delegation: &eventstore.DelegationRecord{
			DelegationID: string(delegationID), Revision: 1, OriginTurnID: originTurnID,
			Assignee: handoff.ToActor, InstructionMessageID: handoff.InstructionMessageID, ContextEventID: contextEvent.EventID, State: "dispatch_accepted",
		},
		Outbox: []eventstore.WorkItem{{
			WorkID: "dispatch-reply:" + string(originTurnID), DedupeKey: "delegation:" + string(delegationID) + ":1:start",
			Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: sessionID, Priority: 5,
		}, {
			WorkID:    eventstore.ConversationProjectionWorkID(notice.EventID),
			DedupeKey: "conversation_projection:" + string(notice.EventID),
			Kind:      responseFirstProjectionWorkKind, SourceEventID: notice.EventID, SessionID: sessionID, Priority: 10,
		}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(handoff): %v", err)
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: delegationID, Revision: 1, Sender: handoff.ToActor, SchemaVersion: "agent.readback.v1",
		Status: conversation.ReadbackStatusNeedsClarification, UnderstoodGoal: handoff.Goal,
		ExpectedOutput: handoff.ExpectedOutput, Text: "Which cloud region should I use for staging?",
	}
	return replyDispatchClarificationFixture{
		sessionID: sessionID,
		readback: eventstore.ReadbackInput{
			OperationID: "readback-reply-dispatch:" + string(originTurnID), DelegationID: delegationID, Revision: 1,
			Assignee: handoff.ToActor, Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: now,
		},
	}
}

type replyDispatchClarificationFixture struct {
	sessionID modulecore.SessionID
	readback  eventstore.ReadbackInput
}

func replyDispatchPendingTurn(t *testing.T, store *eventstore.SQLiteStore, sessionID modulecore.SessionID, triggerKind string) (eventstore.AcceptedTurn, bool) {
	t.Helper()
	turns, err := store.ReadPendingAcceptedTurns(context.Background(), sessionID, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(): %v", err)
	}
	var result eventstore.AcceptedTurn
	found := false
	for _, turn := range turns {
		if turn.TriggerKind != triggerKind {
			continue
		}
		if found {
			t.Fatalf("multiple pending %s Turns found", triggerKind)
		}
		result, found = turn, true
	}
	return result, found
}

func replyDispatchContextMessages(t *testing.T, event modulecore.EventEnvelope) []eventstore.DelegationContextMessage {
	t.Helper()
	encoded, err := json.Marshal(event.Payload["messages"])
	if err != nil {
		t.Fatalf("marshal persisted reply context messages: %v", err)
	}
	var messages []eventstore.DelegationContextMessage
	if err := json.Unmarshal(encoded, &messages); err != nil {
		t.Fatalf("decode persisted reply context messages: %v", err)
	}
	return messages
}

func drainReplyDispatchPublisher(publisher *responseFirstTestPublisher) {
	for {
		select {
		case <-publisher.committed:
		case <-publisher.accepted:
		default:
			return
		}
	}
}

func replyDispatchReadbackJSON(status string, acknowledged []string) string {
	if acknowledged == nil {
		acknowledged = []string{}
	}
	encoded, _ := json.Marshal(struct {
		SchemaVersion              string   `json:"schema_version"`
		Status                     string   `json:"status"`
		UnderstoodGoal             string   `json:"understood_goal"`
		AcknowledgedConstraintKeys []string `json:"acknowledged_constraint_keys"`
		ExpectedOutput             string   `json:"expected_output"`
		Text                       string   `json:"text"`
	}{
		SchemaVersion: "agent.readback.v1", Status: status,
		UnderstoodGoal: "Deploy the service safely", AcknowledgedConstraintKeys: acknowledged,
		ExpectedOutput: "A concise deployment report", Text: "I understand the requested deployment work.",
	})
	return string(encoded)
}

type replyDispatchConversationProvider struct {
	contents []string
	calls    int
}

func (p *replyDispatchConversationProvider) GenerateConversation(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.calls >= len(p.contents) {
		return llm.GenerateResponse{}, context.Canceled
	}
	content := p.contents[p.calls]
	p.calls++
	return llm.GenerateResponse{Content: content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 7}, nil
}

func (p *replyDispatchConversationProvider) Name() string { return "reply-dispatch-test" }
