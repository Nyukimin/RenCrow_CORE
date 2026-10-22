package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResponseFirstClarificationControlTurnPersistsAndReplaysDecision(t *testing.T) {
	for _, test := range []struct {
		name             string
		decision         string
		restartViaResume bool
	}{
		{name: "ask user", decision: `{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which deployment region should I use?"}`},
		{name: "continue delegation", decision: `{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"Use the staging region named in the original request."}`, restartViaResume: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			storePath := filepath.Join(t.TempDir(), "events.sqlite")
			store, err := eventstore.NewSQLiteStore(storePath)
			if err != nil {
				t.Fatalf("open EventStore: %v", err)
			}
			fixture := seedResponseFirstClarificationWithActor(t, store, responseFirstActorID)
			accepted, err := store.AcceptReadback(ctx, fixture.readback)
			if err != nil {
				t.Fatalf("AcceptReadback(needs_clarification): %v", err)
			}
			if accepted.Status != conversation.ReadbackStatusNeedsClarification || accepted.ControlTurn == nil || accepted.TaskStartQueued {
				t.Fatalf("clarification acceptance = %+v, want accepted Mio control Turn with no Task start", accepted)
			}

			provider := &responseFirstClarificationTestProvider{content: test.decision}
			readback := responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
				"shiro": {Provider: provider, SystemPrompt: "test Shiro readback prompt"},
			}}
			publisher := newResponseFirstTestPublisher()
			runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID, readback)
			if err != nil {
				t.Fatalf("build response-first runtime: %v", err)
			}
			controlTurn := *accepted.ControlTurn
			acceptedTurn := viewer.V2AcceptedTurn{
				SessionID: controlTurn.SessionID, TurnID: controlTurn.TurnID,
				TraceID: accepted.Event.TraceID, InputMessageID: controlTurn.InputMessageID,
				ClientSubmissionID: controlTurn.ClientSubmissionID, ActorID: controlTurn.ActorID,
			}
			if err := runtime.Process(ctx, acceptedTurn); err != nil {
				t.Fatalf("Process(clarification control Turn): %v", err)
			}
			first := receiveResponseFirstClarificationBatch(t, publisher)
			if provider.calls != 1 {
				t.Fatalf("Mio generation calls after first process = %d, want 1", provider.calls)
			}
			if len(first.Events) == 0 || first.TurnID != controlTurn.TurnID || first.SessionID != fixture.sessionID {
				t.Fatalf("published clarification batch = %+v, want the control Turn's durable events", first)
			}
			for _, event := range first.Events {
				if event.EventID == accepted.Event.EventID {
					t.Fatalf("published clarification outcome contains the private question event %s", event.EventID)
				}
			}
			stored, found, err := store.LoadClarificationDecisionOutcome(ctx, controlTurn.TurnID)
			if err != nil || !found {
				t.Fatalf("LoadClarificationDecisionOutcome() found=%t err=%v", found, err)
			}
			if !reflect.DeepEqual(first.Events, stored.Events) {
				t.Fatalf("published events = %#v, want exact durable cross-Turn receipt events %#v", first.Events, stored.Events)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close EventStore before restart: %v", err)
			}

			restartedStore, err := eventstore.NewSQLiteStore(storePath)
			if err != nil {
				t.Fatalf("reopen EventStore: %v", err)
			}
			defer func() {
				if err := restartedStore.Close(); err != nil {
					t.Errorf("close restarted EventStore: %v", err)
				}
			}()
			restartedPublisher := newResponseFirstTestPublisher()
			restartedRuntime, err := newResponseFirstConversationRuntime(restartedStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, restartedPublisher, responseFirstTestOwnerID, readback)
			if err != nil {
				t.Fatalf("build response-first runtime after restart: %v", err)
			}
			if test.restartViaResume {
				if err := restartedRuntime.ResumePendingAcceptedTurns(ctx, fixture.sessionID); err != nil {
					t.Fatalf("ResumePendingAcceptedTurns(committed continuation after restart): %v", err)
				}
			} else if err := restartedRuntime.Process(ctx, acceptedTurn); err != nil {
				t.Fatalf("Process(committed clarification after restart): %v", err)
			}
			replayed := receiveResponseFirstClarificationBatch(t, restartedPublisher)
			if provider.calls != 1 {
				t.Fatalf("Mio generation calls after restart replay = %d, want no regeneration", provider.calls)
			}
			if !reflect.DeepEqual(replayed, first) {
				t.Fatalf("restart replay = %#v, want exact original published receipt %#v", replayed, first)
			}
		})
	}
}

