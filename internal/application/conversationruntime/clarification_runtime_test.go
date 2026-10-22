package conversationruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildReadViewLoadsBoundClarificationContextFromStore(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which environment?",
	})
	reader := &clarificationTailReader{fakeTailReader: &fakeTailReader{tail: fixture.tail}, context: fixture.context}
	service := NewReadViewService(reader)

	view, err := service.BuildReadView(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("BuildReadView(): %v", err)
	}
	if reader.contextCalls != 1 || reader.contextRequest != (ClarificationControlContextRequest{
		SessionID: fixture.request.Session.SessionID, ControlTurnID: fixture.request.Accepted.TurnID,
		OwnerID: fixture.request.Session.OwnerID, Assignee: fixture.request.Accepted.ActorID,
	}) {
		t.Fatalf("clarification context request = %+v, calls=%d", reader.contextRequest, reader.contextCalls)
	}
	if reader.afterSeq != fixture.request.Session.CutoverSeq {
		t.Fatalf("canonical tail starts after %d, want clarification context from cutover %d", reader.afterSeq, fixture.request.Session.CutoverSeq)
	}
	if view.ClarificationControl == nil || !reflect.DeepEqual(view.ClarificationControl.Handoff, fixture.context.Handoff) ||
		view.ClarificationControl.QuestionText != "private worker question" || len(view.Messages) == 0 ||
		view.Messages[len(view.Messages)-1].MessageID != string(fixture.request.Accepted.InputMessageID) {
		t.Fatalf("clarification read view = %+v, want bound private control context and accepted question last", view)
	}
}

func TestBuildReadViewFailsClosedWhenClarificationContextDoesNotMatchAcceptedQuestion(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which environment?",
	})
	reader := &clarificationTailReader{fakeTailReader: &fakeTailReader{tail: fixture.tail}, context: fixture.context}
	reader.context.QuestionEvent.Payload["text"] = "changed private question"

	if _, err := NewReadViewService(reader).BuildReadView(context.Background(), fixture.request); !errors.Is(err, ErrReadViewInvalid) {
		t.Fatalf("BuildReadView(changed stored question) error = %v, want ErrReadViewInvalid", err)
	}
}

func TestRunAcceptedTurnHandlesClarificationDecisionWithoutPublishingPrivateQuestion(t *testing.T) {
	for _, decision := range []conversation.ClarificationDecision{
		{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?"},
		{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation, Instruction: "Use the staging cluster named in the earlier request."},
	} {
		t.Run(string(decision.Kind), func(t *testing.T) {
			fixture := newClarificationRuntimeFixture(t, decision)
			generatedReceipt := agent.InvocationReceipt{
				RequestID: "request-clarification", ResponseID: "response-clarification",
				ProviderLabel: "test-provider", TokensUsed: 42, Status: "completed",
			}
			generator := &clarificationDecisionGeneratorFake{decision: decision, receipt: generatedReceipt}
			journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID}
			publisher := &fakePublisher{}
			runtime, err := NewRuntime(RuntimeDependencies{
				ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
				Generator: generator, Journal: journal, Publisher: publisher,
				Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
			})
			if err != nil {
				t.Fatalf("NewRuntime(): %v", err)
			}

			result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
			if result.Status != TurnStatusDelivered || !result.Committed {
				t.Fatalf("RunAcceptedTurn() = %+v, want committed clarification decision", result)
			}
			if generator.clarificationCalls != 1 || generator.emissionCalls != 0 {
				t.Fatalf("generator calls = clarification:%d emission:%d, want one typed decision and no normal emission", generator.clarificationCalls, generator.emissionCalls)
			}
			if generator.scope.Purpose != conversation.PurposeClarificationControl || generator.scope.ActorID != fixture.request.Accepted.ActorID ||
				generator.control.QuestionMessageID != fixture.request.Accepted.InputMessageID || generator.control.QuestionText != "private worker question" {
				t.Fatalf("clarification generation scope/context = %+v / %+v", generator.scope, generator.control)
			}
			if promptHasText(generator.snapshot, "private worker question") {
				t.Fatal("private question was duplicated into the canonical prompt instead of remaining typed control context")
			}
			if !promptHasText(generator.snapshot, "original user request") || generator.allowlist.AllowedSourceMessageIDs[len(generator.allowlist.AllowedSourceMessageIDs)-1] == fixture.request.Accepted.InputMessageID {
				t.Fatalf("clarification prompt/allowlist did not preserve prior context while removing the private control input: %+v", generator.snapshot)
			}
			if journal.commitCalls != 1 || journal.lastInput.OperationID != "clarification.decision:"+string(fixture.request.Accepted.TurnID) ||
				journal.lastInput.Decision != decision || journal.lastInput.InvocationReceipt != generatedReceipt || journal.loadCalls != 1 {
				t.Fatalf("clarification journal calls/input = %d/%d %+v", journal.commitCalls, journal.loadCalls, journal.lastInput)
			}
			if publisher.publishCount() != 1 || len(result.Batch.Events) == 0 || result.Batch.TurnID != fixture.request.Accepted.TurnID {
				t.Fatalf("published clarification result = %+v, batches=%d", result.Batch, publisher.publishCount())
			}
			for _, event := range result.Batch.Events {
				if strings.Contains(eventPayloadText(event), "private worker question") {
					t.Fatalf("private assignee question reached committed output: %+v", event)
				}
			}
			if decision.Kind == conversation.ClarificationDecisionContinueDelegation {
				if result.ResultState != "awaiting_readback" || len(result.Batch.Events) != 3 ||
					result.Batch.Events[1].TurnID != fixture.context.Handoff.OriginTurnID ||
					result.Batch.Events[2].EventType != "conversation.mio_emission_receipt" || result.Batch.Events[2].TurnID != fixture.request.Accepted.TurnID {
					t.Fatalf("continuation result = %+v, want complete cross-Turn handoff batch", result)
				}
			} else if result.ResultState != "completed" || len(result.Batch.Events) != 2 ||
				result.Batch.Events[0].EventType != "agent.response" ||
				result.Batch.Events[0].TurnID != fixture.request.Accepted.TurnID ||
				result.Batch.Events[1].EventType != "conversation.mio_emission_receipt" {
				t.Fatalf("ask-user result = %+v, want only the committed public clarification prompt", result)
			}
		})
	}
}

