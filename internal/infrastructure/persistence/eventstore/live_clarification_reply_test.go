package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAcceptTurnConsumesClarificationPromptOnlyOnce(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)

	first, err := store.AcceptTurn(context.Background(), clarificationReplyAcceptInput(fixture, prompt.Message.MessageID, "reply-op-first", "reply-submission-first", "Use the staging region."))
	if err != nil {
		t.Fatalf("AcceptTurn(first clarification reply): %v", err)
	}
	if first.TriggerKind != "clarification_reply" {
		t.Fatalf("accepted trigger kind = %q, want clarification_reply", first.TriggerKind)
	}

	loaded, err := store.LoadClarificationReplyContext(context.Background(), ClarificationReplyContextInput{
		SessionID: fixture.sessionID, ReplyTurnID: first.TurnID, OwnerID: "user-1",
	})
	if err != nil {
		t.Fatalf("LoadClarificationReplyContext(): %v", err)
	}
	if loaded.Receipt.PromptMessageID != prompt.Message.MessageID || loaded.Receipt.SourceTurnID != fixture.turnID ||
		loaded.Receipt.DelegationID != fixture.delegationID || loaded.Receipt.Revision != fixture.revision ||
		loaded.AnswerText != "Use the staging region." || loaded.QuestionMessage.Visibility != "private" ||
		loaded.PromptMessage.Visibility != "public" || loaded.ControlTurn.TurnID == loaded.OriginTurnID ||
		loaded.Handoff.OriginTurnID != fixture.turnID || len(loaded.Context) != 1 || loaded.Context[0].Text != "please delegate this task" {
		t.Fatalf("loaded clarification reply context = %#v", loaded)
	}

	_, err = store.AcceptTurn(context.Background(), clarificationReplyAcceptInput(fixture, prompt.Message.MessageID, "reply-op-second", "reply-submission-second", "Use the production region."))
	if err == nil {
		t.Fatal("AcceptTurn(second reply to consumed prompt) error = nil, want conflict")
	}
}

func TestAcceptTurnDoesNotInterpretOrdinaryReplyTargetAsClarification(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	input := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID, "ordinary-reply-op", "ordinary-reply-submission", "A normal message replying to that text.")
	input.TriggerKind = "user_message"

	accepted, err := store.AcceptTurn(context.Background(), input)
	if err != nil {
		t.Fatalf("AcceptTurn(ordinary user message): %v", err)
	}
	if accepted.TriggerKind != "user_message" {
		t.Fatalf("accepted trigger kind = %q, want user_message", accepted.TriggerKind)
	}
}