func TestResponseFirstClarificationAdaptersPreservePersistedIdentitiesAndReceipt(t *testing.T) {
	ctx := context.Background()
	sessionID, controlTurnID, originTurnID := modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewTurnID()
	delegationID := modulecore.NewRequestID()
	questionEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.clarification_question", time.Now().UTC(), map[string]any{"text": "Which region?"})
	questionEvent.SessionID, questionEvent.TurnID = sessionID, controlTurnID
	questionMessage := eventstore.MessageIndex{
		MessageID: modulecore.NewMessageID(), EventID: questionEvent.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: "shiro", ToKind: "agent", ToActor: "mio", Kind: "clarification_question", Visibility: "private",
	}
	questionEvent.MessageID = questionMessage.MessageID
	controlTurn := eventstore.AcceptedTurn{
		OperationID: "accept-clarification", ClientSubmissionID: "clarification:" + string(delegationID) + ":1",
		SessionID: sessionID, TurnID: controlTurnID, ActorID: "shiro", TriggerKind: "clarification_control",
		InputMessageID: questionMessage.MessageID, AdmissionSeq: 9, GenerationEpoch: 3, State: "accepted", InputEvent: questionEvent,
	}
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: originTurnID, OriginUserMessageID: modulecore.NewMessageID(),
		SessionID: sessionID, SourceTraceID: questionEvent.TraceID, FromActor: "mio", ToActor: "shiro",
		InstructionMessageID: modulecore.NewMessageID(), ContextSnapshotHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		AllowedCapabilities: []string{}, ReplyTo: modulecore.NewMessageID(), Goal: "Goal", Instruction: "Do work",
		ExpectedOutput: "Report",
	}
	contextMessageID := modulecore.NewMessageID()
	stored := eventstore.ClarificationControlContext{
		ControlTurn: controlTurn, OwnerID: responseFirstTestOwnerID, OriginUserActorID: responseFirstActorID, Handoff: handoff,
		QuestionEvent: questionEvent, QuestionMessage: questionMessage,
		Context: []eventstore.DelegationContextMessage{{MessageID: contextMessageID, Role: "user", Text: "Original request"}},
	}
	receipt := agent.InvocationReceipt{RequestID: "request-1", ResponseID: "response-1", ProviderLabel: "test", TokensUsed: 12, Status: "completed"}
	primary := modulecore.NewRootEventEnvelope("delegation_mailbox", "conversation.delegation_instruction", time.Now().UTC(), map[string]any{"text": "revised instruction"})
	primary.SessionID, primary.TurnID, primary.RequestID, primary.MessageID = sessionID, originTurnID, delegationID, modulecore.NewMessageID()
	contextEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.delegation_context", time.Now().UTC(), map[string]any{"handoff": handoff})
	contextEvent.SessionID, contextEvent.TurnID, contextEvent.RequestID = sessionID, originTurnID, delegationID
	receiptEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.mio_emission_receipt", time.Now().UTC(), map[string]any{"status": "completed"})
	receiptEvent.SessionID, receiptEvent.TurnID = sessionID, controlTurnID
	primaryMessage := eventstore.MessageIndex{
		MessageID: primary.MessageID, EventID: primary.EventID, SessionID: sessionID, TurnID: originTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: "shiro", Kind: "delegation_instruction", Visibility: "private",
	}
	committed := eventstore.ClarificationDecisionCommit{
		OperationID: "clarification.decision:" + string(controlTurnID), SessionID: sessionID, ControlTurnID: controlTurnID,
		DelegationID: delegationID, Revision: 1, Kind: conversation.ClarificationDecisionContinueDelegation,
		ResultState: "completed", InvocationReceipt: receipt, Event: primary, ReceiptEvent: receiptEvent,
		Message: primaryMessage, Events: []modulecore.EventEnvelope{contextEvent, primary, receiptEvent},
		MessageIndexes: []eventstore.MessageIndex{primaryMessage}, NextHandoff: &handoff, DispatchQueued: true,
	}
	store := &responseFirstClarificationAdapterStore{storedContext: stored, committed: committed, found: true}
	reader := responseFirstCanonicalReader{store: store}
	gotContext, err := reader.LoadClarificationControlContext(ctx, conversationruntime.ClarificationControlContextRequest{
		SessionID: sessionID, ControlTurnID: controlTurnID, OwnerID: responseFirstTestOwnerID, Assignee: "shiro",
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(): %v", err)
	}
	wantContext := conversationruntime.StoredClarificationControlContext{
		ControlTurn: toConversationAcceptedTurn(controlTurn), OwnerID: stored.OwnerID,
		OriginUserActorID: stored.OriginUserActorID, Handoff: handoff,
		QuestionEvent: questionEvent, QuestionMessage: toConversationMessageIndex(questionMessage),
		Context: []conversationruntime.DelegationDispatchMessage{{MessageID: contextMessageID, Role: "user", Text: "Original request"}},
	}
	if !reflect.DeepEqual(gotContext, wantContext) {
		t.Fatalf("adapted clarification context = %#v, want %#v", gotContext, wantContext)
	}

	decision := conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
		Instruction: "Use the original request.",
	}
	input := conversationruntime.ClarificationDecisionCommitInput{
		OperationID: "clarification.decision:" + string(controlTurnID), SessionID: sessionID, ControlTurnID: controlTurnID,
		DelegationID: delegationID, Revision: 1, GenerationEpoch: 3, Decision: decision, InvocationReceipt: receipt,
		EventID: primary.EventID, MessageID: primary.MessageID, ContextEventID: contextEvent.EventID,
		ReceiptEventID: receiptEvent.EventID, OccurredAt: primary.OccurredAt,
	}
	journal := responseFirstTurnJournal{store: store}
	gotCommit, err := journal.CommitClarificationDecision(ctx, input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(): %v", err)
	}
	wantInput := eventstore.ClarificationDecisionInput{
		OperationID: input.OperationID, SessionID: input.SessionID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision, GenerationEpoch: input.GenerationEpoch,
		Decision: input.Decision, InvocationReceipt: input.InvocationReceipt, EventID: input.EventID,
		MessageID: input.MessageID, ContextEventID: input.ContextEventID,
		ReceiptEventID: input.ReceiptEventID, OccurredAt: input.OccurredAt,
	}
	if !reflect.DeepEqual(store.commitInput, wantInput) {
		t.Fatalf("EventStore decision input = %#v, want %#v", store.commitInput, wantInput)
	}
	wantCommit := conversationruntime.ClarificationDecisionCommitResult{
		OperationID: committed.OperationID, ControlTurnID: committed.ControlTurnID, DelegationID: committed.DelegationID,
		Revision: committed.Revision, Kind: committed.Kind, ResultState: committed.ResultState,
		InvocationReceipt: committed.InvocationReceipt, Event: committed.Event, ReceiptEvent: committed.ReceiptEvent,
		Message: toConversationMessageIndex(committed.Message), NextHandoff: committed.NextHandoff,
		DispatchQueued: committed.DispatchQueued, Events: committed.Events,
		MessageIndexes: []conversationruntime.MessageIndex{toConversationMessageIndex(primaryMessage)},
	}
	if !reflect.DeepEqual(gotCommit, wantCommit) {
		t.Fatalf("adapted clarification commit = %#v, want %#v", gotCommit, wantCommit)
	}
	gotOutcome, found, err := journal.LoadClarificationDecisionOutcome(ctx, controlTurnID)
	if err != nil || !found || !reflect.DeepEqual(gotOutcome, wantCommit) {
		t.Fatalf("LoadClarificationDecisionOutcome() = %#v, found=%t err=%v, want %#v", gotOutcome, found, err, wantCommit)
	}

	terminal := committed
	terminal.Kind = ""
	terminal.TerminalReason = conversation.ClarificationTerminalInvalidOutput
	terminal.NextHandoff = nil
	terminal.DispatchQueued = false
	store.terminal = terminal
	terminalInput := conversationruntime.ClarificationTerminalCommitInput{
		OperationID: "clarification.terminal:" + string(controlTurnID), SessionID: sessionID, ControlTurnID: controlTurnID,
		DelegationID: delegationID, Revision: 1, GenerationEpoch: 3,
		Reason: conversation.ClarificationTerminalInvalidOutput, AttemptedKind: conversation.ClarificationDecisionContinueDelegation,
		InvocationReceipt: receipt, EventID: primary.EventID, MessageID: primary.MessageID,
		ReceiptEventID: receiptEvent.EventID, OccurredAt: primary.OccurredAt,
	}
	gotTerminal, err := journal.CommitClarificationTerminal(ctx, terminalInput)
	if err != nil {
		t.Fatalf("CommitClarificationTerminal(): %v", err)
	}
	wantTerminalInput := eventstore.ClarificationTerminalInput{
		OperationID: terminalInput.OperationID, SessionID: terminalInput.SessionID, ControlTurnID: terminalInput.ControlTurnID,
		DelegationID: terminalInput.DelegationID, Revision: terminalInput.Revision, GenerationEpoch: terminalInput.GenerationEpoch,
		Reason: terminalInput.Reason, AttemptedKind: terminalInput.AttemptedKind, InvocationReceipt: terminalInput.InvocationReceipt,
		EventID: terminalInput.EventID, MessageID: terminalInput.MessageID, ReceiptEventID: terminalInput.ReceiptEventID,
		OccurredAt: terminalInput.OccurredAt,
	}
	if !reflect.DeepEqual(store.terminalInput, wantTerminalInput) {
		t.Fatalf("EventStore terminal input = %#v, want %#v", store.terminalInput, wantTerminalInput)
	}
	wantTerminal := toConversationClarificationDecisionCommitResult(terminal)
	if !reflect.DeepEqual(gotTerminal, wantTerminal) {
		t.Fatalf("adapted terminal result = %#v, want %#v", gotTerminal, wantTerminal)
	}
}