func TestRunAcceptedTurnCommitsClarificationTerminalForGenerationErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		reason      conversation.ClarificationTerminalReason
		status      string
		decision    conversation.ClarificationDecision
		generateErr error
	}{
		{
			name: "generation error", reason: conversation.ClarificationTerminalGenerationError,
			status: "generation_failed", generateErr: errors.New("provider details must stay private"),
		},
		{
			name: "provider returned invalid output", reason: conversation.ClarificationTerminalInvalidOutput,
			status: "invalid_output", generateErr: errors.New("raw malformed output must stay private"),
		},
		{
			name: "runtime rejects invalid typed output", reason: conversation.ClarificationTerminalInvalidOutput,
			status: "completed", decision: conversation.ClarificationDecision{SchemaVersion: "wrong", Kind: conversation.ClarificationDecisionAskUser, Question: ""},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClarificationRuntimeFixture(t, test.decision)
			generator := &clarificationDecisionGeneratorFake{
				decision: test.decision, receipt: agent.InvocationReceipt{RequestID: "request-terminal", ResponseID: "response-terminal", Status: test.status},
				generateErr: test.generateErr,
			}
			journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID}
			publisher := &fakePublisher{}
			runtime, err := NewRuntime(RuntimeDependencies{
				ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
				Generator: generator, Journal: journal, Publisher: publisher,
				Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
			})
			if err != nil {
				t.Fatalf("NewRuntime(): %v", err)
			}

			result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
			if result.Status != TurnStatusDelivered || !result.Committed || result.ResultState != "completed" ||
				generator.clarificationCalls != 1 || journal.commitCalls != 0 || journal.terminalCalls != 1 || publisher.publishCount() != 1 {
				t.Fatalf("clarification terminal result=%+v generate=%d decisionCommit=%d terminalCommit=%d publish=%d",
					result, generator.clarificationCalls, journal.commitCalls, journal.terminalCalls, publisher.publishCount())
			}
			wantReceiptStatus, _ := clarificationTerminalStatus(test.reason)
			if journal.terminalInput.Reason != test.reason || journal.terminalInput.InvocationReceipt.Status != wantReceiptStatus ||
				journal.terminalInput.OperationID != "clarification.decision:"+string(fixture.request.Accepted.TurnID) {
				t.Fatalf("terminal input = %+v", journal.terminalInput)
			}
			if len(result.Batch.Events) != 2 || payloadString(result.Batch.Events[0].Payload, "kind") != "turn_error" ||
				payloadString(result.Batch.Events[0].Payload, "text") != conversation.ClarificationTerminalUserText ||
				strings.Contains(eventPayloadText(result.Batch.Events[0]), "must stay private") {
				t.Fatalf("terminal output exposed provider error or has wrong shape: %+v", result.Batch.Events)
			}
		})
	}
}