func TestAcceptTurnRejectsUnknownPrivateCrossOwnerAndCrossSessionClarificationTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, readbackFixture, ClarificationDecisionCommit, *AcceptInput)
	}{
		{
			name: "unknown target",
			mutate: func(_ *testing.T, _ *SQLiteStore, _ readbackFixture, _ ClarificationDecisionCommit, input *AcceptInput) {
				setClarificationReplyTarget(input, modulecore.NewMessageID())
			},
		},
		{
			name: "private question target",
			mutate: func(t *testing.T, store *SQLiteStore, _ readbackFixture, prompt ClarificationDecisionCommit, input *AcceptInput) {
				var questionID string
				if err := store.db.QueryRow(`SELECT message_id FROM live_message_index WHERE event_id=?`, string(prompt.Event.CausationEventID)).Scan(&questionID); err != nil {
					t.Fatalf("load private question MessageID: %v", err)
				}
				setClarificationReplyTarget(input, modulecore.MessageID(questionID))
			},
		},
		{
			name: "cross owner",
			mutate: func(_ *testing.T, _ *SQLiteStore, _ readbackFixture, _ ClarificationDecisionCommit, input *AcceptInput) {
				input.OwnerID = "user-2"
			},
		},
		{
			name: "cross session",
			mutate: func(_ *testing.T, _ *SQLiteStore, _ readbackFixture, _ ClarificationDecisionCommit, input *AcceptInput) {
				sessionID := modulecore.NewSessionID()
				input.CreateNewSession = true
				input.InputEvent.SessionID = sessionID
				input.InputMessage.SessionID = sessionID
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture, prompt := createClarificationPrompt(t, store)
			input := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID, "invalid-target-"+test.name, "invalid-target-submission-"+test.name, "Use staging.")
			test.mutate(t, store, fixture, prompt, &input)
			if _, err := store.AcceptTurn(context.Background(), input); err == nil {
				t.Fatal("AcceptTurn() error = nil, want invalid clarification target conflict")
			}
			var receiptCount int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM live_clarification_reply_receipt WHERE reply_turn_id=?`, string(input.InputEvent.TurnID)).Scan(&receiptCount); err != nil {
				t.Fatalf("count invalid reply receipts: %v", err)
			}
			if receiptCount != 0 {
				t.Fatalf("invalid clarification target left %d receipt rows", receiptCount)
			}
		})
	}
}

func TestClarificationReplyRejectsConsumedPromptAfterRevisionAdvances(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	first, err := store.AcceptTurn(context.Background(), clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"stale-reply-first", "stale-reply-submission-first", "Use staging."))
	if err != nil {
		t.Fatalf("AcceptTurn(first answer): %v", err)
	}
	if _, err := store.CommitClarificationReplyContinuation(context.Background(), clarificationReplyContinuationInput(fixture,
		first.TurnID, prompt.Message.MessageID, "stale-reply-continuation", 1, "Continue with staging.")); err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(): %v", err)
	}
	second := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"stale-reply-second", "stale-reply-submission-second", "Different answer to stale prompt.")
	if _, err := store.AcceptTurn(context.Background(), second); !errors.Is(err, ErrClarificationReplyConflict) {
		t.Fatalf("AcceptTurn(stale consumed prompt) error = %v, want ErrClarificationReplyConflict", err)
	}
}

func TestClarificationReplyContinuationPersistsAnswerAndAllowsReadback(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	reply, err := store.AcceptTurn(context.Background(), clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"reply-continuation-accept", "reply-continuation-submission", "Use the staging region."))
	if err != nil {
		t.Fatalf("AcceptTurn(clarification reply): %v", err)
	}
	input := clarificationReplyContinuationInput(fixture, reply.TurnID, prompt.Message.MessageID, "reply-continuation-commit", 1,
		"Use the staging region and retain the existing constraints.")
	committed, err := store.CommitClarificationReplyContinuation(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(): %v", err)
	}
	if committed.OwnerID != "user-1" || committed.ReplyClientSubmissionID != reply.ClientSubmissionID ||
		committed.NextRevision != 2 || committed.ResultState != "awaiting_readback" ||
		!committed.DispatchQueued || committed.NextHandoff == nil || committed.NextHandoff.ToActor != fixture.assignee ||
		committed.NextHandoff.Goal != "Summarize a repository behavior" || committed.NextHandoff.Instruction != input.Instruction ||
		len(committed.NextHandoff.Constraints) != 1 || committed.NextHandoff.Constraints[0].Key != "keep_scope" ||
		len(committed.NextHandoff.AllowedCapabilities) != 0 {
		t.Fatalf("clarification reply continuation = %#v", committed)
	}
	if committed.Event.CausationEventID != committed.ContextEvent.EventID ||
		committed.ReceiptEvent.TraceID != reply.InputEvent.TraceID || committed.ReceiptEvent.CausationEventID != reply.InputEvent.EventID ||
		committed.Event.EventType != "conversation.delegation_instruction" || committed.Message.Visibility != "private" ||
		committed.ReceiptEvent.EventType != "conversation.mio_emission_receipt" ||
		committed.InvocationReceipt.RequestID != input.InvocationReceipt.RequestID ||
		committed.InvocationReceipt.ResponseID != input.InvocationReceipt.ResponseID ||
		committed.InvocationReceipt.Status != input.InvocationReceipt.Status || committed.InvocationReceipt.ProviderLabel != "" ||
		committed.InvocationReceipt.TokensUsed != 0 {
		t.Fatalf("continuation output causation/private receipt = event %#v receipt %#v", committed.Event, committed.ReceiptEvent)
	}
	var messagePayload string
	if err := store.db.QueryRow(`SELECT envelope_json FROM event_envelope WHERE event_id=?`, string(committed.ContextEvent.EventID)).Scan(&messagePayload); err != nil {
		t.Fatalf("load persisted continuation context: %v", err)
	}
	var storedContext modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(messagePayload), &storedContext); err != nil {
		t.Fatalf("decode persisted continuation context: %v", err)
	}
	var storedMessages []DelegationContextMessage
	encodedMessages, err := json.Marshal(storedContext.Payload["messages"])
	if err != nil || json.Unmarshal(encodedMessages, &storedMessages) != nil || len(storedMessages) != 2 ||
		storedMessages[1].MessageID != reply.InputMessageID || storedMessages[1].Role != "user" || storedMessages[1].Text != "Use the staging region." {
		t.Fatalf("continuation context messages = %#v", storedMessages)
	}

	retry := input
	retry.EventID, retry.MessageID, retry.ContextEventID, retry.ReceiptEventID =
		modulecore.NewEventID(), modulecore.NewMessageID(), modulecore.NewEventID(), modulecore.NewEventID()
	retry.OccurredAt = input.OccurredAt.Add(time.Minute)
	retry.InvocationReceipt.ProviderLabel = "diagnostic-only retry value"
	retry.InvocationReceipt.TokensUsed = 123
	replayed, err := store.CommitClarificationReplyContinuation(context.Background(), retry)
	if err != nil || !reflect.DeepEqual(replayed, committed) {
		t.Fatalf("CommitClarificationReplyContinuation(retry) = %#v, %v; want original %#v", replayed, err, committed)
	}
	changed := input
	changed.Instruction += " changed"
	if _, err := store.CommitClarificationReplyContinuation(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("CommitClarificationReplyContinuation(changed input) error = %v, want ErrOperationConflict", err)
	}

	readback := readbackForRevision(fixture, 2, conversation.ReadbackStatusAccepted, "reply-continuation-readback")
	accepted, err := store.AcceptReadback(context.Background(), readback)
	if err != nil {
		t.Fatalf("AcceptReadback(revision 2 from user-reply continuation): %v", err)
	}
	if accepted.Status != conversation.ReadbackStatusAccepted || accepted.OriginTurnID != fixture.turnID {
		t.Fatalf("revision 2 readback = %#v", accepted)
	}
	if payloadString(accepted.Event.Payload, "response_to_turn_id") != string(reply.TurnID) ||
		payloadString(accepted.Event.Payload, "response_to_client_submission_id") != reply.ClientSubmissionID {
		t.Fatalf("public readback omitted clarification reply correlation: %#v", accepted.Event.Payload)
	}
	outcome, found, err := store.LoadClarificationReplyContinuationOutcome(context.Background(), reply.TurnID)
	if err != nil || !found || !reflect.DeepEqual(outcome, committed) {
		t.Fatalf("LoadClarificationReplyContinuationOutcome() found=%t err=%v outcome=%#v", found, err, outcome)
	}
}

func TestClarificationReplyContinuationAcceptsAnswerOnItsOwnRequestTrace(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	answerInput := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"reply-separate-trace-accept", "reply-separate-trace-submission", "Use the staging region.")
	answerTraceID := modulecore.NewTraceID()
	answerInput.InputEvent.TraceID = answerTraceID
	accepted, err := store.AcceptTurn(context.Background(), answerInput)
	if err != nil {
		t.Fatalf("AcceptTurn(clarification reply on a separate trace): %v", err)
	}
	input := clarificationReplyContinuationInput(fixture, accepted.TurnID, prompt.Message.MessageID,
		"reply-separate-trace-continuation", 1, "Continue with staging.")
	committed, err := store.CommitClarificationReplyContinuation(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(separate answer trace): %v", err)
	}
	if committed.Event.TraceID != fixture.traceID || committed.Event.CausationEventID != committed.ContextEvent.EventID ||
		committed.ReceiptEvent.TraceID != answerTraceID || committed.ReceiptEvent.CausationEventID != accepted.InputEvent.EventID {
		t.Fatalf("Mio receipt lineage = trace %q causation %q, want answer trace %q event %q",
			committed.ReceiptEvent.TraceID, committed.ReceiptEvent.CausationEventID, answerTraceID, accepted.InputEvent.EventID)
	}
}

func TestExplicitClarificationReplyCanAdvancePastAutomaticRevisionLimit(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	ctx := context.Background()
	reply, err := store.AcceptTurn(ctx, clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"max-revision-reply-1", "max-revision-submission-1", "Explicit answer for revision 1."))
	if err != nil {
		t.Fatalf("AcceptTurn(explicit answer at revision 1): %v", err)
	}
	continued, err := store.CommitClarificationReplyContinuation(ctx,
		clarificationReplyContinuationInput(fixture, reply.TurnID, prompt.Message.MessageID,
			"max-revision-reply-continuation-1", 1, "Continue after the user's answer."))
	if err != nil || continued.NextRevision != 2 {
		t.Fatalf("CommitClarificationReplyContinuation(revision 1) = %#v, %v", continued, err)
	}
	for revision := uint64(2); revision < conversation.MaxClarificationRevision; revision++ {
		readback := readbackForRevision(fixture, revision, conversation.ReadbackStatusNeedsClarification,
			"max-revision-readback-"+strconv.FormatUint(revision, 10))
		accepted, err := store.AcceptReadback(ctx, readback)
		if err != nil {
			t.Fatalf("AcceptReadback(revision %d): %v", revision, err)
		}
		control, err := store.LoadClarificationControlContext(ctx, ClarificationControlContextInput{
			SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID,
			OwnerID: "user-1", Assignee: fixture.assignee,
		})
		if err != nil {
			t.Fatalf("LoadClarificationControlContext(revision %d): %v", revision, err)
		}
		continued, err := store.CommitClarificationDecision(ctx, clarificationDecisionInputForRevision(fixture, control.ControlTurn.TurnID, revision,
			conversation.ClarificationDecision{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
				Instruction: "Automatic continuation within the configured limit."}))
		if err != nil {
			t.Fatalf("CommitClarificationDecision(automatic revision %d): %v", revision, err)
		}
		if continued.NextHandoff == nil || continued.NextHandoff.Revision != revision+1 {
			t.Fatalf("automatic continuation at revision %d = %#v", revision, continued)
		}
	}

	readback := readbackForRevision(fixture, conversation.MaxClarificationRevision,
		conversation.ReadbackStatusNeedsClarification, "max-revision-readback-4")
	accepted, err := store.AcceptReadback(ctx, readback)
	if err != nil {
		t.Fatalf("AcceptReadback(revision 4): %v", err)
	}
	control, err := store.LoadClarificationControlContext(ctx, ClarificationControlContextInput{
		SessionID: fixture.sessionID, ControlTurnID: accepted.ControlTurn.TurnID,
		OwnerID: "user-1", Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(revision 4): %v", err)
	}
	prompt, err = store.CommitClarificationDecision(ctx, clarificationDecisionInputForRevision(fixture, control.ControlTurn.TurnID, conversation.MaxClarificationRevision,
		conversation.ClarificationDecision{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser,
			Question: "What should Shiro do next?"}))
	if err != nil {
		t.Fatalf("CommitClarificationDecision(ask user at revision 4): %v", err)
	}
	reply, err = store.AcceptTurn(ctx, clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"max-revision-reply", "max-revision-submission", "Use the staging region."))
	if err != nil {
		t.Fatalf("AcceptTurn(explicit answer at revision 4): %v", err)
	}
	continued, err = store.CommitClarificationReplyContinuation(ctx,
		clarificationReplyContinuationInput(fixture, reply.TurnID, prompt.Message.MessageID,
			"max-revision-reply-continuation", conversation.MaxClarificationRevision, "Use staging and preserve the constraints."))
	if err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(revision 4 to 5): %v", err)
	}
	if continued.NextRevision != conversation.MaxClarificationRevision+1 {
		t.Fatalf("explicit reply next revision = %d, want %d", continued.NextRevision, conversation.MaxClarificationRevision+1)
	}

	readback5 := readbackForRevision(fixture, conversation.MaxClarificationRevision+1,
		conversation.ReadbackStatusNeedsClarification, "max-revision-readback-"+strconv.FormatUint(conversation.MaxClarificationRevision+1, 10))
	accepted5, err := store.AcceptReadback(ctx, readback5)
	if err != nil {
		t.Fatalf("AcceptReadback(revision 5 after explicit user answer): %v", err)
	}
	control5, err := store.LoadClarificationControlContext(ctx, ClarificationControlContextInput{
		SessionID: fixture.sessionID, ControlTurnID: accepted5.ControlTurn.TurnID,
		OwnerID: "user-1", Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadClarificationControlContext(revision 5): %v", err)
	}
	_, err = store.CommitClarificationDecision(ctx, clarificationDecisionInputForRevision(fixture, control5.ControlTurn.TurnID, conversation.MaxClarificationRevision+1,
		conversation.ClarificationDecision{SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionContinueDelegation,
			Instruction: "This automatic continuation must remain blocked."}))
	if !errors.Is(err, ErrClarificationRevisionLimit) {
		t.Fatalf("automatic continuation at revision 5 error = %v, want ErrClarificationRevisionLimit", err)
	}
}

func TestClarificationReplyAdmissionRetriesAndConcurrentAnswersHaveOneWinner(t *testing.T) {
	store := newTestStore(t)
	fixture, prompt := createClarificationPrompt(t, store)
	firstInput := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"reply-retry-original-operation", "reply-retry-submission", "Use staging.")
	first, err := store.AcceptTurn(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("AcceptTurn(first): %v", err)
	}
	retry := firstInput
	retry.OperationID = "reply-retry-new-operation"
	retry.InputEvent.EventID = modulecore.NewEventID()
	retry.InputEvent.TurnID = modulecore.NewTurnID()
	retry.InputEvent.MessageID = modulecore.NewMessageID()
	retry.InputMessage.EventID = retry.InputEvent.EventID
	retry.InputMessage.TurnID = retry.InputEvent.TurnID
	retry.InputMessage.MessageID = retry.InputEvent.MessageID
	second, err := store.AcceptTurn(context.Background(), retry)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("AcceptTurn(same submission retry) = %#v, %v; want %#v", second, err, first)
	}
	changed := retry
	changed.InputEvent.Payload = map[string]any{
		"from": "user", "source_actor_id": "user-1", "to": "mio", "content": "Changed answer", "text": "Changed answer",
		"reply_to_message_id": string(prompt.Message.MessageID),
	}
	changed.OperationID += "-changed"
	if _, err := store.AcceptTurn(context.Background(), changed); !errors.Is(err, ErrClientSubmissionConflict) {
		t.Fatalf("AcceptTurn(changed same submission) error = %v, want ErrClientSubmissionConflict", err)
	}

	concurrentStore := newTestStore(t)
	concurrentFixture, concurrentPrompt := createClarificationPrompt(t, concurrentStore)
	inputs := []AcceptInput{
		clarificationReplyAcceptInput(concurrentFixture, concurrentPrompt.Message.MessageID, "race-reply-1", "race-submission-1", "First answer."),
		clarificationReplyAcceptInput(concurrentFixture, concurrentPrompt.Message.MessageID, "race-reply-2", "race-submission-2", "Second answer."),
	}
	var wg sync.WaitGroup
	results := make([]error, len(inputs))
	wg.Add(len(inputs))
	for i := range inputs {
		go func(index int) {
			defer wg.Done()
			_, results[index] = concurrentStore.AcceptTurn(context.Background(), inputs[index])
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrClarificationReplyConflict) {
			t.Fatalf("concurrent AcceptTurn error = %v, want clarification conflict or one success", err)
		}
	}
	var receiptCount int
	if err := concurrentStore.db.QueryRow(`SELECT COUNT(*) FROM live_clarification_reply_receipt WHERE prompt_message_id=?`, string(concurrentPrompt.Message.MessageID)).Scan(&receiptCount); err != nil {
		t.Fatalf("count consumed prompt receipts: %v", err)
	}
	if winners != 1 || receiptCount != 1 {
		t.Fatalf("concurrent reply winners=%d receipts=%d, want exactly one each", winners, receiptCount)
	}
}

func TestClarificationReplyContextAndContinuationSurviveStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(restarted store): %v", err)
		}
	})
	fixture, prompt := createClarificationPrompt(t, store)
	replyInput := clarificationReplyAcceptInput(fixture, prompt.Message.MessageID,
		"restart-reply-operation", "restart-reply-submission", "Use the staging region.")
	reply, err := store.AcceptTurn(context.Background(), replyInput)
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first store before context reload): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restarted before continuation): %v", err)
	}
	loaded, err := store.LoadClarificationReplyContext(context.Background(), ClarificationReplyContextInput{
		SessionID: fixture.sessionID, ReplyTurnID: reply.TurnID, OwnerID: "user-1",
	})
	if err != nil || loaded.Receipt.PromptMessageID != prompt.Message.MessageID || loaded.AnswerText != "Use the staging region." {
		t.Fatalf("LoadClarificationReplyContext(after admission restart) = %#v, %v", loaded, err)
	}
	if _, found, err := store.LoadClarificationReplyContinuationOutcome(context.Background(), reply.TurnID); err != nil || found {
		t.Fatalf("LoadClarificationReplyContinuationOutcome(before commit) found=%t err=%v, want no outcome", found, err)
	}
	continuationInput := clarificationReplyContinuationInput(fixture, reply.TurnID, prompt.Message.MessageID,
		"restart-reply-continuation", 1, "Use staging.")
	committed, err := store.CommitClarificationReplyContinuation(context.Background(), continuationInput)
	if err != nil {
		t.Fatalf("CommitClarificationReplyContinuation(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first store): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restarted): %v", err)
	}
	outcome, found, err := store.LoadClarificationReplyContinuationOutcome(context.Background(), reply.TurnID)
	if err != nil || !found || !reflect.DeepEqual(outcome, committed) {
		t.Fatalf("LoadClarificationReplyContinuationOutcome(after restart) found=%t err=%v outcome=%#v", found, err, outcome)
	}
	continued, err := store.CommitClarificationReplyContinuation(context.Background(), continuationInput)
	if err != nil || !reflect.DeepEqual(continued, committed) {
		t.Fatalf("CommitClarificationReplyContinuation(retry after restart) = %#v, %v", continued, err)
	}
}

func createClarificationPrompt(t *testing.T, store *SQLiteStore) (readbackFixture, ClarificationDecisionCommit) {
	t.Helper()
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusNeedsClarification)
	accepted, err := store.AcceptReadback(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReadback(needs clarification): %v", err)
	}
	input := clarificationDecisionInput(fixture, accepted.ControlTurn.TurnID, conversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: conversation.ClarificationDecisionAskUser,
		Question: "Which region should Shiro use?",
	})
	prompt, err := store.CommitClarificationDecision(context.Background(), input)
	if err != nil {
		t.Fatalf("CommitClarificationDecision(ask user): %v", err)
	}
	return fixture, prompt
}

func clarificationReplyAcceptInput(fixture readbackFixture, promptID modulecore.MessageID, operationID, submissionID, answer string) AcceptInput {
	turnID := modulecore.NewTurnID()
	event := eventFixture(fixture.traceID, "conversation.user_message", "orchestrator")
	event.SessionID, event.TurnID, event.MessageID = fixture.sessionID, turnID, modulecore.NewMessageID()
	event.Payload = map[string]any{
		"from": "user", "source_actor_id": "user-1", "to": "mio", "content": answer, "text": answer,
		"reply_to_message_id": string(promptID),
	}
	return AcceptInput{
		OperationID: operationID, OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: submissionID, ActorID: "user-1", TriggerKind: "clarification_reply",
		GenerationEpoch: 1, InputEvent: event,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: fixture.sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message",
			ReplyToMessageID: promptID, Visibility: "public",
		},
	}
}

func setClarificationReplyTarget(input *AcceptInput, promptID modulecore.MessageID) {
	input.InputMessage.ReplyToMessageID = promptID
	input.InputEvent.Payload["reply_to_message_id"] = string(promptID)
}

func clarificationReplyContinuationInput(fixture readbackFixture, replyTurnID modulecore.TurnID, promptID modulecore.MessageID,
	operationID string, revision uint64, instruction string) ClarificationReplyContinuationInput {
	return ClarificationReplyContinuationInput{
		OperationID: operationID, OwnerID: "user-1", SessionID: fixture.sessionID, ReplyTurnID: replyTurnID,
		PromptMessageID: promptID, DelegationID: fixture.delegationID, Revision: revision, GenerationEpoch: 1,
		Instruction: instruction,
		InvocationReceipt: agent.InvocationReceipt{
			RequestID: "mio-request-" + operationID, ResponseID: "mio-response-" + operationID,
			ProviderLabel: "mio-local", TokensUsed: 17, Status: "completed",
		},
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		ContextEventID: modulecore.NewEventID(), ReceiptEventID: modulecore.NewEventID(),
		OccurredAt: fixture.input.OccurredAt.Add(2 * time.Second),
	}
}

func readbackForRevision(fixture readbackFixture, revision uint64, status conversation.ReadbackStatus, operationID string) ReadbackInput {
	input := fixture.input
	input.OperationID = operationID
	input.Revision = revision
	input.Readback.DelegationID = fixture.delegationID
	input.Readback.Revision = revision
	input.Readback.Sender = fixture.assignee
	input.Readback.Status = status
	input.Readback.AcknowledgedConstraintKeys = nil
	if status == conversation.ReadbackStatusAccepted {
		input.Readback.AcknowledgedConstraintKeys = []string{"keep_scope"}
	}
	input.Readback.Text = "I understand the current delegation instruction."
	input.EventID = modulecore.NewEventID()
	input.MessageID = modulecore.NewMessageID()
	input.OccurredAt = fixture.input.OccurredAt.Add(time.Duration(revision+1) * time.Minute)
	return input
}

func clarificationDecisionInputForRevision(fixture readbackFixture, controlTurnID modulecore.TurnID, revision uint64,
	decision conversation.ClarificationDecision) ClarificationDecisionInput {
	input := clarificationDecisionInput(fixture, controlTurnID, decision)
	input.Revision = revision
	if decision.Kind == conversation.ClarificationDecisionContinueDelegation {
		input.ContextEventID = modulecore.NewEventID()
	}
	return input
}
