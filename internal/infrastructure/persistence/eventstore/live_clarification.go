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
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrClarificationConflict      = errors.New("clarification control Turn or delegation revision conflicts")
	ErrClarificationRevisionLimit = errors.New("clarification continuation revision limit reached")
)

type ClarificationControlContextInput struct {
	SessionID     modulecore.SessionID
	ControlTurnID modulecore.TurnID
	OwnerID       string
	Assignee      string
}

// ClarificationControlContext is read from the canonical private EventStore
// handoff and question records. It is never reconstructed from a user-facing
// conversation tail.
type ClarificationControlContext struct {
	ControlTurn       AcceptedTurn                 `json:"control_turn"`
	OwnerID           string                       `json:"owner_id"`
	OriginUserActorID string                       `json:"origin_user_actor_id"`
	Handoff           conversation.HandoffEnvelope `json:"handoff"`
	QuestionEvent     modulecore.EventEnvelope     `json:"question_event"`
	QuestionMessage   MessageIndex                 `json:"question_message"`
	Context           []DelegationContextMessage   `json:"context"`
}

type ClarificationDecisionInput struct {
	OperationID       string
	SessionID         modulecore.SessionID
	ControlTurnID     modulecore.TurnID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Decision          conversation.ClarificationDecision
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ContextEventID    modulecore.EventID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

// ClarificationTerminalInput records a deterministic safe terminal after Mio
// generation failed, returned invalid output, or exceeded the continuation
// revision bound. It contains no model error text or generated user content.
type ClarificationTerminalInput struct {
	OperationID       string
	SessionID         modulecore.SessionID
	ControlTurnID     modulecore.TurnID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Reason            conversation.ClarificationTerminalReason
	AttemptedKind     conversation.ClarificationDecisionKind
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

type ClarificationDecisionCommit struct {
	OperationID       string                                   `json:"operation_id"`
	SessionID         modulecore.SessionID                     `json:"session_id"`
	ControlTurnID     modulecore.TurnID                        `json:"control_turn_id"`
	DelegationID      modulecore.RequestID                     `json:"delegation_id"`
	Revision          uint64                                   `json:"revision"`
	Kind              conversation.ClarificationDecisionKind   `json:"kind"`
	TerminalReason    conversation.ClarificationTerminalReason `json:"terminal_reason,omitempty"`
	ResultState       string                                   `json:"result_state"`
	InvocationReceipt agent.InvocationReceipt                  `json:"invocation_receipt"`
	Event             modulecore.EventEnvelope                 `json:"event"`
	ReceiptEvent      modulecore.EventEnvelope                 `json:"receipt_event"`
	Message           MessageIndex                             `json:"message"`
	Events            []modulecore.EventEnvelope               `json:"events"`
	MessageIndexes    []MessageIndex                           `json:"message_indexes"`
	NextHandoff       *conversation.HandoffEnvelope            `json:"next_handoff,omitempty"`
	DispatchQueued    bool                                     `json:"dispatch_queued"`
}

// LoadClarificationControlContext validates and returns the exact accepted
// private assignee question and original handoff context bound to one control
// Turn. It fails closed when that Turn is stale, superseded, or addressed to a
// different owner or assignee.
func (s *SQLiteStore) LoadClarificationControlContext(ctx context.Context, input ClarificationControlContextInput) (ClarificationControlContext, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationControlContext{}, err
	}
	if ctx == nil || input.SessionID.Validate() != nil || input.ControlTurnID.Validate() != nil ||
		strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) ||
		strings.TrimSpace(input.Assignee) == "" || input.Assignee != strings.TrimSpace(input.Assignee) {
		return ClarificationControlContext{}, errors.New("clarification context requires context, session, control Turn, owner, and assignee")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClarificationControlContext{}, err
	}
	defer tx.Rollback()
	loaded, err := loadClarificationControlContextTx(ctx, tx, input)
	if err != nil {
		return ClarificationControlContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClarificationControlContext{}, err
	}
	return loaded, nil
}

func loadClarificationControlContextTx(ctx context.Context, tx *sql.Tx, input ClarificationControlContextInput) (ClarificationControlContext, error) {
	return loadClarificationControlContextWithStateTx(ctx, tx, input, "accepted", true)
}