func TestRunAcceptedTurnDoesNotTerminalizeCallerCancellationDuringGeneration(t *testing.T) {
	decision := conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which region?",
	}
	fixture := newClarificationRuntimeFixture(t, decision)
	journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID}
	ctx, cancel := context.WithCancel(context.Background())
	generator := &clarificationDecisionGeneratorFake{
		decision: decision, receipt: agent.InvocationReceipt{Status: "generation_failed"},
		generateErr: context.Canceled, cancel: cancel,
	}
	publisher := &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(ctx, AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusFailedBeforeCommit || !errors.Is(result.Cause, context.Canceled) || journal.terminalCalls != 0 ||
		journal.commitCalls != 0 || generator.clarificationCalls != 1 || publisher.publishCount() != 0 {
		t.Fatalf("caller cancellation became a durable terminal: result=%+v generate=%d decisionCommit=%d terminalCommit=%d publish=%d",
			result, generator.clarificationCalls, journal.commitCalls, journal.terminalCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnCommitsRevisionLimitAsTerminal(t *testing.T) {
	decision := conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
		Instruction: "Continue with the additional detail.",
	}
	fixture := newClarificationRuntimeFixture(t, decision)
	fixture.context.Handoff.Revision = conversation.MaxClarificationRevision
	fixture.context.ControlTurn.InputEvent.Payload["delegation_revision"] = conversation.MaxClarificationRevision
	fixture.context.QuestionEvent.Payload["delegation_revision"] = conversation.MaxClarificationRevision
	fixture.request.Accepted.InputEvent.Payload["delegation_revision"] = conversation.MaxClarificationRevision
	fixture.view.ClarificationControl.Handoff.Revision = conversation.MaxClarificationRevision
	fixture.view.ClarificationControl.QuestionRevision = conversation.MaxClarificationRevision
	generator := &clarificationDecisionGeneratorFake{decision: decision, receipt: agent.InvocationReceipt{Status: "completed"}}
	journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID}
	publisher := &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || !result.Committed || result.ResultState != "completed" ||
		generator.clarificationCalls != 1 || journal.commitCalls != 0 || journal.terminalCalls != 1 || publisher.publishCount() != 1 {
		t.Fatalf("revision-limit result=%+v generation=%d decisionCommit=%d terminalCommit=%d publish=%d",
			result, generator.clarificationCalls, journal.commitCalls, journal.terminalCalls, publisher.publishCount())
	}
	if journal.terminalInput.Reason != conversation.ClarificationTerminalRevisionLimit ||
		journal.terminalInput.AttemptedKind != conversation.ClarificationDecisionContinueDelegation ||
		journal.terminalInput.Revision != conversation.MaxClarificationRevision {
		t.Fatalf("revision-limit terminal input = %+v", journal.terminalInput)
	}
}

func TestRunAcceptedTurnDoesNotTerminalizeAnUncertainDecisionCommit(t *testing.T) {
	decision := conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?",
	}
	fixture := newClarificationRuntimeFixture(t, decision)
	journal := &clarificationDecisionJournalFake{
		context: fixture.context, ownerID: fixture.request.Session.OwnerID,
		commitErr: errors.New("decision commit result is unknown"), commitWithoutOutcome: true,
	}
	generator, publisher := &clarificationDecisionGeneratorFake{decision: decision}, &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusCommitUncertain || result.Committed || journal.commitCalls != 1 || journal.terminalCalls != 0 ||
		generator.clarificationCalls != 1 || publisher.publishCount() != 0 {
		t.Fatalf("uncertain decision commit was terminalized: result=%+v generation=%d decisionCommit=%d terminalCommit=%d publish=%d",
			result, generator.clarificationCalls, journal.commitCalls, journal.terminalCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnReplaysClarificationTerminalWithoutGenerating(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{})
	terminalInput := ClarificationTerminalCommitInput{
		OperationID: clarificationDecisionOperationPrefix + string(fixture.request.Accepted.TurnID),
		SessionID:   fixture.request.Session.SessionID, ControlTurnID: fixture.request.Accepted.TurnID,
		DelegationID: fixture.context.Handoff.DelegationID, Revision: fixture.context.Handoff.Revision,
		GenerationEpoch: fixture.request.Accepted.GenerationEpoch, Reason: conversation.ClarificationTerminalInvalidOutput,
		InvocationReceipt: agent.InvocationReceipt{RequestID: "request-terminal", ResponseID: "response-terminal", Status: "invalid_output"},
		EventID:           modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(), OccurredAt: fixture.now,
	}
	journal := &clarificationDecisionJournalFake{
		context: fixture.context, ownerID: fixture.request.Session.OwnerID,
		result: clarificationTerminalCommitResult(fixture.context, fixture.request.Session.OwnerID, terminalInput), resultFound: true,
	}
	generator, publisher := &clarificationEmissionOnlyGeneratorFake{}, &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || !result.Committed || result.ResultState != "completed" || generator.calls != 0 ||
		journal.commitCalls != 0 || journal.terminalCalls != 0 || journal.loadCalls != 1 || publisher.publishCount() != 1 ||
		!reflect.DeepEqual(result.Batch.Events, journal.result.Events) {
		t.Fatalf("stored clarification terminal was not replayed exactly: result=%+v generated=%d decisionCommit=%d terminalCommit=%d loaded=%d published=%d",
			result, generator.calls, journal.commitCalls, journal.terminalCalls, journal.loadCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnLeavesUnknownClarificationTerminalCommitUncertain(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{})
	terminalErr := errors.New("terminal commit result is unknown")
	journal := &clarificationDecisionJournalFake{
		context: fixture.context, ownerID: fixture.request.Session.OwnerID,
		terminalErr: terminalErr, commitWithoutOutcome: true,
	}
	generator := &clarificationDecisionGeneratorFake{
		receipt: agent.InvocationReceipt{Status: "generation_failed"}, generateErr: errors.New("provider failed"),
	}
	publisher := &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusCommitUncertain || result.Committed || journal.terminalCalls != 1 || journal.loadCalls != 2 ||
		generator.clarificationCalls != 1 || publisher.publishCount() != 0 {
		t.Fatalf("unknown terminal commit was retried or published: result=%+v generation=%d terminalCommit=%d load=%d publish=%d",
			result, generator.clarificationCalls, journal.terminalCalls, journal.loadCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnFailsClosedForClarificationWithoutValidatedContext(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which environment?",
	})
	fixture.view.ClarificationControl = nil
	generator := &clarificationDecisionGeneratorFake{decision: conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which environment?",
	}}
	journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID}
	publisher := &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusFailedBeforeCommit || result.Cause == nil || generator.clarificationCalls != 0 || journal.commitCalls != 0 || publisher.publishCount() != 0 {
		t.Fatalf("missing validated context crossed decision boundary: result=%+v generation=%d commit=%d publish=%d", result, generator.clarificationCalls, journal.commitCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnReplaysClarificationReceiptWithoutGenerating(t *testing.T) {
	for _, decision := range []conversation.ClarificationDecision{
		{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?"},
		{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation, Instruction: "Use the staging cluster named earlier."},
	} {
		t.Run(string(decision.Kind), func(t *testing.T) {
			fixture := newClarificationRuntimeFixture(t, decision)
			input := ClarificationDecisionCommitInput{
				OperationID: clarificationDecisionOperationPrefix + string(fixture.request.Accepted.TurnID),
				SessionID:   fixture.request.Session.SessionID, ControlTurnID: fixture.request.Accepted.TurnID,
				DelegationID: fixture.context.Handoff.DelegationID, Revision: fixture.context.Handoff.Revision,
				GenerationEpoch: fixture.request.Accepted.GenerationEpoch, Decision: decision,
				InvocationReceipt: agent.InvocationReceipt{RequestID: "request-1", ResponseID: "response-1", Status: "completed"},
				EventID:           modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(), OccurredAt: fixture.now,
			}
			if decision.Kind == conversation.ClarificationDecisionContinueDelegation {
				input.ContextEventID = modulecore.NewEventID()
			}
			journal := &clarificationDecisionJournalFake{
				context: fixture.context, ownerID: fixture.request.Session.OwnerID,
				result: clarificationCommitResult(fixture.context, fixture.request.Session.OwnerID, input), resultFound: true,
			}
			generator, publisher := &clarificationEmissionOnlyGeneratorFake{}, &fakePublisher{}
			runtime, err := NewRuntime(RuntimeDependencies{
				ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
				Generator: generator, Journal: journal, Publisher: publisher,
				Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
			})
			if err != nil {
				t.Fatalf("NewRuntime(): %v", err)
			}

			result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
			if result.Status != TurnStatusDelivered || !result.Committed || generator.calls != 0 || journal.commitCalls != 0 ||
				journal.loadCalls != 1 || publisher.publishCount() != 1 || !reflect.DeepEqual(result.Batch.Events, journal.result.Events) {
				t.Fatalf("stored clarification receipt was not replayed exactly: result=%+v generated=%d committed=%d loaded=%d published=%d",
					result, generator.calls, journal.commitCalls, journal.loadCalls, publisher.publishCount())
			}
		})
	}
}

func TestRunAcceptedTurnRecoversClarificationAfterUnknownCommitWithoutRegeneration(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?",
	})
	decision := conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?",
	}
	commitErr := errors.New("commit response was lost")
	journal := &clarificationDecisionJournalFake{context: fixture.context, ownerID: fixture.request.Session.OwnerID, commitErr: commitErr}
	generator, publisher := &clarificationDecisionGeneratorFake{decision: decision}, &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusDelivered || !result.Committed || !errors.Is(result.Cause, commitErr) ||
		generator.clarificationCalls != 1 || journal.commitCalls != 1 || journal.loadCalls != 2 || publisher.publishCount() != 1 {
		t.Fatalf("unknown clarification commit recovery = %+v generation=%d commit=%d load=%d publish=%d",
			result, generator.clarificationCalls, journal.commitCalls, journal.loadCalls, publisher.publishCount())
	}
}

func TestRunAcceptedTurnDoesNotPublishMalformedClarificationReceipt(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?",
	})
	input := ClarificationDecisionCommitInput{
		OperationID: clarificationDecisionOperationPrefix + string(fixture.request.Accepted.TurnID),
		SessionID:   fixture.request.Session.SessionID, ControlTurnID: fixture.request.Accepted.TurnID,
		DelegationID: fixture.context.Handoff.DelegationID, Revision: fixture.context.Handoff.Revision,
		GenerationEpoch:   fixture.request.Accepted.GenerationEpoch,
		InvocationReceipt: agent.InvocationReceipt{Status: "completed"},
		Decision:          conversation.ClarificationDecision{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser, Question: "Which deployment region should I use?"},
		EventID:           modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(), OccurredAt: fixture.now,
	}
	receipt := clarificationCommitResult(fixture.context, fixture.request.Session.OwnerID, input)
	receipt.ReceiptEvent.Payload["status"] = "failed"
	journal := &clarificationDecisionJournalFake{
		context: fixture.context, ownerID: fixture.request.Session.OwnerID, result: receipt, resultFound: true,
	}
	generator, publisher := &clarificationDecisionGeneratorFake{decision: input.Decision}, &fakePublisher{}
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: clarificationReadViewBuilder{view: fixture.view}, Composer: identityPromptComposer{},
		Generator: generator, Journal: journal, Publisher: publisher,
		Config: RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: []string{"shiro"}},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: fixture.request})
	if result.Status != TurnStatusCommitUncertain || result.Committed || generator.clarificationCalls != 0 || publisher.publishCount() != 0 {
		t.Fatalf("malformed stored receipt crossed the publish boundary: result=%+v generated=%d published=%d",
			result, generator.clarificationCalls, publisher.publishCount())
	}
}