func TestResponseFirstAcceptsClarificationReplyAsCanonicalReplyWithoutForegroundPublication(t *testing.T) {
	ctx := context.Background()
	store, l1 := openProjectionTestStores(t, t.TempDir())
	fixture := seedResponseFirstClarification(t, store)
	readback, err := store.AcceptReadback(ctx, fixture.readback)
	if err != nil {
		t.Fatalf("AcceptReadback(needs_clarification): %v", err)
	}
	control := *readback.ControlTurn
	prompt, err := store.CommitClarificationDecision(ctx, eventstore.ClarificationDecisionInput{
		OperationID: "ask-user-for-clarification:" + string(control.TurnID),
		SessionID:   fixture.sessionID, ControlTurnID: control.TurnID,
		DelegationID: fixture.readback.DelegationID, Revision: fixture.readback.Revision,
		GenerationEpoch: control.GenerationEpoch,
		Decision: conversation.ClarificationDecision{
			SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser,
			Question: "Which cloud region should I use?",
		},
		InvocationReceipt: agent.InvocationReceipt{RequestID: "request-clarification-question", ResponseID: "response-clarification-question", Status: "completed"},
		EventID:           modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		ReceiptEventID: modulecore.NewEventID(), OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CommitClarificationDecision(ask_user): %v", err)
	}

	publisher := newResponseFirstTestPublisher()
	runtime := newResponseFirstProjectionRuntime(t, store, l1, publisher, &responseFirstTestProvider{})
	request := responseFirstRequest(fixture.sessionID, "clarification-answer-submit", "Use ap-southeast-2.")
	request.ReplyToMessageID = prompt.Message.MessageID
	batch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(): %v", err)
	}
	turn, _, err := batch.FindOrBeginTurn(string(request.TurnID), string(request.TraceID))
	if err != nil {
		t.Fatalf("FindOrBeginTurn(): %v", err)
	}
	accepted, err := runtime.Accept(turntiming.WithBatchTurnRecorder(ctx, turn), request)
	if err != nil {
		t.Fatalf("Accept(clarification reply): %v", err)
	}
	pending, err := store.ReadPendingAcceptedTurns(ctx, fixture.sessionID, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(): %v", err)
	}
	var acceptedReply *eventstore.AcceptedTurn
	for index := range pending {
		if pending[index].TurnID == accepted.TurnID {
			acceptedReply = &pending[index]
			break
		}
	}
	if acceptedReply == nil {
		t.Fatalf("accepted clarification reply Turn %s is not pending: %+v", accepted.TurnID, pending)
	}
	if acceptedReply.TriggerKind != "clarification_reply" {
		t.Fatalf("accepted trigger kind=%q, want clarification_reply", acceptedReply.TriggerKind)
	}
	stored, err := store.LoadClarificationReplyContext(ctx, eventstore.ClarificationReplyContextInput{
		SessionID: fixture.sessionID, ReplyTurnID: accepted.TurnID, OwnerID: responseFirstTestOwnerID,
	})
	if err != nil {
		t.Fatalf("LoadClarificationReplyContext(): %v", err)
	}
	if stored.AnswerEvent.Payload["reply_to_message_id"] != string(prompt.Message.MessageID) ||
		stored.AnswerMessage.ReplyToMessageID != prompt.Message.MessageID || stored.AnswerText != request.Message {
		t.Fatalf("stored accepted answer does not preserve canonical reply identity: event=%+v index=%+v text=%q", stored.AnswerEvent, stored.AnswerMessage, stored.AnswerText)
	}
	pendingProjections, err := store.ListUnmarkedClarificationReplyProjections(ctx, responseFirstTestOwnerID, 1)
	if err != nil || len(pendingProjections) != 1 {
		t.Fatalf("ListUnmarkedClarificationReplyProjections() = %d items, err=%v; want the accepted answer in the durable lane", len(pendingProjections), err)
	}
	if pendingProjections[0].Event.Payload["source_actor_id"] != responseFirstActorID {
		t.Fatalf("canonical reply Actor=%v, want trusted Viewer Actor %q (distinct from owner %q)", pendingProjections[0].Event.Payload["source_actor_id"], responseFirstActorID, responseFirstTestOwnerID)
	}
	if err := validateResponseFirstClarificationReplyProjection(pendingProjections[0], responseFirstTestOwnerID); err != nil {
		t.Fatalf("validate accepted reply projection with distinct owner and Actor: %v", err)
	}
	workID := eventstore.ConversationProjectionWorkID(pendingProjections[0].SourceEventID)
	runtime.projection.children.mu.Lock()
	child := runtime.projection.children.entries[workID]
	childCount := len(runtime.projection.children.entries)
	runtime.projection.children.mu.Unlock()
	if child == nil || childCount != 1 {
		t.Fatalf("pre-commit acceptance child registry has %d entries and exact work child=%t, want 1 exact durable reply child", childCount, child != nil)
	}

	// A retry creates a fresh provisional envelope ID while the stable
	// EventStore receipt resolves to the original accepted answer.
	retryBatch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(retry): %v", err)
	}
	retryTurn, _, err := retryBatch.FindOrBeginTurn(string(request.TurnID), string(request.TraceID))
	if err != nil {
		t.Fatalf("FindOrBeginTurn(retry): %v", err)
	}
	retried, err := runtime.Accept(turntiming.WithBatchTurnRecorder(ctx, retryTurn), request)
	if err != nil || retried.TurnID != accepted.TurnID {
		t.Fatalf("Accept(idempotent clarification reply retry) = %+v, %v; want original receipt %+v", retried, err, accepted)
	}
	runtime.projection.children.mu.Lock()
	child = runtime.projection.children.entries[workID]
	childCount = len(runtime.projection.children.entries)
	runtime.projection.children.mu.Unlock()
	if child == nil || childCount != 1 {
		t.Fatalf("idempotent retry changed reply child registry: entries=%d original child=%t; want only the original durable work identity", childCount, child != nil)
	}

	// Reusing a submission identity for different content is a definitive
	// rejection. The provisional projection child must be abandoned, while the
	// timing record still reports incomplete work tracking for the rejected
	// request.
	conflictingRequest := request
	conflictingRequest.Message = "Use a different cloud region."
	conflictBatch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(conflict): %v", err)
	}
	conflictTurn, _, err := conflictBatch.FindOrBeginTurn(string(conflictingRequest.TurnID), string(conflictingRequest.TraceID))
	if err != nil {
		t.Fatalf("FindOrBeginTurn(conflict): %v", err)
	}
	if _, err := runtime.Accept(turntiming.WithBatchTurnRecorder(ctx, conflictTurn), conflictingRequest); !errors.Is(err, viewer.ErrV2SubmissionConflict) {
		t.Fatalf("Accept(conflicting clarification reply) error=%v, want submission conflict", err)
	}
	if !conflictTurn.FinishForeground() {
		t.Fatal("FinishForeground(conflict) failed")
	}
	if err := conflictBatch.FreezeIncomplete(); err != nil {
		t.Fatalf("FreezeIncomplete(conflict): %v", err)
	}
	conflictSnapshot, err := conflictBatch.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(conflict): %v", err)
	}
	conflictTurnSnapshot, found := conflictSnapshot.Turn(0)
	if !found || !conflictSnapshot.IsIncomplete() || !conflictTurnSnapshot.WorkTrackingIncomplete() || conflictTurnSnapshot.PendingChildren() != 0 {
		t.Fatalf("conflict timing snapshot incomplete=%t found=%t work_incomplete=%t pending_children=%d; want explicit incomplete with no leaked provisional child", conflictSnapshot.IsIncomplete(), found, conflictTurnSnapshot.WorkTrackingIncomplete(), conflictTurnSnapshot.PendingChildren())
	}
	runtime.projection.children.mu.Lock()
	conflictChildCount := len(runtime.projection.children.entries)
	runtime.projection.children.mu.Unlock()
	if conflictChildCount != 1 {
		t.Fatalf("definitive conflict left %d registered projection children, want only the original accepted work", conflictChildCount)
	}

	select {
	case event := <-publisher.accepted:
		t.Fatalf("clarification answer was published in the foreground: %+v", event)
	default:
	}
	if len(runtime.projection.wake) != 1 {
		t.Fatalf("projection wake queue depth=%d, want one wake after durable reply acceptance", len(runtime.projection.wake))
	}
}