func loadClarificationReplyControlContextTx(ctx context.Context, tx *sql.Tx, input ClarificationControlContextInput) (ClarificationControlContext, error) {
	return loadClarificationControlContextWithStateTx(ctx, tx, input, "completed", true)
}

func loadCommittedClarificationReplyControlContextTx(ctx context.Context, tx *sql.Tx, input ClarificationControlContextInput) (ClarificationControlContext, error) {
	return loadClarificationControlContextWithStateTx(ctx, tx, input, "completed", false)
}

func loadClarificationControlContextWithStateTx(ctx context.Context, tx *sql.Tx, input ClarificationControlContextInput, expectedTurnState string, requireLatest bool) (ClarificationControlContext, error) {
	var turn AcceptedTurn
	var ownerID, triggerKind, state, sourceTurnID, delegationID, submissionID, acceptedOperationID string
	var admissionSeq, generationEpoch int64
	err := tx.QueryRowContext(ctx, `
		SELECT turn_id,session_id,owner_id,actor_id,trigger_kind,client_submission_id,accept_receipt_id,input_message_id,
		       admission_seq,generation_epoch,state,COALESCE(source_turn_id,''),COALESCE(delegation_id,'')
		FROM live_conversation_turn WHERE turn_id=?`, string(input.ControlTurnID)).Scan(
		&turn.TurnID, &turn.SessionID, &ownerID, &turn.ActorID, &triggerKind, &submissionID, &acceptedOperationID,
		&turn.InputMessageID, &admissionSeq, &generationEpoch, &state, &sourceTurnID, &delegationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	if err != nil {
		return ClarificationControlContext{}, err
	}
	turn.OperationID = acceptedOperationID
	turn.ClientSubmissionID = submissionID
	turn.GenerationEpoch = generationEpoch
	turn.AdmissionSeq = modulecore.EventSeq(admissionSeq)
	turn.State = state
	turn.TriggerKind = triggerKind
	if input.SessionID != turn.SessionID || input.OwnerID != ownerID || input.Assignee != turn.ActorID || triggerKind != "clarification_control" ||
		state != expectedTurnState || sourceTurnID == "" || delegationID == "" || submissionID == "" {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	turn.InputEvent, err = eventForIndexedMessage(ctx, tx, turn.InputMessageID)
	if err != nil {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	var revision int64
	revisionQuery := `SELECT revision FROM live_delegation WHERE delegation_id=? AND state='needs_clarification'`
	revisionArgs := []any{delegationID}
	if requireLatest {
		revisionQuery += ` AND revision=(SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?)`
		revisionArgs = append(revisionArgs, delegationID)
	} else {
		revisionQuery += ` AND revision=?`
		revisionArgs = append(revisionArgs, payloadUint64Field(turn.InputEvent.Payload, "delegation_revision"))
	}
	if err := tx.QueryRowContext(ctx, revisionQuery, revisionArgs...).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClarificationControlContext{}, ErrClarificationConflict
		}
		return ClarificationControlContext{}, err
	}
	if revision < 1 || submissionID != clarificationControlSubmissionID(modulecore.RequestID(delegationID), payloadUint64Field(turn.InputEvent.Payload, "delegation_revision")) ||
		turn.InputEvent.EventType != "conversation.clarification_question" || turn.InputEvent.ComponentID != "conversation_runtime" ||
		turn.InputEvent.SessionID != input.SessionID || turn.InputEvent.TurnID != input.ControlTurnID ||
		turn.InputEvent.RequestID != modulecore.RequestID(delegationID) ||
		payloadString(turn.InputEvent.Payload, "delegation_id") != delegationID ||
		payloadUint64Field(turn.InputEvent.Payload, "delegation_revision") != uint64(revision) {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	var message MessageIndex
	err = tx.QueryRowContext(ctx, `
		SELECT message_id,event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE message_id=?`, string(turn.InputMessageID)).Scan(
		&message.MessageID, &message.EventID, &message.SessionID, &message.TurnID, &message.FromActor,
		&message.ToKind, &message.ToActor, &message.Kind, &message.ReplyToMessageID, &message.Visibility,
	)
	if err != nil || message.MessageID != turn.InputMessageID || message.EventID != turn.InputEvent.EventID ||
		message.SessionID != input.SessionID || message.TurnID != input.ControlTurnID || message.FromActor != input.Assignee ||
		message.ToKind != "agent" || message.ToActor != "mio" || message.Kind != "clarification_question" || message.Visibility != "private" ||
		payloadString(turn.InputEvent.Payload, "status") != string(conversation.ReadbackStatusNeedsClarification) ||
		payloadString(turn.InputEvent.Payload, "visibility") != "private" || payloadString(turn.InputEvent.Payload, "from") != input.Assignee ||
		payloadString(turn.InputEvent.Payload, "to") != "mio" || payloadString(turn.InputEvent.Payload, "text") == "" {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	delegation, err := loadReadbackDelegation(ctx, tx, modulecore.RequestID(delegationID), uint64(revision))
	if err != nil || delegation.originTurnID != sourceTurnID || delegation.assignee != input.Assignee ||
		delegation.delegationState != string(conversation.ReadbackStatusNeedsClarification) || delegation.sessionID != string(input.SessionID) ||
		delegation.sessionOwnerID != input.OwnerID {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return ClarificationControlContext{}, err
	}
	handoff, _, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, ReadbackInput{
		DelegationID: modulecore.RequestID(delegationID), Revision: uint64(revision), Assignee: input.Assignee,
	})
	if err != nil {
		return ClarificationControlContext{}, err
	}
	questionText := payloadString(turn.InputEvent.Payload, "text")
	if turn.InputEvent.TraceID != handoff.SourceTraceID || message.ReplyToMessageID != handoff.ReplyTo ||
		payloadString(turn.InputEvent.Payload, "reply_to_message_id") != string(handoff.ReplyTo) {
		return ClarificationControlContext{}, ErrClarificationConflict
	}
	question := conversation.ClarificationControlContext{
		Handoff: handoff, QuestionDelegationID: modulecore.RequestID(delegationID), QuestionRevision: uint64(revision),
		QuestionMessageID: turn.InputMessageID, QuestionSender: input.Assignee, QuestionText: questionText,
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return ClarificationControlContext{}, err
	}
	contextMessages, err := loadAndValidateDelegationContext(ctx, tx, contextEvent, handoff, delegation)
	if err != nil {
		return ClarificationControlContext{}, err
	}
	return ClarificationControlContext{
		ControlTurn: turn, OwnerID: ownerID, OriginUserActorID: delegation.originActorID,
		Handoff: question.Handoff, QuestionEvent: turn.InputEvent,
		QuestionMessage: message, Context: contextMessages,
	}, nil
}

// CommitClarificationDecision atomically persists a public user prompt or a
// same-assignee continuation, its message index, control Turn state, next
// delegation revision/outbox when applicable, and an idempotent receipt.
func (s *SQLiteStore) CommitClarificationDecision(ctx context.Context, input ClarificationDecisionInput) (ClarificationDecisionCommit, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if ctx == nil {
		return ClarificationDecisionCommit{}, errors.New("clarification decision context is required")
	}
	if err := validateClarificationDecisionInput(input); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	inputHash, err := clarificationDecisionInputHash(input)
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
		if receipt.operationKind != "commit_clarification_decision" || receipt.inputSHA256 != inputHash {
			return ClarificationDecisionCommit{}, ErrOperationConflict
		}
		var result ClarificationDecisionCommit
		if err := json.Unmarshal([]byte(receipt.resultJSON), &result); err != nil {
			return ClarificationDecisionCommit{}, fmt.Errorf("decode clarification decision receipt: %w", err)
		}
		if err := validateStoredClarificationDecisionCommit(result, input); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		if err := tx.Commit(); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		committed = true
		return result, nil
	}

	// The runtime does not send actor labels as authority. Load the trusted
	// owner and assignee from the persisted control Turn, then revalidate.
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

	result, err := s.commitClarificationDecisionTx(ctx, tx, input, control)
	if err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "commit_clarification_decision", inputHash, result); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClarificationDecisionCommit{}, err
	}
	committed = true
	return result, nil
}