type clarificationTailReader struct {
	*fakeTailReader
	context        StoredClarificationControlContext
	contextErr     error
	contextCalls   int
	contextRequest ClarificationControlContextRequest
}

func (reader *clarificationTailReader) LoadClarificationControlContext(_ context.Context, request ClarificationControlContextRequest) (StoredClarificationControlContext, error) {
	reader.contextCalls++
	reader.contextRequest = request
	return reader.context, reader.contextErr
}

type clarificationReadViewBuilder struct{ view ReadView }

func (builder clarificationReadViewBuilder) BuildReadView(_ context.Context, _ ReadViewRequest) (ReadView, error) {
	return builder.view, nil
}

type clarificationDecisionGeneratorFake struct {
	decision           conversation.ClarificationDecision
	receipt            agent.InvocationReceipt
	generateErr        error
	cancel             context.CancelFunc
	emissionCalls      int
	clarificationCalls int
	scope              conversation.ConversationScope
	snapshot           agent.PromptSnapshot
	control            conversation.ClarificationControlContext
	allowlist          conversation.EmissionAllowlist
}

type clarificationEmissionOnlyGeneratorFake struct{ calls int }

func (generator *clarificationEmissionOnlyGeneratorFake) GenerateEmission(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	generator.calls++
	return conversation.AgentEmission{}, agent.InvocationReceipt{}, errors.New("stored clarification replay must not generate")
}