func TestResponseFirstProcessesAndResumesCommittedClarificationReplyOutcomeFirst(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, l1 := openProjectionTestStores(t, root)
	fixture := seedResponseFirstClarification(t, store)
	readback, err := store.AcceptReadback(ctx, fixture.readback)
	if err != nil {
		t.Fatalf("AcceptReadback(needs_clarification): %v", err)
	}
	provider := &responseFirstClarificationReplyFlowProvider{contents: []string{
		`{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which deployment region should I use?"}`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Use the explicitly selected staging region and preserve the existing deployment constraints."}`,
	}}
	publisher := newResponseFirstTestPublisher()
	readbackWiring := responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
		"shiro": {Provider: provider, SystemPrompt: "test Shiro readback prompt"},
	}}
	runtime, err := newResponseFirstConversationRuntimeWithProjection(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID, l1, readbackWiring,
	)
	if err != nil {
		t.Fatalf("build response-first projection runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	control := *readback.ControlTurn
	controlAccepted := viewer.V2AcceptedTurn{
		SessionID: control.SessionID, TurnID: control.TurnID, TraceID: readback.Event.TraceID,
		InputMessageID: control.InputMessageID, ClientSubmissionID: control.ClientSubmissionID, ActorID: control.ActorID,
	}
	if err := runtime.Process(ctx, controlAccepted); err != nil {
		t.Fatalf("Process(clarification control): %v", err)
	}
	controlBundle := receiveResponseFirstClarificationBatch(t, publisher)

	request := responseFirstRequest(fixture.sessionID, "clarification-reply-process-replay", "Use ap-southeast-2.")
	// The public prompt is produced by Mio's decision commit, not the private
	// assignee question that created the control Turn.
	prompt, promptFound, err := store.LoadClarificationDecisionOutcome(ctx, control.TurnID)
	if err != nil || !promptFound || prompt.Message.MessageID == "" {
		t.Fatalf("LoadClarificationDecisionOutcome() prompt=%+v found=%t err=%v", prompt, promptFound, err)
	}
	request.ReplyToMessageID = prompt.Message.MessageID
	accepted, err := runtime.Accept(ctx, request)
	if err != nil {
		t.Fatalf("Accept(clarification reply): %v", err)
	}
	if err := runtime.Process(ctx, accepted); err != nil {
		t.Fatalf("Process(accepted clarification reply): %v", err)
	}
	firstReplyBundle := receiveResponseFirstClarificationBatch(t, publisher)
	if provider.calls != 2 {
		t.Fatalf("Mio generation calls after control and reply = %d, want exactly 2", provider.calls)
	}
	committedReply, found, err := store.LoadClarificationReplyContinuationOutcome(ctx, accepted.TurnID)
	if err != nil || !found || committedReply.ResultState != "awaiting_readback" || !reflect.DeepEqual(firstReplyBundle.Events, committedReply.Events) {
		t.Fatalf("reply continuation outcome found=%t err=%v state=%q published=%#v durable=%#v",
			found, err, committedReply.ResultState, firstReplyBundle.Events, committedReply.Events)
	}
	for _, event := range committedReply.Events {
		if event.EventType == "agent.response" || event.ComponentID == "orchestrator" ||
			strings.EqualFold(responseFirstEventPayloadString(event, "visibility"), "public") {
			t.Fatalf("clarification reply continuation exposed public output: %+v", event)
		}
	}

	if err := runtime.Process(ctx, accepted); err != nil {
		t.Fatalf("Process(committed clarification reply retry): %v", err)
	}
	replayedReplyBundle := receiveResponseFirstClarificationBatch(t, publisher)
	if !reflect.DeepEqual(replayedReplyBundle, firstReplyBundle) || provider.calls != 2 {
		t.Fatalf("reply outcome-first replay=%#v, want %#v with no regeneration (calls=%d)", replayedReplyBundle, firstReplyBundle, provider.calls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close EventStore before reply recovery: %v", err)
	}
	restartedStore, err := eventstore.NewSQLiteStore(filepath.Join(root, "event-store.sqlite"))
	if err != nil {
		t.Fatalf("reopen EventStore for reply recovery: %v", err)
	}
	t.Cleanup(func() {
		if err := restartedStore.Close(); err != nil {
			t.Errorf("close restarted EventStore: %v", err)
		}
	})
	restartedPublisher := newResponseFirstTestPublisher()
	restartedRuntime, err := newResponseFirstConversationRuntimeWithProjection(
		restartedStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, restartedPublisher, responseFirstTestOwnerID, l1, readbackWiring,
	)
	if err != nil {
		t.Fatalf("build restarted response-first projection runtime: %v", err)
	}
	allowResponseFirstTestReservations(restartedRuntime)
	if err := restartedRuntime.ResumePendingAcceptedTurns(ctx, fixture.sessionID); err != nil {
		t.Fatalf("ResumePendingAcceptedTurns(after reply continuation commit): %v", err)
	}
	resumedReply := receiveResponseFirstClarificationBatch(t, restartedPublisher)
	if !reflect.DeepEqual(resumedReply, firstReplyBundle) || provider.calls != 2 {
		t.Fatalf("restart reply replay=%#v calls=%d; want exact stored continuation and no regeneration (control receipt=%#v)", resumedReply, provider.calls, controlBundle)
	}
}

func TestResponseFirstClarificationReplyJournalAdaptersPreservePersistedIdentity(t *testing.T) {
	ctx := context.Background()
	sessionID, replyTurnID, controlTurnID, originTurnID := modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewTurnID(), modulecore.NewTurnID()
	delegationID, promptMessageID := modulecore.NewRequestID(), modulecore.NewMessageID()
	answerEvent := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", time.Now().UTC(), map[string]any{"reply_to_message_id": string(promptMessageID)})
	answerEvent.SessionID, answerEvent.TurnID, answerEvent.MessageID = sessionID, replyTurnID, modulecore.NewMessageID()
	answerIndex := eventstore.MessageIndex{
		MessageID: answerEvent.MessageID, EventID: answerEvent.EventID, SessionID: sessionID, TurnID: replyTurnID,
		FromActor: responseFirstActorID, ToKind: "agent", ToActor: "mio", Kind: "user_message",
		ReplyToMessageID: promptMessageID, Visibility: "public",
	}
	promptEvent := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", time.Now().UTC(), map[string]any{"kind": "clarification_prompt"})
	promptEvent.SessionID, promptEvent.TurnID, promptEvent.MessageID = sessionID, controlTurnID, promptMessageID
	promptIndex := eventstore.MessageIndex{
		MessageID: promptMessageID, EventID: promptEvent.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: "mio", ToKind: "user", ToActor: responseFirstTestOwnerID, Kind: "clarification_prompt", Visibility: "public",
	}
	questionEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.clarification_question", time.Now().UTC(), map[string]any{"text": "Which region?"})
	questionEvent.SessionID, questionEvent.TurnID = sessionID, controlTurnID
	questionIndex := eventstore.MessageIndex{
		MessageID: modulecore.NewMessageID(), EventID: questionEvent.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: "shiro", ToKind: "agent", ToActor: "mio", Kind: "clarification_question", Visibility: "private",
	}
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 2, OriginTurnID: originTurnID, SessionID: sessionID,
		SourceTraceID: answerEvent.TraceID, FromActor: "mio", ToActor: "shiro", Goal: "Deploy safely",
		Instruction: "Use the staging region", ExpectedOutput: "Deployment report",
	}
	reply := eventstore.AcceptedTurn{
		OperationID: "accept-reply", ClientSubmissionID: "reply-submit", SessionID: sessionID,
		TurnID: replyTurnID, ActorID: responseFirstActorID, TriggerKind: responseFirstClarificationReplyTriggerKind,
		InputMessageID: answerEvent.MessageID, AdmissionSeq: 19, GenerationEpoch: 4, State: "accepted", InputEvent: answerEvent,
	}
	control := eventstore.AcceptedTurn{
		OperationID: "accept-control", ClientSubmissionID: "control-submit", SessionID: sessionID,
		TurnID: controlTurnID, ActorID: "shiro", TriggerKind: "clarification_control",
		InputMessageID: questionIndex.MessageID, AdmissionSeq: 17, GenerationEpoch: 4, State: "awaiting_readback", InputEvent: questionEvent,
	}
	stored := eventstore.ClarificationReplyContext{
		Reply: reply, Receipt: eventstore.ClarificationReplyReceipt{
			PromptMessageID: promptMessageID, SourceTurnID: originTurnID, DelegationID: delegationID,
			Revision: 2, ReplyTurnID: replyTurnID, ReplyMessageID: answerEvent.MessageID,
			OwnerID: responseFirstTestOwnerID, SessionID: sessionID,
		},
		AnswerEvent: answerEvent, AnswerMessage: answerIndex, PromptEvent: promptEvent, PromptMessage: promptIndex,
		ControlTurn: control, QuestionEvent: questionEvent, QuestionMessage: questionIndex,
		Handoff: handoff, OriginTurnID: originTurnID,
		Context:    []eventstore.DelegationContextMessage{{MessageID: modulecore.NewMessageID(), Role: "user", Text: "Original request"}},
		AnswerText: "Use ap-southeast-2.",
	}
	contextReplyEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.delegation_context", time.Now().UTC(), nil)
	continuationEvent := modulecore.NewRootEventEnvelope("delegation_mailbox", "conversation.delegation_instruction", time.Now().UTC(), nil)
	receiptEvent := modulecore.NewRootEventEnvelope("conversation_runtime", "conversation.clarification_reply_receipt", time.Now().UTC(), nil)
	continuationIndex := eventstore.MessageIndex{
		MessageID: modulecore.NewMessageID(), EventID: continuationEvent.EventID, SessionID: sessionID, TurnID: originTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: "shiro", Kind: "delegation_instruction", Visibility: "private",
	}
	continuation := eventstore.ClarificationReplyContinuationCommit{
		OperationID: "clarification.reply:" + string(replyTurnID), OwnerID: responseFirstTestOwnerID,
		SessionID: sessionID, ReplyTurnID: replyTurnID, PromptMessageID: promptMessageID,
		DelegationID: delegationID, Revision: 2, NextRevision: 3, ResultState: "awaiting_readback",
		ContextEvent: contextReplyEvent, Event: continuationEvent, ReceiptEvent: receiptEvent, Message: continuationIndex,
		Events:         []modulecore.EventEnvelope{contextReplyEvent, continuationEvent, receiptEvent},
		MessageIndexes: []eventstore.MessageIndex{continuationIndex}, NextHandoff: &handoff, DispatchQueued: true,
	}
	store := &responseFirstClarificationReplyAdapterStore{storedContext: stored, continuation: continuation, found: true}
	journal := responseFirstTurnJournal{store: store}

	request := conversationruntime.ClarificationReplyContextRequest{SessionID: sessionID, ReplyTurnID: replyTurnID, OwnerID: responseFirstTestOwnerID}
	gotContext, err := journal.LoadClarificationReplyContext(ctx, request)
	if err != nil {
		t.Fatalf("LoadClarificationReplyContext(): %v", err)
	}
	wantContext := conversationruntime.StoredClarificationReplyContext{
		Reply: toConversationAcceptedTurn(reply), OwnerID: responseFirstTestOwnerID,
		Receipt: conversationruntime.ClarificationReplyReceipt{
			PromptMessageID: promptMessageID, SourceTurnID: originTurnID, DelegationID: delegationID, Revision: 2,
		},
		AnswerEvent: answerEvent, AnswerMessage: toConversationMessageIndex(answerIndex),
		PromptEvent: promptEvent, PromptMessage: toConversationMessageIndex(promptIndex),
		ControlTurn: toConversationAcceptedTurn(control), QuestionEvent: questionEvent,
		QuestionMessage: toConversationMessageIndex(questionIndex), Handoff: handoff, OriginTurnID: originTurnID,
		Context:    []conversationruntime.DelegationDispatchMessage{{MessageID: stored.Context[0].MessageID, Role: "user", Text: "Original request"}},
		AnswerText: stored.AnswerText,
	}
	if !reflect.DeepEqual(gotContext, wantContext) || store.contextInput != (eventstore.ClarificationReplyContextInput{
		SessionID: sessionID, ReplyTurnID: replyTurnID, OwnerID: responseFirstTestOwnerID,
	}) {
		t.Fatalf("adapted reply context=%#v input=%#v; want %#v", gotContext, store.contextInput, wantContext)
	}

	gotOutcome, found, err := journal.LoadClarificationReplyContinuationOutcome(ctx, replyTurnID)
	if err != nil || !found || !reflect.DeepEqual(gotOutcome, toConversationClarificationReplyContinuationResult(continuation)) || store.outcomeTurnID != replyTurnID {
		t.Fatalf("LoadClarificationReplyContinuationOutcome()=(%#v,%t,%v) queried=%s; want mapped continuation", gotOutcome, found, err, store.outcomeTurnID)
	}
	input := conversationruntime.ClarificationReplyContinuationInput{
		OperationID: continuation.OperationID, OwnerID: responseFirstTestOwnerID, SessionID: sessionID,
		ReplyTurnID: replyTurnID, PromptMessageID: promptMessageID, DelegationID: delegationID,
		Revision: 2, GenerationEpoch: 4, Instruction: "Use ap-southeast-2 and report the result.",
		InvocationReceipt: agent.InvocationReceipt{RequestID: "request-2", ResponseID: "response-2", Status: "completed"},
		EventID:           continuationEvent.EventID, MessageID: continuationIndex.MessageID,
		ContextEventID: contextReplyEvent.EventID, ReceiptEventID: receiptEvent.EventID, OccurredAt: time.Now().UTC(),
	}
	if _, err := journal.CommitClarificationReplyContinuation(ctx, input); err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(): %v", err)
	}
	wantInput := eventstore.ClarificationReplyContinuationInput{
		OperationID: input.OperationID, OwnerID: input.OwnerID, SessionID: input.SessionID,
		ReplyTurnID: input.ReplyTurnID, PromptMessageID: input.PromptMessageID, DelegationID: input.DelegationID,
		Revision: input.Revision, GenerationEpoch: input.GenerationEpoch, Instruction: input.Instruction,
		InvocationReceipt: input.InvocationReceipt, EventID: input.EventID, MessageID: input.MessageID,
		ContextEventID: input.ContextEventID, ReceiptEventID: input.ReceiptEventID, OccurredAt: input.OccurredAt,
	}
	if !reflect.DeepEqual(store.continuationInput, wantInput) {
		t.Fatalf("EventStore reply continuation input=%#v, want %#v", store.continuationInput, wantInput)
	}
}