func (s *SQLiteStore) commitClarificationDecisionTx(ctx context.Context, tx *sql.Tx, input ClarificationDecisionInput, control ClarificationControlContext) (ClarificationDecisionCommit, error) {
	handoff := control.Handoff
	question := control.QuestionEvent
	var events []modulecore.EventEnvelope
	var indexes []MessageIndex
	var primary modulecore.EventEnvelope
	var receiptEvent modulecore.EventEnvelope
	var message MessageIndex
	var nextHandoff *conversation.HandoffEnvelope
	resultState := "completed"
	dispatchQueued := false
	if input.Decision.Kind == conversation.ClarificationDecisionAskUser {
		primary = modulecore.EventEnvelope{
			SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID, TraceID: handoff.SourceTraceID,
			CausationEventID: question.EventID, EventType: "agent.response", ComponentID: "orchestrator",
			OccurredAt: input.OccurredAt.UTC(), SessionID: input.SessionID, TurnID: input.ControlTurnID,
			MessageID: input.MessageID,
			Payload: map[string]any{
				"kind": "clarification_prompt", "status": "awaiting_user_clarification", "availability": "free",
				"client_submission_id": control.ControlTurn.ClientSubmissionID,
				"delegation_id":        string(input.DelegationID), "delegation_revision": input.Revision,
				"visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user",
				"content": input.Decision.Question, "text": input.Decision.Question,
				"reply_to_message_id": string(handoff.ReplyTo),
			},
		}
		message = MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: input.SessionID, TurnID: input.ControlTurnID,
			FromActor: "mio", ToKind: "user", ToActor: control.OwnerID, Kind: "clarification_prompt",
			ReplyToMessageID: handoff.ReplyTo, Visibility: "public",
		}
		receiptEvent = clarificationReceiptEvent(input, question)
		if err := s.insertClarificationEvents(ctx, tx, []modulecore.EventEnvelope{primary, receiptEvent}, []MessageIndex{message}, input.SessionID, input.ControlTurnID); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		if err := insertConversationProjectionWork(ctx, tx, primary, message); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		primary = primaryFromPersistedEvent(ctx, tx, input.EventID)
		receiptEvent = primaryFromPersistedEvent(ctx, tx, input.ReceiptEventID)
		if primary.EventID == "" || receiptEvent.EventID == "" {
			return ClarificationDecisionCommit{}, errors.New("persisted clarification prompt could not be reloaded")
		}
		events, indexes = []modulecore.EventEnvelope{primary, receiptEvent}, []MessageIndex{message}
	} else {
		if input.Revision >= conversation.MaxClarificationRevision || input.Revision >= math.MaxInt64 {
			return ClarificationDecisionCommit{}, ErrClarificationRevisionLimit
		}
		delegation, err := loadReadbackDelegation(ctx, tx, input.DelegationID, input.Revision)
		if err != nil {
			return ClarificationDecisionCommit{}, err
		}
		oldContext, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
		if err != nil {
			return ClarificationDecisionCommit{}, err
		}
		if _, err := loadAndValidateDelegationContext(ctx, tx, oldContext, handoff, delegation); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		newHandoff := handoff
		newHandoff.Revision = input.Revision + 1
		newHandoff.Instruction = input.Decision.Instruction
		newHandoff.InstructionMessageID = input.MessageID
		if err := conversation.ValidateHandoffEnvelope(newHandoff); err != nil {
			return ClarificationDecisionCommit{}, fmt.Errorf("validate continued HandoffEnvelope: %w", err)
		}
		contextPayload, err := cloneEventPayload(oldContext.Payload)
		if err != nil {
			return ClarificationDecisionCommit{}, err
		}
		contextPayload["handoff"] = newHandoff
		contextEvent := modulecore.EventEnvelope{
			SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.ContextEventID, TraceID: handoff.SourceTraceID,
			CausationEventID: oldContext.CausationEventID, EventType: "conversation.delegation_context", ComponentID: "conversation_runtime",
			OccurredAt: input.OccurredAt.UTC(), SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
			RequestID: input.DelegationID, Payload: contextPayload,
		}
		primary = modulecore.EventEnvelope{
			SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID, TraceID: handoff.SourceTraceID,
			CausationEventID: question.EventID, EventType: "conversation.delegation_instruction", ComponentID: "delegation_mailbox",
			OccurredAt: input.OccurredAt.UTC(), SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
			RequestID: input.DelegationID, MessageID: input.MessageID, ActorKind: "agent", ActorID: "mio",
			Payload: map[string]any{
				"kind": "delegation_instruction", "delegation_id": string(input.DelegationID), "delegation_revision": input.Revision + 1,
				"visibility": "private", "from": "mio", "source_actor_id": "mio", "to": handoff.ToActor,
				"content": input.Decision.Instruction, "text": input.Decision.Instruction,
			},
		}
		message = MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
			FromActor: "mio", ToKind: "agent", ToActor: handoff.ToActor, Kind: "delegation_instruction",
			ReplyToMessageID: handoff.ReplyTo, Visibility: "private",
		}
		receiptEvent = clarificationReceiptEvent(input, question)
		if err := s.insertClarificationEvents(ctx, tx, []modulecore.EventEnvelope{contextEvent, primary, receiptEvent}, []MessageIndex{message}, input.SessionID, handoff.OriginTurnID); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		primary = primaryFromPersistedEvent(ctx, tx, input.EventID)
		receiptEvent = primaryFromPersistedEvent(ctx, tx, input.ReceiptEventID)
		contextEvent, err = loadEventEnvelopeTx(ctx, tx, input.ContextEventID)
		if primary.EventID == "" || receiptEvent.EventID == "" || err != nil {
			return ClarificationDecisionCommit{}, errors.New("persisted continuation events could not be reloaded")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO live_delegation
			(delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision)
			VALUES(?,?,?,?,?,?,'dispatch_accepted',NULL,0)`,
			string(input.DelegationID), int64(input.Revision+1), string(handoff.OriginTurnID), handoff.ToActor,
			string(input.MessageID), string(input.ContextEventID)); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		work := WorkItem{
			WorkID:    fmt.Sprintf("dispatch:%s:%d", input.DelegationID, input.Revision+1),
			DedupeKey: readbackDispatchDedupeKey(input.DelegationID, input.Revision+1), Kind: "dispatch_delegation",
			SourceEventID: input.EventID, SessionID: input.SessionID, Priority: 5,
		}
		if err := insertClarificationDispatch(ctx, tx, work); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		if err := conversation.ValidateHandoffEnvelope(newHandoff); err != nil {
			return ClarificationDecisionCommit{}, err
		}
		copyHandoff := newHandoff
		nextHandoff = &copyHandoff
		events, indexes = []modulecore.EventEnvelope{contextEvent, primary, receiptEvent}, []MessageIndex{message}
		resultState, dispatchQueued = "awaiting_readback", true
	}

	result, err := tx.ExecContext(ctx, `UPDATE live_conversation_turn SET state=?,result_receipt_id=?
		WHERE turn_id=? AND session_id=? AND generation_epoch=? AND trigger_kind='clarification_control'
		AND delegation_id=? AND state='accepted'`, resultState, input.OperationID, string(input.ControlTurnID),
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
		DelegationID: input.DelegationID, Revision: input.Revision, Kind: input.Decision.Kind,
		ResultState: resultState, InvocationReceipt: input.InvocationReceipt,
		Event: primary, ReceiptEvent: receiptEvent, Message: message, Events: events,
		MessageIndexes: indexes, NextHandoff: nextHandoff, DispatchQueued: dispatchQueued,
	}, nil
}

func (s *SQLiteStore) insertClarificationEvents(ctx context.Context, tx *sql.Tx, events []modulecore.EventEnvelope, indexes []MessageIndex, sessionID modulecore.SessionID, messageTurnID modulecore.TurnID) error {
	seenIDs := make(map[modulecore.EventID]struct{}, len(events))
	for i := range events {
		event := events[i]
		seq, err := nextEventSeq(ctx, tx)
		if err != nil {
			return err
		}
		event.EventSeq = seq
		if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
			return err
		}
		if _, duplicate := seenIDs[event.EventID]; duplicate {
			return errors.New("clarification decision contains duplicate event IDs")
		}
		seenIDs[event.EventID] = struct{}{}
		if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
			return err
		}
		if err := s.validateReferences(ctx, tx, event); err != nil {
			return err
		}
		persisted, err := insertLiveEvent(ctx, tx, event)
		if err != nil {
			return err
		}
		events[i] = persisted
	}
	for _, index := range indexes {
		event := events[0]
		for _, candidate := range events {
			if candidate.EventID == index.EventID {
				event = candidate
				break
			}
		}
		if err := validateMessageIndex(index, event, sessionID, messageTurnID); err != nil {
			return fmt.Errorf("validate clarification decision message index: %w", err)
		}
		if err := validateReplyTarget(ctx, tx, index.ReplyToMessageID, sessionID); err != nil {
			return err
		}
		if err := insertMessageIndex(ctx, tx, index); err != nil {
			return err
		}
	}
	return nil
}

func clarificationReceiptEvent(input ClarificationDecisionInput, question modulecore.EventEnvelope) modulecore.EventEnvelope {
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

func insertClarificationDispatch(ctx context.Context, tx *sql.Tx, work WorkItem) error {
	var source modulecore.EventEnvelope
	var err error
	source, err = loadEventEnvelopeTx(ctx, tx, work.SourceEventID)
	if err != nil || source.EventType != "conversation.delegation_instruction" || source.SessionID != work.SessionID {
		return ErrClarificationConflict
	}
	if err := validateOutboxSource(ctx, tx, work.SourceEventID, string(source.TraceID), work.SessionID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO live_work_outbox
		(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES(?,?,'dispatch_delegation',?,?,?,'pending',0,'',1)`,
		work.WorkID, work.DedupeKey, string(work.SourceEventID), string(work.SessionID), work.Priority)
	return err
}