func (generator *clarificationDecisionGeneratorFake) GenerateEmission(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	generator.emissionCalls++
	return conversation.AgentEmission{}, agent.InvocationReceipt{}, errors.New("ordinary emission should not run for clarification control")
}

func (generator *clarificationDecisionGeneratorFake) GenerateClarificationDecision(_ context.Context, scope conversation.ConversationScope, snapshot agent.PromptSnapshot, control conversation.ClarificationControlContext, allowlist conversation.EmissionAllowlist) (conversation.ClarificationDecision, agent.InvocationReceipt, error) {
	generator.clarificationCalls++
	generator.scope = scope
	generator.snapshot = snapshot.Clone()
	generator.control = control
	generator.allowlist = allowlist
	receipt := generator.receipt
	if receipt.Status == "" {
		receipt.Status = "completed"
	}
	if generator.cancel != nil {
		generator.cancel()
	}
	return generator.decision, receipt, generator.generateErr
}

type clarificationDecisionJournalFake struct {
	*fakeJournal
	context              StoredClarificationControlContext
	ownerID              string
	commitCalls          int
	terminalCalls        int
	loadCalls            int
	lastInput            ClarificationDecisionCommitInput
	terminalInput        ClarificationTerminalCommitInput
	commitErr            error
	terminalErr          error
	loadErr              error
	result               ClarificationDecisionCommitResult
	resultFound          bool
	commitWithoutOutcome bool
}

func (journal *clarificationDecisionJournalFake) CommitClarificationDecision(_ context.Context, input ClarificationDecisionCommitInput) (ClarificationDecisionCommitResult, error) {
	journal.commitCalls++
	journal.lastInput = input
	if journal.result.OperationID == "" {
		journal.result = clarificationCommitResult(journal.context, journal.ownerID, input)
	}
	if !journal.commitWithoutOutcome {
		journal.resultFound = true
	}
	if journal.commitErr != nil {
		return ClarificationDecisionCommitResult{}, journal.commitErr
	}
	return cloneClarificationDecisionCommitResult(journal.result), nil
}

