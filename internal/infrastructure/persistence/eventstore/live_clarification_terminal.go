package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// CommitClarificationTerminal durably ends a control Turn after a bounded,
// known clarification failure. The public message, private Mio receipt, Turn
// state, and exact operation result are committed together.
func (s *SQLiteStore) CommitClarificationTerminal(ctx context.Context, input ClarificationTerminalInput) (ClarificationDecisionCommit, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if ctx == nil {
		return ClarificationDecisionCommit{}, errors.New("clarification terminal context is required")
	}
	if err := validateClarificationTerminalInput(input); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	inputHash, err := clarificationTerminalInputHash(input)
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return ClarificationDecisionCommit{}, err
	} else if found {
		if receipt.operationKind != "commit_clarification_terminal" || receipt.inputSHA256 != inputHash {
			return ClarificationDecisionCommit{}, ErrOperationConflict
		}
		var result ClarificationDecisionCommit
		if err := json.Unmarshal([]byte(receipt.resultJSON), &result); err != nil {
			return ClarificationDecisionCommit{}, fmt.Errorf("decode clarification terminal receipt: %w", err)
		}
		if err := validateStoredClarificationTerminalCommit(result, input); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		if err := tx.Commit(); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		committed = true
		return result, nil
	}

	var ownerID, assignee string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,actor_id FROM live_conversation_turn WHERE turn_id=?`, string(input.ControlTurnID)).Scan(&ownerID, &assignee); err != nil {
		return ClarificationDecisionCommit{}, ErrClarificationConflict
	}
	control, err := loadClarificationControlContextTx(ctx, tx, ClarificationControlContextInput{
		SessionID: input.SessionID, ControlTurnID: input.ControlTurnID, OwnerID: ownerID, Assignee: assignee,
	})
	if err != nil || control.Handoff.DelegationID != input.DelegationID || control.Handoff.Revision != input.Revision ||
		control.ControlTurn.GenerationEpoch != input.GenerationEpoch {
		return ClarificationDecisionCommit{}, ErrClarificationConflict
	}

	result, err := s.commitClarificationTerminalTx(ctx, tx, input, control)
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "commit_clarification_terminal", inputHash, result); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	committed = true
	return result, nil
}

func (s *SQLiteStore) commitClarificationTerminalTx(ctx context.Context, tx *sql.Tx, input ClarificationTerminalInput, control ClarificationControlContext) (ClarificationDecisionCommit, error) {
	handoff, question := control.Handoff, control.QuestionEvent
	primary := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       input.EventID, TraceID: handoff.SourceTraceID, CausationEventID: question.EventID,
		EventType: "agent.response", ComponentID: "orchestrator", OccurredAt: input.OccurredAt.UTC(),
		SessionID: input.SessionID, TurnID: input.ControlTurnID, MessageID: input.MessageID,
		Payload: map[string]any{
			"kind": "turn_error", "client_submission_id": control.ControlTurn.ClientSubmissionID,
			"availability": "free", "visibility": "public", "delegation_id": string(input.DelegationID),
			"delegation_revision": input.Revision, "from": "mio", "source_actor_id": "mio", "to": "user",
			"content": conversation.ClarificationTerminalUserText, "text": conversation.ClarificationTerminalUserText,
			"reply_to_message_id": string(handoff.ReplyTo),
		},
	}
	message := MessageIndex{
		MessageID: input.MessageID, EventID: input.EventID, SessionID: input.SessionID, TurnID: input.ControlTurnID,
		FromActor: "mio", ToKind: "user", ToActor: control.OwnerID, Kind: "turn_error",
		ReplyToMessageID: handoff.ReplyTo, Visibility: "public",
	}
	receiptEvent := clarificationTerminalReceiptEvent(input, question)
	if err := s.insertClarificationEvents(ctx, tx, []modulecore.EventEnvelope{primary, receiptEvent}, []MessageIndex{message}, input.SessionID, input.ControlTurnID); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if err := insertConversationProjectionWork(ctx, tx, primary, message); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	primary = primaryFromPersistedEvent(ctx, tx, input.EventID)
	receiptEvent = primaryFromPersistedEvent(ctx, tx, input.ReceiptEventID)
	if primary.EventID == "" || receiptEvent.EventID == "" {
		return ClarificationDecisionCommit{}, errors.New("persisted clarification terminal events could not be reloaded")
	}
	delegationUpdate, err := tx.ExecContext(ctx, `UPDATE live_delegation SET state='needs_attention'
		WHERE delegation_id=? AND revision=? AND state='needs_clarification'`,
		string(input.DelegationID), int64(input.Revision))
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	delegationRows, err := delegationUpdate.RowsAffected()
	if err != nil || delegationRows != 1 {
		return ClarificationDecisionCommit{}, ErrClarificationConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE live_conversation_turn SET state='completed',result_receipt_id=?
		WHERE turn_id=? AND session_id=? AND generation_epoch=? AND trigger_kind='clarification_control'
		AND delegation_id=? AND state='accepted'`, input.OperationID, string(input.ControlTurnID),
		string(input.SessionID), input.GenerationEpoch, string(input.DelegationID))
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ClarificationDecisionCommit{}, ErrClarificationConflict
	}
	return ClarificationDecisionCommit{
		OperationID: input.OperationID, SessionID: input.SessionID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision,
		TerminalReason: input.Reason, ResultState: "completed", InvocationReceipt: input.InvocationReceipt,
		Event: primary, ReceiptEvent: receiptEvent, Message: message,
		Events: []modulecore.EventEnvelope{primary, receiptEvent}, MessageIndexes: []MessageIndex{message},
	}, nil
}