type responseFirstClarificationReplyAdapterStore struct {
	responseFirstJournalStore
	storedContext     eventstore.ClarificationReplyContext
	contextInput      eventstore.ClarificationReplyContextInput
	continuation      eventstore.ClarificationReplyContinuationCommit
	continuationInput eventstore.ClarificationReplyContinuationInput
	outcomeTurnID     modulecore.TurnID
	found             bool
}

func (s *responseFirstClarificationReplyAdapterStore) LoadClarificationReplyContext(_ context.Context, input eventstore.ClarificationReplyContextInput) (eventstore.ClarificationReplyContext, error) {
	s.contextInput = input
	return s.storedContext, nil
}

func (s *responseFirstClarificationReplyAdapterStore) LoadClarificationReplyContinuationOutcome(_ context.Context, turnID modulecore.TurnID) (eventstore.ClarificationReplyContinuationCommit, bool, error) {
	s.outcomeTurnID = turnID
	return s.continuation, s.found, nil
}

func (s *responseFirstClarificationReplyAdapterStore) CommitClarificationReplyContinuation(_ context.Context, input eventstore.ClarificationReplyContinuationInput) (eventstore.ClarificationReplyContinuationCommit, error) {
	s.continuationInput = input
	return s.continuation, nil
}

func TestResponseFirstJournalReleasesProjectionChildrenOnlyAfterConfirmedNoCommit(t *testing.T) {
	for _, operation := range []string{"bundle", "clarification_decision", "clarification_terminal"} {
		for _, uncertain := range []bool{false, true} {
			name := "confirmed_no_work"
			if uncertain {
				name = "outcome_lookup_uncertain"
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				store := &responseFirstProjectionCommitErrorStore{commitErr: errors.New("injected commit failure")}
				if uncertain {
					store.outcomeErr = errors.New("injected outcome lookup failure")
				}
				children := newResponseFirstProjectionChildren()
				journal := responseFirstTurnJournal{store: store, projectionChildren: children}
				batch, err := turntiming.NewBatchRecorder()
				if err != nil {
					t.Fatalf("NewBatchRecorder(): %v", err)
				}
				sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
				turn, _, err := batch.FindOrBeginTurn(string(turnID), string(modulecore.NewTraceID()))
				if err != nil {
					t.Fatalf("FindOrBeginTurn(): %v", err)
				}
				trackedContext := turntiming.WithBatchTurnRecorder(ctx, turn)
				workID := "projection:" + string(modulecore.NewEventID())
				switch operation {
				case "bundle":
					_, err = journal.CommitTurnBundle(trackedContext, conversationruntime.CommitBundle{
						OperationID: "commit-failing-bundle", SessionID: sessionID, TurnID: turnID,
						GenerationEpoch: 1, ResultState: "completed",
						Outbox: []conversationruntime.WorkItem{{
							WorkID: workID, DedupeKey: workID, Kind: responseFirstProjectionWorkKind,
							SourceEventID: modulecore.NewEventID(), SessionID: sessionID,
						}},
					})
				case "clarification_decision":
					_, err = journal.CommitClarificationDecision(trackedContext, conversationruntime.ClarificationDecisionCommitInput{
						OperationID: "commit-failing-clarification", SessionID: sessionID,
						ControlTurnID: turnID, EventID: modulecore.EventID(strings.TrimPrefix(workID, "projection:")),
					})
				case "clarification_terminal":
					_, err = journal.CommitClarificationTerminal(trackedContext, conversationruntime.ClarificationTerminalCommitInput{
						OperationID: "commit-failing-terminal", SessionID: sessionID,
						ControlTurnID: turnID, EventID: modulecore.EventID(strings.TrimPrefix(workID, "projection:")),
					})
				}
				if err == nil {
					t.Fatal("commit unexpectedly succeeded")
				}
				if !turn.FinishForeground() {
					t.Fatal("FinishForeground() failed")
				}
				batch.CloseAdmission()
				if uncertain {
					if err := batch.FreezeIncomplete(); err != nil {
						t.Fatalf("FreezeIncomplete(): %v", err)
					}
					snapshot, err := batch.Snapshot()
					if err != nil || !snapshot.IsIncomplete() {
						t.Fatalf("uncertain commit snapshot incomplete=%t err=%v, want explicit incomplete", snapshot.IsIncomplete(), err)
					}
					if got := children.entryCount(); got != 1 {
						t.Fatalf("projection children after uncertain commit=%d, want retained child", got)
					}
					return
				}
				waitCtx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				if err := batch.WaitDrained(waitCtx); err != nil {
					t.Fatalf("WaitDrained() after confirmed no work: %v", err)
				}
				if err := batch.Freeze(); err != nil {
					t.Fatalf("Freeze(): %v", err)
				}
				snapshot, err := batch.Snapshot()
				if err != nil || snapshot.IsIncomplete() {
					t.Fatalf("confirmed no-work snapshot incomplete=%t err=%v, want complete", snapshot.IsIncomplete(), err)
				}
				if got := children.entryCount(); got != 0 {
					t.Fatalf("projection children after confirmed no work=%d, want none", got)
				}
			})
		}
	}
}