func (journal *clarificationDecisionJournalFake) CommitClarificationTerminal(_ context.Context, input ClarificationTerminalCommitInput) (ClarificationDecisionCommitResult, error) {
	journal.terminalCalls++
	journal.terminalInput = input
	if journal.result.OperationID == "" {
		journal.result = clarificationTerminalCommitResult(journal.context, journal.ownerID, input)
	}
	if !journal.commitWithoutOutcome {
		journal.resultFound = true
	}
	if journal.terminalErr != nil {
		return ClarificationDecisionCommitResult{}, journal.terminalErr
	}
	return cloneClarificationDecisionCommitResult(journal.result), nil
}

func (journal *clarificationDecisionJournalFake) LoadClarificationDecisionOutcome(_ context.Context, turnID modulecore.TurnID) (ClarificationDecisionCommitResult, bool, error) {
	journal.loadCalls++
	if journal.loadErr != nil {
		return ClarificationDecisionCommitResult{}, false, journal.loadErr
	}
	if !journal.resultFound || journal.result.ControlTurnID != turnID {
		return ClarificationDecisionCommitResult{}, false, nil
	}
	return cloneClarificationDecisionCommitResult(journal.result), true, nil
}

func newClarificationRuntimeFixture(t *testing.T, _ conversation.ClarificationDecision) clarificationRuntimeFixture {
	t.Helper()
	const ownerID, assignee = "owner-1", "shiro"
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	sessionID := modulecore.NewSessionID()
	originTurnID, controlTurnID := modulecore.NewTurnID(), modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	delegationID := modulecore.NewRequestID()
	origin := userEvent(sessionID, originTurnID, 2, ownerID, "original user request")
	origin.TraceID = traceID
	notice := assistantEvent(sessionID, originTurnID, 3, string(modulecore.NewMessageID()), "Mio delegated the task", "mio")
	notice.TraceID = traceID
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: originTurnID,
		OriginUserMessageID: origin.MessageID, SessionID: sessionID, SourceTraceID: traceID,
		FromActor: "mio", ToActor: assignee, InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: strings.Repeat("a", 64), AllowedCapabilities: []string{}, ReplyTo: origin.MessageID,
		Goal: "Inspect the request", Instruction: "Inspect the request and report.",
		Constraints: []conversation.AgentConstraint{}, ExpectedOutput: "A concise report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	question := modulecore.NewEventEnvelope(traceID, modulecore.NewEventID(), nil, "conversation_runtime", "conversation.clarification_question", now, map[string]any{
		"kind": "clarification_question", "status": string(conversation.ReadbackStatusNeedsClarification),
		"delegation_id": string(delegationID), "delegation_revision": uint64(1),
		"visibility": "private", "from": assignee, "source_actor_id": assignee, "to": "mio",
		"content": "private worker question", "text": "private worker question", "reply_to_message_id": string(origin.MessageID),
	})
	question.SessionID, question.TurnID, question.RequestID, question.MessageID, question.EventSeq = sessionID, controlTurnID, delegationID, modulecore.NewMessageID(), 4
	controlTurn := AcceptedTurn{
		SessionID: sessionID, TurnID: controlTurnID, ActorID: assignee, TriggerKind: "clarification_control",
		ClientSubmissionID: "clarification:" + string(delegationID) + ":1", InputMessageID: question.MessageID,
		AdmissionSeq: question.EventSeq, GenerationEpoch: 3, State: "accepted", InputEvent: question,
	}
	questionIndex := MessageIndex{
		MessageID: question.MessageID, EventID: question.EventID, SessionID: sessionID, TurnID: controlTurnID,
		FromActor: assignee, ToKind: "agent", ToActor: "mio", Kind: "clarification_question",
		ReplyToMessageID: origin.MessageID, Visibility: "private",
	}
	context := StoredClarificationControlContext{
		ControlTurn: controlTurn, OwnerID: ownerID, Handoff: handoff, QuestionEvent: question, QuestionMessage: questionIndex,
		Context: []DelegationDispatchMessage{{MessageID: origin.MessageID, Role: "user", Text: "original user request"}},
	}
	request := ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: assignee},
		Session: SessionState{
			SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 1, AdmissionSeq: 4, ProjectionWatermark: 1, GenerationEpoch: 3,
		},
		Accepted: controlTurn, Legacy: LegacyProjection{AppliedThrough: 1, Complete: true},
	}
	tail := CanonicalTail{
		ThroughSeq: 4, Complete: true,
		Messages: []CanonicalMessage{
			canonicalMessage(origin, ownerID, "agent", "mio", "user_message"),
			canonicalMessage(notice, "mio", "user", ownerID, "delegation_notice"),
			{
				Event: question,
				Index: MessageIndex{
					MessageID: question.MessageID, EventID: question.EventID, SessionID: sessionID, TurnID: controlTurnID,
					FromActor: assignee, ToKind: "agent", ToActor: "mio", Kind: "clarification_question",
					ReplyToMessageID: origin.MessageID, Visibility: "private",
				},
			},
		},
	}
	control := conversation.ClarificationControlContext{
		Handoff: handoff, QuestionDelegationID: delegationID, QuestionRevision: 1,
		QuestionMessageID: question.MessageID, QuestionSender: assignee, QuestionText: "private worker question",
	}
	view := ReadView{
		SessionID: sessionID, RuntimeVersion: ConversationRuntimeV2, CutoverSeq: 1, ProjectionWatermark: 1, ThroughSeq: 4,
		Messages: []PromptMessage{
			{Role: "user", Text: "original user request", MessageID: string(origin.MessageID), TurnID: originTurnID, EventSeq: origin.EventSeq},
			{Role: "assistant", Text: "Mio delegated the task", MessageID: string(notice.MessageID), TurnID: originTurnID, EventSeq: notice.EventSeq},
			{Role: "user", Text: "private worker question", MessageID: string(question.MessageID), TurnID: controlTurnID, EventSeq: question.EventSeq},
		},
		ClarificationControl: &control,
	}
	return clarificationRuntimeFixture{request: request, tail: tail, context: context, view: view, now: now}
}