func clarificationTerminalReceiptEvent(input ClarificationTerminalInput, question modulecore.EventEnvelope) modulecore.EventEnvelope {
	payload := map[string]any{"status": input.InvocationReceipt.Status}
	if input.InvocationReceipt.RequestID != "" {
		payload["request_id"] = input.InvocationReceipt.RequestID
	}
	if input.InvocationReceipt.ResponseID != "" {
		payload["response_id"] = input.InvocationReceipt.ResponseID
	}
	event := modulecore.NewEventEnvelope(question.TraceID, question.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", input.OccurredAt.UTC(), payload)
	event.EventID = input.ReceiptEventID
	event.SessionID, event.TurnID = input.SessionID, input.ControlTurnID
	return event
}

func validateClarificationTerminalInput(input ClarificationTerminalInput) error {
	if strings.TrimSpace(input.OperationID) == "" || input.OperationID != strings.TrimSpace(input.OperationID) {
		return errors.New("clarification terminal operation_id is required and must be trimmed")
	}
	if input.SessionID.Validate() != nil || input.ControlTurnID.Validate() != nil || input.DelegationID.Validate() != nil {
		return errors.New("clarification terminal session, control Turn, or delegation ID is invalid")
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 || input.GenerationEpoch <= 0 {
		return errors.New("clarification terminal revision and generation epoch must be positive and bounded")
	}
	if input.EventID.Validate() != nil || input.MessageID.Validate() != nil || input.ReceiptEventID.Validate() != nil ||
		input.EventID == input.ReceiptEventID || input.OccurredAt.IsZero() {
		return errors.New("clarification terminal output event, message, and occurrence time are required")
	}
	if err := validateClarificationInvocationReceipt(input.InvocationReceipt); err != nil {
		return err
	}
	switch input.Reason {
	case conversation.ClarificationTerminalGenerationError:
		if input.InvocationReceipt.Status != "generation_failed" || input.AttemptedKind != "" {
			return errors.New("clarification generation-error terminal fields are invalid")
		}
	case conversation.ClarificationTerminalInvalidOutput:
		if input.InvocationReceipt.Status != "invalid_output" || input.AttemptedKind != "" {
			return errors.New("clarification invalid-output terminal fields are invalid")
		}
	case conversation.ClarificationTerminalRevisionLimit:
		if input.Revision < conversation.MaxClarificationRevision || input.InvocationReceipt.Status != "completed" ||
			input.AttemptedKind != conversation.ClarificationDecisionContinueDelegation {
			return errors.New("clarification revision-limit terminal fields are invalid")
		}
	default:
		return errors.New("clarification terminal reason is invalid")
	}
	return nil
}

func validateClarificationInvocationReceipt(receipt agent.InvocationReceipt) error {
	if receipt.TokensUsed < 0 || receipt.RequestID != strings.TrimSpace(receipt.RequestID) ||
		receipt.ResponseID != strings.TrimSpace(receipt.ResponseID) || receipt.ProviderLabel != strings.TrimSpace(receipt.ProviderLabel) ||
		len(receipt.RequestID) > 512 || len(receipt.ResponseID) > 512 || len(receipt.ProviderLabel) > 512 {
		return errors.New("clarification invocation receipt fields are invalid")
	}
	return nil
}

func clarificationTerminalInputHash(input ClarificationTerminalInput) (string, error) {
	return hashJSON(struct {
		SessionID       modulecore.SessionID                     `json:"session_id"`
		ControlTurnID   modulecore.TurnID                        `json:"control_turn_id"`
		DelegationID    modulecore.RequestID                     `json:"delegation_id"`
		Revision        uint64                                   `json:"revision"`
		GenerationEpoch int64                                    `json:"generation_epoch"`
		Reason          conversation.ClarificationTerminalReason `json:"reason"`
		AttemptedKind   conversation.ClarificationDecisionKind   `json:"attempted_kind,omitempty"`
		Receipt         agent.InvocationReceipt                  `json:"invocation_receipt"`
	}{input.SessionID, input.ControlTurnID, input.DelegationID, input.Revision, input.GenerationEpoch, input.Reason, input.AttemptedKind, input.InvocationReceipt})
}

func validateStoredClarificationTerminalCommit(result ClarificationDecisionCommit, input ClarificationTerminalInput) error {
	if result.OperationID != input.OperationID || result.SessionID != input.SessionID || result.ControlTurnID != input.ControlTurnID ||
		result.DelegationID != input.DelegationID || result.Revision != input.Revision || result.TerminalReason != input.Reason ||
		result.Kind != "" || result.ResultState != "completed" || result.DispatchQueued || result.NextHandoff != nil ||
		result.InvocationReceipt != input.InvocationReceipt || result.Event.EventID == "" || result.Event.EventSeq <= 0 ||
		result.Message.EventID != result.Event.EventID || result.Message.MessageID != result.Event.MessageID ||
		result.ReceiptEvent.EventID == "" || result.ReceiptEvent.EventSeq <= result.Event.EventSeq || len(result.Events) != 2 ||
		len(result.MessageIndexes) != 1 || result.MessageIndexes[0] != result.Message ||
		!reflect.DeepEqual(result.Events[0], result.Event) || !reflect.DeepEqual(result.Events[1], result.ReceiptEvent) {
		return errors.New("stored clarification terminal receipt identity is inconsistent")
	}
	if err := validateClarificationTerminalInput(input); err != nil {
		return err
	}
	return validateClarificationTerminalBundle(result)
}

func validateClarificationTerminalBundle(result ClarificationDecisionCommit) error {
	if strings.TrimSpace(result.OperationID) == "" || result.OperationID != strings.TrimSpace(result.OperationID) ||
		result.SessionID.Validate() != nil || result.ControlTurnID.Validate() != nil || result.DelegationID.Validate() != nil ||
		result.Revision == 0 || result.Revision > math.MaxInt64 || result.Kind != "" || result.ResultState != "completed" ||
		result.DispatchQueued || result.NextHandoff != nil || result.Event.EventID.Validate() != nil || result.Event.EventSeq <= 0 ||
		result.ReceiptEvent.EventID.Validate() != nil || result.ReceiptEvent.EventSeq <= result.Event.EventSeq ||
		len(result.Events) != 2 || len(result.MessageIndexes) != 1 || result.MessageIndexes[0] != result.Message ||
		!reflect.DeepEqual(result.Events[0], result.Event) || !reflect.DeepEqual(result.Events[1], result.ReceiptEvent) {
		return errors.New("stored clarification terminal bundle shape is invalid")
	}
	if result.TerminalReason != conversation.ClarificationTerminalGenerationError &&
		result.TerminalReason != conversation.ClarificationTerminalInvalidOutput &&
		result.TerminalReason != conversation.ClarificationTerminalRevisionLimit {
		return errors.New("stored clarification terminal reason is invalid")
	}
	if validateClarificationInvocationReceipt(result.InvocationReceipt) != nil ||
		(result.TerminalReason == conversation.ClarificationTerminalGenerationError && result.InvocationReceipt.Status != "generation_failed") ||
		(result.TerminalReason == conversation.ClarificationTerminalInvalidOutput && result.InvocationReceipt.Status != "invalid_output") ||
		(result.TerminalReason == conversation.ClarificationTerminalRevisionLimit &&
			(result.InvocationReceipt.Status != "completed" || result.Revision < conversation.MaxClarificationRevision)) {
		return errors.New("stored clarification terminal reason does not match its receipt or revision")
	}
	if modulecore.ValidateEventEnvelope(result.Event) != nil || result.Event.EventType != "agent.response" || result.Event.ComponentID != "orchestrator" ||
		result.Event.SessionID != result.SessionID || result.Event.TurnID != result.ControlTurnID || result.Event.TraceID != result.ReceiptEvent.TraceID ||
		result.Event.CausationEventID != result.ReceiptEvent.CausationEventID || result.Event.RequestID != "" ||
		result.Event.ActorKind != "" || result.Event.ActorID != "" || result.Event.MessageID.Validate() != nil ||
		payloadString(result.Event.Payload, "kind") != "turn_error" || payloadString(result.Event.Payload, "availability") != "free" ||
		payloadString(result.Event.Payload, "visibility") != "public" || payloadString(result.Event.Payload, "delegation_id") != string(result.DelegationID) ||
		payloadUint64Field(result.Event.Payload, "delegation_revision") != result.Revision ||
		payloadString(result.Event.Payload, "content") != conversation.ClarificationTerminalUserText ||
		payloadString(result.Event.Payload, "text") != conversation.ClarificationTerminalUserText ||
		payloadString(result.Event.Payload, "from") != "mio" || payloadString(result.Event.Payload, "source_actor_id") != "mio" ||
		payloadString(result.Event.Payload, "to") != "user" || payloadString(result.Event.Payload, "reply_to_message_id") != string(result.Message.ReplyToMessageID) ||
		payloadString(result.Event.Payload, "client_submission_id") == "" ||
		len(result.Event.Payload) != 12 {
		return errors.New("stored clarification terminal public event is inconsistent")
	}
	if err := validateMessageIndex(result.Message, result.Event, result.SessionID, result.ControlTurnID); err != nil ||
		result.Message.FromActor != "mio" || result.Message.ToKind != "user" || result.Message.Kind != "turn_error" ||
		result.Message.Visibility != "public" || result.Message.ToActor == "" || result.Message.ReplyToMessageID == "" {
		return errors.New("stored clarification terminal message index is inconsistent")
	}
	if modulecore.ValidateEventEnvelope(result.ReceiptEvent) != nil || result.ReceiptEvent.EventType != "conversation.mio_emission_receipt" ||
		result.ReceiptEvent.ComponentID != "conversation_runtime" || result.ReceiptEvent.SessionID != result.SessionID ||
		result.ReceiptEvent.TurnID != result.ControlTurnID || result.ReceiptEvent.TraceID != result.Event.TraceID ||
		result.ReceiptEvent.CausationEventID != result.Event.CausationEventID || result.ReceiptEvent.MessageID != "" || result.ReceiptEvent.RequestID != "" ||
		result.ReceiptEvent.ActorKind != "" || result.ReceiptEvent.ActorID != "" ||
		payloadString(result.ReceiptEvent.Payload, "status") != result.InvocationReceipt.Status {
		return errors.New("stored clarification terminal receipt event identity is inconsistent")
	}
	expectedReceiptKeys := 1
	if result.InvocationReceipt.RequestID != "" {
		expectedReceiptKeys++
		if payloadString(result.ReceiptEvent.Payload, "request_id") != result.InvocationReceipt.RequestID {
			return errors.New("stored clarification terminal request receipt is inconsistent")
		}
	} else if _, ok := result.ReceiptEvent.Payload["request_id"]; ok {
		return errors.New("stored clarification terminal receipt contains an unbound request ID")
	}
	if result.InvocationReceipt.ResponseID != "" {
		expectedReceiptKeys++
		if payloadString(result.ReceiptEvent.Payload, "response_id") != result.InvocationReceipt.ResponseID {
			return errors.New("stored clarification terminal response receipt is inconsistent")
		}
	} else if _, ok := result.ReceiptEvent.Payload["response_id"]; ok {
		return errors.New("stored clarification terminal receipt contains an unbound response ID")
	}
	if len(result.ReceiptEvent.Payload) != expectedReceiptKeys {
		return errors.New("stored clarification terminal receipt contains non-minimal fields")
	}
	return nil
}