type responseFirstProjectionCommitErrorStore struct {
	responseFirstJournalStore
	commitErr  error
	outcomeErr error
}

func (s *responseFirstProjectionCommitErrorStore) CommitTurnBundle(context.Context, eventstore.CommitInput) (eventstore.CommittedBatch, error) {
	return eventstore.CommittedBatch{}, s.commitErr
}

func (s *responseFirstProjectionCommitErrorStore) LoadCommittedOutcome(context.Context, modulecore.TurnID) (eventstore.CommittedBatch, bool, error) {
	return eventstore.CommittedBatch{}, false, s.outcomeErr
}

func (s *responseFirstProjectionCommitErrorStore) CommitClarificationDecision(context.Context, eventstore.ClarificationDecisionInput) (eventstore.ClarificationDecisionCommit, error) {
	return eventstore.ClarificationDecisionCommit{}, s.commitErr
}

func (s *responseFirstProjectionCommitErrorStore) CommitClarificationTerminal(context.Context, eventstore.ClarificationTerminalInput) (eventstore.ClarificationDecisionCommit, error) {
	return eventstore.ClarificationDecisionCommit{}, s.commitErr
}

func (s *responseFirstProjectionCommitErrorStore) LoadClarificationDecisionOutcome(context.Context, modulecore.TurnID) (eventstore.ClarificationDecisionCommit, bool, error) {
	return eventstore.ClarificationDecisionCommit{}, false, s.outcomeErr
}