type clarificationRuntimeFixture struct {
	request ReadViewRequest
	tail    CanonicalTail
	context StoredClarificationControlContext
	view    ReadView
	now     time.Time
}

func clarificationCommitResult(context StoredClarificationControlContext, ownerID string, input ClarificationDecisionCommitInput) ClarificationDecisionCommitResult {
	result := ClarificationDecisionCommitResult{
		OperationID: input.OperationID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision,
		Kind: input.Decision.Kind, DispatchQueued: input.Decision.Kind == conversation.ClarificationDecisionContinueDelegation,
		InvocationReceipt: input.InvocationReceipt,
	}
	if result.InvocationReceipt.Status == "" {
		result.InvocationReceipt.Status = "completed"
	}
	if input.Decision.Kind == conversation.ClarificationDecisionAskUser {
		result.ResultState = "completed"
		event := modulecore.NewEventEnvelope(context.Handoff.SourceTraceID, context.QuestionEvent.EventID, nil, "orchestrator", "agent.response", input.OccurredAt, map[string]any{
			"kind": "clarification_prompt", "status": "awaiting_user_clarification", "client_submission_id": context.ControlTurn.ClientSubmissionID,
			"availability": "free", "from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public",
			"delegation_id": string(input.DelegationID), "delegation_revision": input.Revision,
			"text": input.Decision.Question, "content": input.Decision.Question,
			"reply_to_message_id": string(context.Handoff.ReplyTo),
		})
		event.EventID = input.EventID
		event.SessionID, event.TurnID, event.MessageID, event.EventSeq = input.SessionID, input.ControlTurnID, input.MessageID, 5
		message := MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: input.SessionID, TurnID: input.ControlTurnID,
			FromActor: "mio", ToKind: "user", ToActor: ownerID, Kind: "clarification_prompt",
			ReplyToMessageID: context.Handoff.OriginUserMessageID, Visibility: "public",
		}
		result.Event, result.Message = event, message
		receiptEvent := clarificationReceiptEvent(context, input, result.InvocationReceipt, 6)
		result.ReceiptEvent = receiptEvent
		result.Events, result.MessageIndexes = []modulecore.EventEnvelope{event, receiptEvent}, []MessageIndex{message}
		return result
	}
	result.ResultState = "awaiting_readback"
	next := context.Handoff
	next.Revision++
	next.Instruction = input.Decision.Instruction
	next.InstructionMessageID = input.MessageID
	result.NextHandoff = &next
	contextEvent := modulecore.NewEventEnvelope(context.Handoff.SourceTraceID, context.QuestionEvent.EventID, nil, "conversation_runtime", "conversation.delegation_context", input.OccurredAt, map[string]any{"handoff": next})
	contextEvent.EventID = input.ContextEventID
	contextEvent.SessionID, contextEvent.TurnID, contextEvent.RequestID, contextEvent.EventSeq = input.SessionID, context.Handoff.OriginTurnID, input.DelegationID, 5
	instruction := modulecore.NewEventEnvelope(context.Handoff.SourceTraceID, contextEvent.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", input.OccurredAt, map[string]any{
		"kind": "delegation_instruction", "delegation_id": string(input.DelegationID), "delegation_revision": next.Revision,
		"visibility": "private", "from": "mio", "source_actor_id": "mio", "to": next.ToActor,
		"text": next.Instruction, "content": next.Instruction,
	})
	instruction.ActorKind, instruction.ActorID = "agent", "mio"
	instruction.EventID = input.EventID
	instruction.CausationEventID = context.QuestionEvent.EventID
	instruction.SessionID, instruction.TurnID, instruction.RequestID, instruction.MessageID, instruction.EventSeq = input.SessionID, next.OriginTurnID, input.DelegationID, input.MessageID, 6
	message := MessageIndex{
		MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: input.SessionID, TurnID: next.OriginTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: next.ToActor, Kind: "delegation_instruction",
		ReplyToMessageID: next.ReplyTo, Visibility: "private",
	}
	result.Event, result.Message = instruction, message
	receiptEvent := clarificationReceiptEvent(context, input, result.InvocationReceipt, 7)
	result.ReceiptEvent = receiptEvent
	result.Events, result.MessageIndexes = []modulecore.EventEnvelope{contextEvent, instruction, receiptEvent}, []MessageIndex{message}
	return result
}