func cloneEventPayload(payload map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil || cloned == nil {
		return nil, errors.New("stored delegation context payload is malformed")
	}
	return cloned, nil
}

func validateClarificationDecisionInput(input ClarificationDecisionInput) error {
	if strings.TrimSpace(input.OperationID) == "" || input.OperationID != strings.TrimSpace(input.OperationID) {
		return errors.New("clarification operation_id is required and must be trimmed")
	}
	if input.SessionID.Validate() != nil || input.ControlTurnID.Validate() != nil || input.DelegationID.Validate() != nil {
		return errors.New("clarification session, control Turn, or delegation ID is invalid")
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 || input.GenerationEpoch <= 0 {
		return errors.New("clarification revision and generation epoch must be positive and bounded")
	}
	if input.EventID.Validate() != nil || input.MessageID.Validate() != nil || input.ReceiptEventID.Validate() != nil ||
		input.ReceiptEventID == input.EventID || input.OccurredAt.IsZero() {
		return errors.New("clarification output event, message, and occurrence time are required")
	}
	if err := validateClarificationInvocationReceipt(input.InvocationReceipt); err != nil || input.InvocationReceipt.Status != "completed" {
		return errors.New("clarification invocation receipt is invalid")
	}
	if input.Decision.SchemaVersion != "mio.clarification.v1" {
		return errors.New("clarification decision schema version is invalid")
	}
	switch input.Decision.Kind {
	case conversation.ClarificationDecisionAskUser:
		if strings.TrimSpace(input.Decision.Question) == "" || len([]byte(input.Decision.Question)) > conversation.MaxUserMessageBytes ||
			input.Decision.Instruction != "" || input.ContextEventID != "" {
			return errors.New("ask_user clarification decision fields are invalid")
		}
	case conversation.ClarificationDecisionContinueDelegation:
		if strings.TrimSpace(input.Decision.Instruction) == "" || len([]byte(input.Decision.Instruction)) > conversation.MaxInstructionBytes ||
			input.Decision.Question != "" || input.ContextEventID.Validate() != nil || input.ContextEventID == input.EventID ||
			input.ContextEventID == input.ReceiptEventID {
			return errors.New("continue_delegation clarification decision fields are invalid")
		}
	default:
		return errors.New("clarification decision kind is invalid")
	}
	return nil
}

func clarificationDecisionInputHash(input ClarificationDecisionInput) (string, error) {
	return hashJSON(struct {
		SessionID         modulecore.SessionID               `json:"session_id"`
		ControlTurnID     modulecore.TurnID                  `json:"control_turn_id"`
		DelegationID      modulecore.RequestID               `json:"delegation_id"`
		Revision          uint64                             `json:"revision"`
		GenerationEpoch   int64                              `json:"generation_epoch"`
		Decision          conversation.ClarificationDecision `json:"decision"`
		InvocationReceipt agent.InvocationReceipt            `json:"invocation_receipt"`
	}{input.SessionID, input.ControlTurnID, input.DelegationID, input.Revision, input.GenerationEpoch, input.Decision, input.InvocationReceipt})
}

func validateStoredClarificationDecisionCommit(result ClarificationDecisionCommit, input ClarificationDecisionInput) error {
	if result.OperationID != input.OperationID || result.SessionID != input.SessionID || result.ControlTurnID != input.ControlTurnID ||
		result.DelegationID != input.DelegationID || result.Revision != input.Revision || result.Kind != input.Decision.Kind ||
		result.TerminalReason != "" ||
		result.Event.EventID == "" || result.Event.EventSeq <= 0 || result.Message.EventID != result.Event.EventID ||
		result.Message.MessageID != result.Event.MessageID || len(result.Events) == 0 || len(result.MessageIndexes) != 1 ||
		result.MessageIndexes[0] != result.Message || result.InvocationReceipt != input.InvocationReceipt ||
		result.ReceiptEvent.EventID == "" || result.ReceiptEvent.EventSeq <= 0 ||
		result.ReceiptEvent.EventType != "conversation.mio_emission_receipt" ||
		result.ReceiptEvent.ComponentID != "conversation_runtime" || result.ReceiptEvent.SessionID != result.SessionID ||
		result.ReceiptEvent.TurnID != result.ControlTurnID || result.ReceiptEvent.TraceID != result.Event.TraceID ||
		result.ReceiptEvent.CausationEventID != result.Event.CausationEventID || result.ReceiptEvent.MessageID != "" ||
		result.ReceiptEvent.RequestID != "" || result.ReceiptEvent.ActorKind != "" || result.ReceiptEvent.ActorID != "" ||
		payloadString(result.ReceiptEvent.Payload, "status") != result.InvocationReceipt.Status ||
		payloadString(result.ReceiptEvent.Payload, "request_id") != result.InvocationReceipt.RequestID ||
		payloadString(result.ReceiptEvent.Payload, "response_id") != result.InvocationReceipt.ResponseID {
		return errors.New("stored clarification decision receipt is inconsistent")
	}
	expectedReceiptKeys := 1
	if result.InvocationReceipt.RequestID != "" {
		expectedReceiptKeys++
	}
	if result.InvocationReceipt.ResponseID != "" {
		expectedReceiptKeys++
	}
	if len(result.ReceiptEvent.Payload) != expectedReceiptKeys || !reflect.DeepEqual(result.Events[len(result.Events)-1], result.ReceiptEvent) {
		return errors.New("stored clarification invocation receipt event is inconsistent")
	}
	if result.Kind == conversation.ClarificationDecisionAskUser {
		if result.ResultState != "completed" || result.DispatchQueued || result.NextHandoff != nil ||
			result.Event.EventType != "agent.response" || result.Event.ComponentID != "orchestrator" ||
			result.Event.RequestID != "" || result.Event.ActorKind != "" || result.Event.ActorID != "" ||
			result.Message.Visibility != "public" || result.Message.Kind != "clarification_prompt" || len(result.Events) != 2 ||
			result.Message.ToKind != "user" || result.Message.ReplyToMessageID != modulecore.MessageID(payloadString(result.Event.Payload, "reply_to_message_id")) ||
			payloadString(result.Event.Payload, "kind") != "clarification_prompt" || payloadString(result.Event.Payload, "visibility") != "public" ||
			payloadString(result.Event.Payload, "availability") != "free" || payloadString(result.Event.Payload, "delegation_id") != string(input.DelegationID) ||
			payloadUint64Field(result.Event.Payload, "delegation_revision") != input.Revision ||
			!reflect.DeepEqual(result.Events[0], result.Event) {
			return errors.New("stored ask_user clarification receipt is inconsistent")
		}
	} else if result.Kind == conversation.ClarificationDecisionContinueDelegation {
		if result.ResultState != "awaiting_readback" || !result.DispatchQueued || result.NextHandoff == nil ||
			result.NextHandoff.DelegationID != input.DelegationID || result.NextHandoff.Revision != input.Revision+1 ||
			result.Event.EventType != "conversation.delegation_instruction" || result.Message.Visibility != "private" ||
			result.Message.ToKind != "agent" || result.Message.ToActor != result.NextHandoff.ToActor || len(result.Events) != 3 ||
			result.Events[0].EventType != "conversation.delegation_context" || !reflect.DeepEqual(result.Events[1], result.Event) {
			return errors.New("stored continuation clarification receipt is inconsistent")
		}
	}
	return nil
}

func (s *SQLiteStore) LoadClarificationDecisionOutcome(ctx context.Context, controlTurnID modulecore.TurnID) (ClarificationDecisionCommit, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationDecisionCommit{}, false, err
	}
	if ctx == nil || controlTurnID.Validate() != nil {
		return ClarificationDecisionCommit{}, false, errors.New("clarification outcome requires context and control Turn ID")
	}
	var sessionID, receiptID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_id,result_receipt_id FROM live_conversation_turn WHERE turn_id=? AND trigger_kind='clarification_control'`, string(controlTurnID)).Scan(&sessionID, &receiptID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !receiptID.Valid) {
		return ClarificationDecisionCommit{}, false, nil
	}
	if err != nil {
		return ClarificationDecisionCommit{}, false, err
	}
	var operationKind, resultJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT operation_kind,result_json FROM live_operation_receipt WHERE operation_id=?`, receiptID.String).Scan(&operationKind, &resultJSON); err != nil {
		return ClarificationDecisionCommit{}, false, err
	}
	if operationKind == "commit_clarification_terminal" {
		var terminal ClarificationDecisionCommit
		if err := json.Unmarshal([]byte(resultJSON), &terminal); err != nil {
			return ClarificationDecisionCommit{}, false, fmt.Errorf("decode clarification terminal outcome: %w", err)
		}
		var committedSession, committedReceipt sql.NullString
		var turnState, delegationState string
		stateErr := s.db.QueryRowContext(ctx, `SELECT t.session_id,t.result_receipt_id,t.state,d.state
			FROM live_conversation_turn t JOIN live_delegation d ON d.delegation_id=? AND d.revision=?
			WHERE t.turn_id=? AND t.trigger_kind='clarification_control'`, string(terminal.DelegationID), int64(terminal.Revision), string(controlTurnID)).Scan(
			&committedSession, &committedReceipt, &turnState, &delegationState)
		if stateErr != nil || terminal.OperationID != receiptID.String || terminal.ControlTurnID != controlTurnID ||
			terminal.SessionID != modulecore.SessionID(sessionID.String) || committedSession.String != sessionID.String ||
			committedReceipt.String != receiptID.String || turnState != "completed" || delegationState != "needs_attention" ||
			validateClarificationTerminalBundle(terminal) != nil {
			return ClarificationDecisionCommit{}, false, errors.New("stored clarification terminal outcome identity is inconsistent")
		}
		return terminal, true, nil
	}
	if operationKind != "commit_clarification_decision" {
		return ClarificationDecisionCommit{}, false, errors.New("clarification control Turn references a different receipt kind")
	}
	var result ClarificationDecisionCommit
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return ClarificationDecisionCommit{}, false, fmt.Errorf("decode clarification decision outcome: %w", err)
	}
	if result.ControlTurnID != controlTurnID || result.SessionID != modulecore.SessionID(sessionID.String) ||
		validateStoredClarificationDecisionCommit(result, ClarificationDecisionInput{
			OperationID: result.OperationID, SessionID: result.SessionID, ControlTurnID: result.ControlTurnID,
			DelegationID: result.DelegationID, Revision: result.Revision, Decision: conversation.ClarificationDecision{Kind: result.Kind},
			InvocationReceipt: result.InvocationReceipt, ReceiptEventID: result.ReceiptEvent.EventID,
		}) != nil {
		return ClarificationDecisionCommit{}, false, errors.New("stored clarification outcome identity is inconsistent")
	}
	return result, true, nil
}

func eventForIndexedMessage(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID) (modulecore.EventEnvelope, error) {
	var eventID string
	if err := tx.QueryRowContext(ctx, `SELECT event_id FROM live_message_index WHERE message_id=?`, string(messageID)).Scan(&eventID); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	return loadEventEnvelopeTx(ctx, tx, modulecore.EventID(eventID))
}

func primaryFromPersistedEvent(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID) modulecore.EventEnvelope {
	event, err := loadEventEnvelopeTx(ctx, tx, eventID)
	if err != nil {
		return modulecore.EventEnvelope{}
	}
	return event
}