func (r *responseFirstProjectionChildren) entryCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

type responseFirstClarificationAdapterStore struct {
	responseFirstJournalStore
	storedContext eventstore.ClarificationControlContext
	commitInput   eventstore.ClarificationDecisionInput
	committed     eventstore.ClarificationDecisionCommit
	terminalInput eventstore.ClarificationTerminalInput
	terminal      eventstore.ClarificationDecisionCommit
	found         bool
}

func (s *responseFirstClarificationAdapterStore) LoadClarificationControlContext(_ context.Context, _ eventstore.ClarificationControlContextInput) (eventstore.ClarificationControlContext, error) {
	return s.storedContext, nil
}

func (s *responseFirstClarificationAdapterStore) CommitClarificationDecision(_ context.Context, input eventstore.ClarificationDecisionInput) (eventstore.ClarificationDecisionCommit, error) {
	s.commitInput = input
	return s.committed, nil
}

func (s *responseFirstClarificationAdapterStore) CommitClarificationTerminal(_ context.Context, input eventstore.ClarificationTerminalInput) (eventstore.ClarificationDecisionCommit, error) {
	s.terminalInput = input
	return s.terminal, nil
}

func (s *responseFirstClarificationAdapterStore) LoadClarificationDecisionOutcome(_ context.Context, _ modulecore.TurnID) (eventstore.ClarificationDecisionCommit, bool, error) {
	return s.committed, s.found, nil
}

type responseFirstClarificationTestProvider struct {
	content string
	calls   int
}

type responseFirstClarificationReplyFlowProvider struct {
	contents []string
	calls    int
}