func clarificationTerminalCommitResult(context StoredClarificationControlContext, ownerID string, input ClarificationTerminalCommitInput) ClarificationDecisionCommitResult {
	status, ok := clarificationTerminalStatus(input.Reason)
	if !ok {
		panic("test fixture has invalid clarification terminal reason")
	}
	result := ClarificationDecisionCommitResult{
		OperationID: input.OperationID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision,
		TerminalReason: input.Reason, ResultState: "completed", InvocationReceipt: input.InvocationReceipt,
	}
	if result.InvocationReceipt.Status == "" {
		result.InvocationReceipt.Status = status
	}
	question := context.QuestionEvent
	event := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", input.OccurredAt, map[string]any{
		"kind": "turn_error", "client_submission_id": context.ControlTurn.ClientSubmissionID,
		"availability": "free", "visibility": "public", "delegation_id": string(input.DelegationID),
		"delegation_revision": input.Revision, "from": "mio", "source_actor_id": "mio", "to": "user",
		"content": conversation.ClarificationTerminalUserText, "text": conversation.ClarificationTerminalUserText,
		"reply_to_message_id": string(context.Handoff.ReplyTo),
	})
	event.EventID, event.TraceID, event.CausationEventID = input.EventID, question.TraceID, question.EventID
	event.SessionID, event.TurnID, event.MessageID, event.EventSeq = input.SessionID, input.ControlTurnID, input.MessageID, 5
	message := MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: input.SessionID, TurnID: input.ControlTurnID,
		FromActor: "mio", ToKind: "user", ToActor: ownerID, Kind: "turn_error",
		ReplyToMessageID: context.Handoff.ReplyTo, Visibility: "public",
	}
	receiptEvent := modulecore.NewEventEnvelope(question.TraceID, question.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", input.OccurredAt, map[string]any{"status": result.InvocationReceipt.Status})
	if result.InvocationReceipt.RequestID != "" {
		receiptEvent.Payload["request_id"] = result.InvocationReceipt.RequestID
	}
	if result.InvocationReceipt.ResponseID != "" {
		receiptEvent.Payload["response_id"] = result.InvocationReceipt.ResponseID
	}
	receiptEvent.EventID = input.ReceiptEventID
	receiptEvent.SessionID, receiptEvent.TurnID, receiptEvent.EventSeq = input.SessionID, input.ControlTurnID, 6
	result.Event, result.Message, result.ReceiptEvent = event, message, receiptEvent
	result.Events = []modulecore.EventEnvelope{event, receiptEvent}
	result.MessageIndexes = []MessageIndex{message}
	return result
}

func clarificationReceiptEvent(context StoredClarificationControlContext, input ClarificationDecisionCommitInput, receipt agent.InvocationReceipt, eventSeq modulecore.EventSeq) modulecore.EventEnvelope {
	payload := map[string]any{"status": receipt.Status}
	if receipt.RequestID != "" {
		payload["request_id"] = receipt.RequestID
	}
	if receipt.ResponseID != "" {
		payload["response_id"] = receipt.ResponseID
	}
	event := modulecore.NewEventEnvelope(context.Handoff.SourceTraceID, context.QuestionEvent.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", input.OccurredAt, payload)
	event.EventID = input.ReceiptEventID
	event.SessionID, event.TurnID, event.EventSeq = input.SessionID, input.ControlTurnID, eventSeq
	return event
}

func cloneClarificationDecisionCommitResult(result ClarificationDecisionCommitResult) ClarificationDecisionCommitResult {
	cloned := result
	cloned.Events = append([]modulecore.EventEnvelope(nil), result.Events...)
	cloned.MessageIndexes = append([]MessageIndex(nil), result.MessageIndexes...)
	cloned.InvocationReceipt = result.InvocationReceipt
	if result.NextHandoff != nil {
		handoff := *result.NextHandoff
		if handoff.AllowedCapabilities != nil {
			capabilities := make([]string, len(result.NextHandoff.AllowedCapabilities))
			copy(capabilities, result.NextHandoff.AllowedCapabilities)
			handoff.AllowedCapabilities = capabilities
		}
		if handoff.Constraints != nil {
			constraints := make([]conversation.AgentConstraint, len(result.NextHandoff.Constraints))
			copy(constraints, result.NextHandoff.Constraints)
			handoff.Constraints = constraints
		}
		cloned.NextHandoff = &handoff
	}
	return cloned
}

func promptHasText(snapshot agent.PromptSnapshot, text string) bool {
	for _, message := range snapshot.Messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func eventPayloadText(event modulecore.EventEnvelope) string {
	text, _ := event.Payload["text"].(string)
	content, _ := event.Payload["content"].(string)
	return text + content
}