func (p *responseFirstClarificationReplyFlowProvider) GenerateConversation(_ context.Context, _ conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.calls >= len(p.contents) {
		return llm.GenerateResponse{}, errors.New("unexpected additional clarification flow generation")
	}
	content := p.contents[p.calls]
	p.calls++
	return llm.GenerateResponse{
		Content: content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 7,
	}, nil
}

func (p *responseFirstClarificationReplyFlowProvider) Name() string {
	return "test-clarification-reply-flow"
}

func (p *responseFirstClarificationTestProvider) GenerateConversation(_ context.Context, _ conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{
		Content: p.content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 7,
	}, nil
}

func (p *responseFirstClarificationTestProvider) Name() string { return "test-clarification" }

func receiveResponseFirstClarificationBatch(t *testing.T, publisher *responseFirstTestPublisher) conversationruntime.CommittedBundle {
	t.Helper()
	select {
	case batch := <-publisher.committed:
		return batch
	case <-time.After(3 * time.Second):
		t.Fatal("clarification result was not published")
		return conversationruntime.CommittedBundle{}
	}
}

type responseFirstClarificationFixture struct {
	sessionID modulecore.SessionID
	readback  eventstore.ReadbackInput
}

func seedResponseFirstClarification(t *testing.T, store *eventstore.SQLiteStore) responseFirstClarificationFixture {
	return seedResponseFirstClarificationWithActor(t, store, responseFirstTestOwnerID)
}

func seedResponseFirstClarificationWithActor(t *testing.T, store *eventstore.SQLiteStore, actorID string) responseFirstClarificationFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	sessionID, originTurnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	userInput := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", now, map[string]any{
		"from": "user", "source_actor_id": actorID, "to": "mio",
		"content": "Please deploy the service to the staging environment.", "text": "Please deploy the service to the staging environment.", "visibility": "public",
	})
	userInput.SessionID, userInput.TurnID, userInput.MessageID = sessionID, originTurnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(ctx, eventstore.AcceptInput{
		OperationID: "accept-clarification-input:" + string(originTurnID), OwnerID: responseFirstTestOwnerID,
		RuntimeVersion: responseFirstRuntimeVer, ClientSubmissionID: "clarification-source:" + string(originTurnID),
		ActorID: actorID, TriggerKind: responseFirstTriggerKind, GenerationEpoch: 1, CreateNewSession: true,
		InputEvent: userInput,
		InputMessage: eventstore.MessageIndex{
			MessageID: userInput.MessageID, EventID: userInput.EventID, SessionID: sessionID, TurnID: originTurnID,
			FromActor: actorID, ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	}); err != nil {
		t.Fatalf("AcceptTurn(source): %v", err)
	}

	delegationID, revision, assignee := modulecore.NewRequestID(), uint64(1), "shiro"
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
		DelegationID: delegationID, Revision: revision, OriginTurnID: originTurnID,
		OriginUserMessageID: userInput.MessageID, SessionID: sessionID, SourceTraceID: userInput.TraceID,
		FromActor: "mio", ToActor: assignee, InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: hex.EncodeToString(contextDigest[:]), AllowedCapabilities: []string{},
		ReplyTo: userInput.MessageID, Goal: "Deploy the service safely", Instruction: "Prepare the staging deployment and report its result.",
		Constraints:    []conversation.AgentConstraint{{Key: "staging_only", Text: "Use staging only.", SourceMessageIDs: []modulecore.MessageID{userInput.MessageID}}},
		ExpectedOutput: "A concise deployment report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	notice := modulecore.NewEventEnvelope(userInput.TraceID, userInput.EventID, nil, "orchestrator", "agent.response", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "I asked Shiro to deploy it.", "text": "I asked Shiro to deploy it.", "visibility": "public",
	})
	notice.SessionID, notice.TurnID, notice.MessageID = sessionID, originTurnID, modulecore.NewMessageID()
	contextEvent := modulecore.NewEventEnvelope(userInput.TraceID, userInput.EventID, nil, "conversation_runtime", "conversation.delegation_context", now, map[string]any{
		"source_revision": "1", "cutover_revision": "0", "messages": contextMessages,
		"context_snapshot_hash": handoff.ContextSnapshotHash, "handoff": handoff,
	})
	contextEvent.SessionID, contextEvent.TurnID = sessionID, originTurnID
	instruction := modulecore.NewEventEnvelope(userInput.TraceID, notice.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": assignee, "text": handoff.Instruction,
	})
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, originTurnID, handoff.InstructionMessageID
	instruction.ActorKind, instruction.ActorID = "agent", "mio"
	if _, err := store.CommitTurnBundle(ctx, eventstore.CommitInput{
		OperationID: "commit-clarification-handoff:" + string(originTurnID), SessionID: sessionID,
		TurnID: originTurnID, GenerationEpoch: 1, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, contextEvent, instruction},
		MessageIndexes: []eventstore.MessageIndex{
			{MessageID: notice.MessageID, EventID: notice.EventID, SessionID: sessionID, TurnID: originTurnID, FromActor: "mio", ToKind: "user", ToActor: responseFirstTestOwnerID, Kind: "delegation_notice", Visibility: "public"},
			{MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: sessionID, TurnID: originTurnID, FromActor: "mio", ToKind: "agent", ToActor: assignee, Kind: "delegation_instruction", Visibility: "private"},
		},
		Delegation: &eventstore.DelegationRecord{
			DelegationID: string(delegationID), Revision: int64(revision), OriginTurnID: originTurnID,
			Assignee: assignee, InstructionMessageID: handoff.InstructionMessageID, ContextEventID: contextEvent.EventID, State: "dispatch_accepted",
		},
		Outbox: []eventstore.WorkItem{{
			WorkID: "dispatch-clarification:" + string(originTurnID), DedupeKey: "delegation:" + string(delegationID) + ":1:start",
			Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: sessionID, Priority: 5,
		}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(handoff): %v", err)
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: delegationID, Revision: revision, Sender: assignee, SchemaVersion: "agent.readback.v1",
		Status: conversation.ReadbackStatusNeedsClarification, UnderstoodGoal: handoff.Goal,
		ExpectedOutput: handoff.ExpectedOutput, Text: "Which cloud region should I use for the staging deployment?",
	}
	return responseFirstClarificationFixture{
		sessionID: sessionID,
		readback: eventstore.ReadbackInput{
			OperationID: "readback-clarification:" + string(originTurnID), DelegationID: delegationID, Revision: revision,
			Assignee: assignee, Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: now,
		},
	}
}

func userInputText(event modulecore.EventEnvelope) string {
	text, _ := event.Payload["text"].(string)
	return text
}
