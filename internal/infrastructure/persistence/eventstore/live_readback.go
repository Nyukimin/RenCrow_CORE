package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrReadbackConflict         = errors.New("delegation revision or state conflicts with readback")
	ErrReadbackAssigneeMismatch = errors.New("authenticated readback assignee does not match delegation")
)

// ReadbackInput is the storage-owned input for committing an authenticated
// agent readback. Assignee comes from the trusted transport. EventID,
// MessageID, and OccurredAt are runtime-issued envelope metadata; retries may
// provide newly generated values because the operation receipt returns the
// first committed envelope.
type ReadbackInput struct {
	OperationID  string
	DelegationID modulecore.RequestID
	Revision     uint64
	Assignee     string
	Readback     conversation.ReadbackEnvelope
	EventID      modulecore.EventID
	MessageID    modulecore.MessageID
	OccurredAt   time.Time
}

// ReadbackAcceptance is the durable result returned after the event, public
// message index, delegation/turn state, receipt, and optional task-start
// outbox item have committed together.
type ReadbackAcceptance struct {
	OperationID     string                      `json:"operation_id"`
	ReceiptID       string                      `json:"receipt_id"`
	DelegationID    modulecore.RequestID        `json:"delegation_id"`
	Revision        uint64                      `json:"revision"`
	OriginTurnID    modulecore.TurnID           `json:"origin_turn_id"`
	SessionID       modulecore.SessionID        `json:"session_id"`
	Status          conversation.ReadbackStatus `json:"status"`
	Availability    string                      `json:"availability"`
	TaskStartQueued bool                        `json:"task_start_queued"`
	Event           modulecore.EventEnvelope    `json:"event"`
	Message         MessageIndex                `json:"message"`
	ControlTurn     *AcceptedTurn               `json:"control_turn,omitempty"`
}

type storedReadbackDelegation struct {
	revision                 int64
	originTurnID             string
	activeTurnID             string
	activeTriggerKind        string
	activeClientSubmissionID string
	inputMessageID           string
	originActorID            string
	assignee                 string
	instructionMessageID     string
	contextEventID           string
	delegationState          string
	sessionID                string
	turnState                string
	turnDelegationID         sql.NullString
	sessionOwnerID           string
	generationEpoch          int64
}

// AcceptReadback validates the authenticated assignee and the private stored
// HandoffEnvelope, then commits the readback and its state transition
// atomically. An accepted readback queues Task work; a clarification readback
// creates a private Mio control Turn without starting a Task.
func (s *SQLiteStore) AcceptReadback(ctx context.Context, input ReadbackInput) (ReadbackAcceptance, error) {
	if err := s.ensureOpen(); err != nil {
		return ReadbackAcceptance{}, err
	}
	if ctx == nil {
		return ReadbackAcceptance{}, errors.New("readback context is required")
	}
	if err := validateReadbackInput(input); err != nil {
		return ReadbackAcceptance{}, err
	}
	inputHash, err := readbackInputHash(input)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	if s.shouldQueueWrite(ctx) {
		queuedInput := cloneQueuedValue(input)
		return enqueueOrderedWriteResult(s, ctx, 1, queuedInput, func(commandCtx context.Context) (ReadbackAcceptance, error) {
			return s.AcceptReadback(commandCtx, queuedInput)
		})
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return ReadbackAcceptance{}, err
	} else if found {
		if receipt.operationKind != "accept_readback" || receipt.inputSHA256 != inputHash {
			return ReadbackAcceptance{}, ErrOperationConflict
		}
		var acceptance ReadbackAcceptance
		if err := json.Unmarshal([]byte(receipt.resultJSON), &acceptance); err != nil {
			return ReadbackAcceptance{}, fmt.Errorf("decode readback acceptance receipt: %w", err)
		}
		if err := validateStoredReadbackAcceptance(acceptance, input); err != nil {
			return ReadbackAcceptance{}, err
		}
		if err := tx.Commit(); err != nil {
			return ReadbackAcceptance{}, err
		}
		committed = true
		return acceptance, nil
	}

	delegation, err := loadReadbackDelegation(ctx, tx, input.DelegationID, input.Revision)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	if input.Assignee != delegation.assignee {
		return ReadbackAcceptance{}, ErrReadbackAssigneeMismatch
	}
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id = ?`, string(input.DelegationID)).Scan(&currentRevision); err != nil {
		return ReadbackAcceptance{}, err
	}
	if currentRevision != delegation.revision || delegation.delegationState != "dispatch_accepted" ||
		delegation.turnState != "awaiting_readback" || !delegation.turnDelegationID.Valid ||
		delegation.turnDelegationID.String != string(input.DelegationID) {
		return ReadbackAcceptance{}, ErrReadbackConflict
	}

	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return ReadbackAcceptance{}, fmt.Errorf("load private handoff context: %w", err)
	}
	handoff, instructionEvent, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, input)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	trusted := conversation.TrustedReadbackMetadata{
		DelegationID: input.DelegationID, Revision: input.Revision, Sender: input.Assignee,
	}
	if err := conversation.ValidateReadbackEnvelope(input.Readback, handoff, trusted); err != nil {
		return ReadbackAcceptance{}, fmt.Errorf("validate readback envelope: %w", err)
	}

	var controlTurnID modulecore.TurnID
	var event modulecore.EventEnvelope
	if input.Readback.Status == conversation.ReadbackStatusNeedsClarification {
		controlTurnID = modulecore.NewTurnID()
		event = makeClarificationQuestionEvent(input, delegation, handoff, instructionEvent, controlTurnID)
	} else {
		event, err = makeReadbackEvent(input, delegation, handoff, instructionEvent)
		if err != nil {
			return ReadbackAcceptance{}, err
		}
	}
	message := readbackMessageIndex(event, input, delegation, handoff)
	if err := validateMessageIndex(message, event, message.SessionID, message.TurnID); err != nil {
		return ReadbackAcceptance{}, fmt.Errorf("validate readback message index: %w", err)
	}
	if err := validateReplyTarget(ctx, tx, message.ReplyToMessageID, message.SessionID); err != nil {
		return ReadbackAcceptance{}, err
	}
	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return ReadbackAcceptance{}, err
	}
	event, err = insertLiveEvent(ctx, tx, event)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := insertMessageIndex(ctx, tx, message); err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := insertConversationProjectionWork(ctx, tx, event, message); err != nil {
		return ReadbackAcceptance{}, err
	}
	var controlTurn *AcceptedTurn
	if controlTurnID != "" {
		acceptedControlTurn, err := insertClarificationControlTurn(ctx, tx, input, delegation, event, inputHash, controlTurnID)
		if err != nil {
			return ReadbackAcceptance{}, err
		}
		controlTurn = &acceptedControlTurn
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE live_delegation SET state = ?
		WHERE delegation_id = ? AND revision = ? AND state = 'dispatch_accepted'`,
		string(input.Readback.Status), string(input.DelegationID), int64(input.Revision),
	)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return ReadbackAcceptance{}, fmt.Errorf("check readback delegation update: %w", err)
	}
	if updated != 1 {
		return ReadbackAcceptance{}, ErrReadbackConflict
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE live_conversation_turn SET state = 'completed'
		WHERE turn_id = ? AND session_id = ? AND delegation_id = ? AND state = 'awaiting_readback'`,
		delegation.activeTurnID, delegation.sessionID, string(input.DelegationID),
	)
	if err != nil {
		return ReadbackAcceptance{}, err
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return ReadbackAcceptance{}, fmt.Errorf("check readback turn release: %w", err)
	}
	if updated != 1 {
		return ReadbackAcceptance{}, ErrReadbackConflict
	}

	acceptance := ReadbackAcceptance{
		OperationID: input.OperationID, ReceiptID: input.OperationID,
		DelegationID: input.DelegationID, Revision: input.Revision,
		OriginTurnID: modulecore.TurnID(delegation.originTurnID), SessionID: modulecore.SessionID(delegation.sessionID),
		Status: input.Readback.Status, Availability: "free", TaskStartQueued: input.Readback.Status == conversation.ReadbackStatusAccepted,
		Event: event, Message: message, ControlTurn: controlTurn,
	}
	if acceptance.TaskStartQueued {
		if err := insertReadbackTaskStart(ctx, tx, event, message.SessionID, input.DelegationID, input.Revision); err != nil {
			return ReadbackAcceptance{}, err
		}
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "accept_readback", inputHash, acceptance); err != nil {
		return ReadbackAcceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadbackAcceptance{}, err
	}
	committed = true
	s.observeCommittedOrchestratorEvent(acceptance.Event)
	return acceptance, nil
}

func validateReadbackInput(input ReadbackInput) error {
	if strings.TrimSpace(input.OperationID) == "" || input.OperationID != strings.TrimSpace(input.OperationID) {
		return errors.New("operation_id is required and must be trimmed")
	}
	if err := input.DelegationID.Validate(); err != nil {
		return fmt.Errorf("delegation_id: %w", err)
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 {
		return errors.New("readback revision must be positive and fit the stored revision column")
	}
	if strings.TrimSpace(input.Assignee) == "" || input.Assignee != strings.TrimSpace(input.Assignee) {
		return errors.New("authenticated assignee is required and must be trimmed")
	}
	if err := input.EventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if err := input.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if input.OccurredAt.IsZero() {
		return errors.New("readback occurred_at is required")
	}
	return nil
}

func readbackInputHash(input ReadbackInput) (string, error) {
	material := struct {
		DelegationID               modulecore.RequestID        `json:"delegation_id"`
		Revision                   uint64                      `json:"revision"`
		Assignee                   string                      `json:"assignee"`
		ReadbackDelegationID       modulecore.RequestID        `json:"readback_delegation_id"`
		ReadbackRevision           uint64                      `json:"readback_revision"`
		ReadbackSender             string                      `json:"readback_sender"`
		SchemaVersion              string                      `json:"schema_version"`
		Status                     conversation.ReadbackStatus `json:"status"`
		UnderstoodGoal             string                      `json:"understood_goal"`
		AcknowledgedConstraintKeys []string                    `json:"acknowledged_constraint_keys"`
		ExpectedOutput             string                      `json:"expected_output"`
		Text                       string                      `json:"text"`
	}{
		DelegationID: input.DelegationID, Revision: input.Revision, Assignee: input.Assignee,
		ReadbackDelegationID: input.Readback.DelegationID, ReadbackRevision: input.Readback.Revision,
		ReadbackSender: input.Readback.Sender,
		SchemaVersion:  input.Readback.SchemaVersion, Status: input.Readback.Status,
		UnderstoodGoal:             input.Readback.UnderstoodGoal,
		AcknowledgedConstraintKeys: input.Readback.AcknowledgedConstraintKeys,
		ExpectedOutput:             input.Readback.ExpectedOutput, Text: input.Readback.Text,
	}
	return hashJSON(material)
}

func loadReadbackDelegation(ctx context.Context, tx *sql.Tx, delegationID modulecore.RequestID, revision uint64) (storedReadbackDelegation, error) {
	var delegation storedReadbackDelegation
	err := tx.QueryRowContext(ctx, `
		SELECT d.revision, d.origin_turn_id, t.input_message_id, t.actor_id, d.assignee, d.instruction_message_id, d.context_event_id, d.state,
		       t.session_id, t.state, t.delegation_id, s.owner_id, t.generation_epoch
		FROM live_delegation d
		JOIN live_conversation_turn t ON t.turn_id = d.origin_turn_id
		JOIN live_conversation_session s ON s.session_id = t.session_id
		WHERE d.delegation_id = ? AND d.revision = ?`, string(delegationID), int64(revision),
	).Scan(
		&delegation.revision, &delegation.originTurnID, &delegation.inputMessageID, &delegation.originActorID,
		&delegation.assignee, &delegation.instructionMessageID,
		&delegation.contextEventID, &delegation.delegationState, &delegation.sessionID,
		&delegation.turnState, &delegation.turnDelegationID, &delegation.sessionOwnerID, &delegation.generationEpoch,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedReadbackDelegation{}, ErrReadbackConflict
	}
	if err != nil {
		return storedReadbackDelegation{}, err
	}
	delegation.activeTurnID = delegation.originTurnID
	if revision > 1 {
		activeTurnID, activeState, activeDelegationID, actorID, err := loadUserReplyContinuationTurn(ctx, tx, delegation, delegationID, revision-1)
		if err == nil {
			if activeState != "awaiting_readback" && activeState != "completed" ||
				activeDelegationID != string(delegationID) || actorID == "" {
				return storedReadbackDelegation{}, ErrReadbackConflict
			}
			delegation.activeTurnID = activeTurnID
			delegation.turnState = activeState
			delegation.turnDelegationID = sql.NullString{String: activeDelegationID, Valid: true}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return storedReadbackDelegation{}, err
		} else {
			var activeTurnID, activeState, activeDelegationID, actorID string
			var questionFrom, questionToKind, questionToActor, questionKind, questionVisibility string
			var questionEventType, questionComponent string
			err := tx.QueryRowContext(ctx, `
			SELECT t.turn_id,t.state,COALESCE(t.delegation_id,''),t.actor_id,
			       m.from_actor,m.to_kind,m.to_actor,m.kind,m.visibility,e.event_type,e.component_id
			FROM live_conversation_turn t
			JOIN live_message_index m ON m.message_id=t.input_message_id
			JOIN event_envelope e ON e.event_id=m.event_id
			WHERE t.session_id=? AND t.source_turn_id=? AND t.delegation_id=? AND t.trigger_kind='clarification_control'
			  AND t.state IN ('awaiting_readback','completed') AND t.generation_epoch=?
			  AND json_extract(e.envelope_json,'$.payload.delegation_id')=?
			  AND CAST(json_extract(e.envelope_json,'$.payload.delegation_revision') AS INTEGER)=?
			ORDER BY t.admission_seq DESC LIMIT 1`,
				delegation.sessionID, delegation.originTurnID, string(delegationID), delegation.generationEpoch,
				string(delegationID), int64(revision-1),
			).Scan(&activeTurnID, &activeState, &activeDelegationID, &actorID, &questionFrom, &questionToKind,
				&questionToActor, &questionKind, &questionVisibility, &questionEventType, &questionComponent)
			if err != nil || activeState != "awaiting_readback" && activeState != "completed" || activeDelegationID != string(delegationID) ||
				actorID != delegation.assignee || questionFrom != delegation.assignee || questionToKind != "agent" || questionToActor != "mio" ||
				questionKind != "clarification_question" || questionVisibility != "private" ||
				questionEventType != "conversation.clarification_question" || questionComponent != "conversation_runtime" {
				return storedReadbackDelegation{}, ErrReadbackConflict
			}
			delegation.activeTurnID = activeTurnID
			delegation.turnState = activeState
			delegation.turnDelegationID = sql.NullString{String: activeDelegationID, Valid: true}
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT trigger_kind,client_submission_id FROM live_conversation_turn WHERE turn_id=?`,
		delegation.activeTurnID).Scan(&delegation.activeTriggerKind, &delegation.activeClientSubmissionID); err != nil {
		return storedReadbackDelegation{}, err
	}
	if delegation.activeTriggerKind == "clarification_reply" {
		if delegation.activeTurnID == "" || delegation.activeClientSubmissionID == "" {
			return storedReadbackDelegation{}, ErrReadbackConflict
		}
	}
	return delegation, nil
}

// loadUserReplyContinuationTurn recognizes a revision created from an
// authenticated public clarification reply. It validates the persisted reply,
// consumed prompt, and original private question without inventing an Agent
// question turn for the user's answer.
func loadUserReplyContinuationTurn(ctx context.Context, tx *sql.Tx, delegation storedReadbackDelegation, delegationID modulecore.RequestID, previousRevision uint64) (string, string, string, string, error) {
	var turnID, state, storedDelegationID, actorID string
	err := tx.QueryRowContext(ctx, `
		SELECT t.turn_id,t.state,COALESCE(t.delegation_id,''),t.actor_id
		FROM live_conversation_turn t
		JOIN live_clarification_reply_receipt r ON r.reply_turn_id=t.turn_id
		JOIN live_message_index answer ON answer.message_id=r.reply_message_id
		JOIN event_envelope answer_event ON answer_event.event_id=answer.event_id
		JOIN live_message_index prompt ON prompt.message_id=r.prompt_message_id
		JOIN event_envelope prompt_event ON prompt_event.event_id=prompt.event_id
		JOIN live_conversation_turn control ON control.turn_id=prompt.turn_id
		JOIN live_message_index question ON question.message_id=control.input_message_id
		JOIN event_envelope question_event ON question_event.event_id=question.event_id
		JOIN live_delegation prior ON prior.delegation_id=r.delegation_id AND prior.revision=r.revision
		JOIN live_delegation current ON current.delegation_id=r.delegation_id AND current.revision=r.revision+1
		JOIN live_operation_receipt op ON op.operation_id=t.result_receipt_id
		WHERE t.session_id=? AND t.owner_id=? AND t.source_turn_id=? AND t.delegation_id=?
		  AND t.actor_id=answer.from_actor AND t.trigger_kind='clarification_reply'
		  AND t.state IN ('awaiting_readback','completed')
		  AND t.generation_epoch=? AND op.operation_kind='commit_clarification_reply_continuation'
		  AND CAST(json_extract(op.result_json,'$.next_revision') AS INTEGER)=?
		  AND json_extract(op.result_json,'$.owner_id')=t.owner_id
		  AND json_extract(op.result_json,'$.session_id')=t.session_id
		  AND json_extract(op.result_json,'$.reply_turn_id')=t.turn_id
		  AND json_extract(op.result_json,'$.prompt_message_id')=r.prompt_message_id
		  AND json_extract(op.result_json,'$.delegation_id')=r.delegation_id
		  AND json_extract(op.result_json,'$.revision')=r.revision
		  AND json_extract(op.result_json,'$.result_state')='awaiting_readback'
		  AND json_extract(op.result_json,'$.event.message_id')=current.instruction_message_id
		  AND json_extract(op.result_json,'$.context_event.event_id')=current.context_event_id
		  AND json_extract(op.result_json,'$.next_handoff.instruction_message_id')=current.instruction_message_id
		  AND current.state='dispatch_accepted' AND current.origin_turn_id=t.source_turn_id
		  AND current.assignee=prior.assignee
		  AND r.owner_id=? AND r.session_id=? AND r.source_turn_id=? AND r.delegation_id=? AND r.revision=?
		  AND r.client_submission_id=t.client_submission_id AND r.submission_sha256=t.submission_sha256
		  AND prior.origin_turn_id=? AND prior.assignee=? AND prior.state='needs_clarification'
		  AND answer.session_id=? AND answer.turn_id=t.turn_id AND answer.kind='user_message'
		  AND answer.from_actor=t.actor_id AND answer.to_kind='agent' AND answer.to_actor='mio'
		  AND answer.reply_to_message_id=r.prompt_message_id AND answer.visibility='public'
		  AND answer_event.event_type='conversation.user_message' AND answer_event.component_id='orchestrator'
		  AND json_extract(answer_event.envelope_json,'$.payload.from')='user'
		  AND json_extract(answer_event.envelope_json,'$.payload.source_actor_id')=t.actor_id
		  AND json_extract(answer_event.envelope_json,'$.payload.reply_to_message_id')=r.prompt_message_id
		  AND json_extract(answer_event.envelope_json,'$.payload.content')=json_extract(answer_event.envelope_json,'$.payload.text')
		  AND length(trim(json_extract(answer_event.envelope_json,'$.payload.content'))) > 0
		  AND json_extract(answer_event.envelope_json,'$.session_id')=t.session_id
		  AND json_extract(answer_event.envelope_json,'$.turn_id')=t.turn_id
		  AND prompt.session_id=t.session_id AND prompt.from_actor='mio' AND prompt.to_kind='user'
		  AND prompt.to_actor=t.owner_id AND prompt.kind='clarification_prompt' AND prompt.visibility='public'
		  AND prompt_event.event_type='agent.response' AND prompt_event.component_id='orchestrator'
		  AND json_extract(prompt_event.envelope_json,'$.payload.kind')='clarification_prompt'
		  AND json_extract(prompt_event.envelope_json,'$.payload.status')='awaiting_user_clarification'
		  AND json_extract(prompt_event.envelope_json,'$.payload.visibility')='public'
		  AND json_extract(prompt_event.envelope_json,'$.payload.from')='mio'
		  AND json_extract(prompt_event.envelope_json,'$.payload.to')='user'
		  AND json_extract(prompt_event.envelope_json,'$.session_id')=prompt.session_id
		  AND json_extract(prompt_event.envelope_json,'$.turn_id')=prompt.turn_id
		  AND json_extract(prompt_event.envelope_json,'$.payload.delegation_id')=r.delegation_id
		  AND CAST(json_extract(prompt_event.envelope_json,'$.payload.delegation_revision') AS INTEGER)=r.revision
		  AND prompt_event.event_id=prompt.event_id
		  AND prompt_event.event_id IS NOT NULL
		  AND control.session_id=t.session_id AND control.owner_id=t.owner_id
		  AND control.source_turn_id=t.source_turn_id AND control.delegation_id=t.delegation_id
		  AND control.actor_id=prior.assignee AND control.trigger_kind='clarification_control' AND control.state='completed'
		  AND control.generation_epoch=t.generation_epoch
		  AND question.session_id=t.session_id AND question.turn_id=control.turn_id
		  AND question.from_actor=prior.assignee AND question.to_kind='agent' AND question.to_actor='mio'
		  AND question.kind='clarification_question' AND question.visibility='private'
		  AND question_event.event_type='conversation.clarification_question'
		  AND question_event.component_id='conversation_runtime'
		  AND json_extract(question_event.envelope_json,'$.session_id')=question.session_id
		  AND json_extract(question_event.envelope_json,'$.turn_id')=control.turn_id
		  AND json_extract(question_event.envelope_json,'$.payload.delegation_id')=r.delegation_id
		  AND CAST(json_extract(question_event.envelope_json,'$.payload.delegation_revision') AS INTEGER)=r.revision
		  AND prompt_event.envelope_json IS NOT NULL
		  AND json_extract(prompt_event.envelope_json,'$.causation_event_id')=question.event_id
		  AND prompt.reply_to_message_id=question.reply_to_message_id
		ORDER BY t.admission_seq DESC LIMIT 1`,
		delegation.sessionID, delegation.sessionOwnerID, delegation.originTurnID, string(delegationID),
		delegation.generationEpoch, delegation.revision, delegation.sessionOwnerID, delegation.sessionID,
		delegation.originTurnID, string(delegationID), int64(previousRevision), delegation.originTurnID,
		delegation.assignee, delegation.sessionID).Scan(&turnID, &state, &storedDelegationID, &actorID)
	if err != nil {
		return "", "", "", "", err
	}
	return turnID, state, storedDelegationID, actorID, nil
}

func loadEventEnvelopeTx(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID) (modulecore.EventEnvelope, error) {
	var storedEventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
	var eventSeq int64
	err := tx.QueryRowContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope WHERE event_id = ?`, string(eventID),
	).Scan(&storedEventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	return decodeStoredEnvelope(storedEventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
}

func validateStoredHandoff(ctx context.Context, tx *sql.Tx, delegation storedReadbackDelegation, contextEvent modulecore.EventEnvelope, input ReadbackInput) (conversation.HandoffEnvelope, modulecore.EventEnvelope, error) {
	if contextEvent.ComponentID != "conversation_runtime" || contextEvent.EventType != "conversation.delegation_context" ||
		contextEvent.SessionID != modulecore.SessionID(delegation.sessionID) || contextEvent.TurnID != modulecore.TurnID(delegation.originTurnID) {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored handoff context is not private or does not match its origin")
	}
	rawHandoff, found := contextEvent.Payload["handoff"]
	if !found {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored handoff context has no HandoffEnvelope")
	}
	encoded, err := json.Marshal(rawHandoff)
	if err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("encode stored HandoffEnvelope: %w", err)
	}
	var handoff conversation.HandoffEnvelope
	if err := json.Unmarshal(encoded, &handoff); err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("decode stored HandoffEnvelope: %w", err)
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("validate stored HandoffEnvelope: %w", err)
	}
	if handoff.DelegationID != input.DelegationID || handoff.Revision != input.Revision ||
		handoff.OriginTurnID != modulecore.TurnID(delegation.originTurnID) || handoff.SessionID != modulecore.SessionID(delegation.sessionID) ||
		handoff.SourceTraceID != contextEvent.TraceID || handoff.ToActor != delegation.assignee ||
		string(handoff.InstructionMessageID) != delegation.instructionMessageID ||
		handoff.OriginUserMessageID != modulecore.MessageID(delegation.inputMessageID) || handoff.ReplyTo != handoff.OriginUserMessageID {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored HandoffEnvelope does not match the current delegation row")
	}
	var originEventID, originSessionID, originTurnID, originVisibility, originFrom, originKind string
	err = tx.QueryRowContext(ctx, `
		SELECT m.event_id, m.session_id, m.turn_id, m.visibility, m.from_actor, m.kind
		FROM live_message_index m WHERE m.message_id = ?`, delegation.inputMessageID,
	).Scan(&originEventID, &originSessionID, &originTurnID, &originVisibility, &originFrom, &originKind)
	if err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("load origin user message index: %w", err)
	}
	if originVisibility != "public" || originKind != "user_message" || originFrom != delegation.originActorID ||
		originSessionID != delegation.sessionID || originTurnID != delegation.originTurnID ||
		contextEvent.CausationEventID != modulecore.EventID(originEventID) {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored handoff context does not descend from the origin user message")
	}
	if err := validateReplyTarget(ctx, tx, handoff.ReplyTo, modulecore.SessionID(delegation.sessionID)); err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("validate HandoffEnvelope reply target: %w", err)
	}
	var instructionEventID, instructionSessionID, instructionTurnID string
	var fromActor, toKind, toActor, visibility string
	err = tx.QueryRowContext(ctx, `
		SELECT m.event_id, m.session_id, m.turn_id, m.from_actor, m.to_kind, m.to_actor, m.visibility
		FROM live_message_index m WHERE m.message_id = ?`, delegation.instructionMessageID,
	).Scan(&instructionEventID, &instructionSessionID, &instructionTurnID, &fromActor, &toKind, &toActor, &visibility)
	if err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("load private delegation instruction index: %w", err)
	}
	if visibility != "private" || toKind != "agent" || toActor != delegation.assignee ||
		instructionSessionID != delegation.sessionID || instructionTurnID != delegation.originTurnID || fromActor != handoff.FromActor {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored delegation instruction index does not match HandoffEnvelope")
	}
	instructionEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(instructionEventID))
	if err != nil {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("load private delegation instruction event: %w", err)
	}
	if instructionEvent.ComponentID == "orchestrator" || instructionEvent.EventType != "conversation.delegation_instruction" ||
		instructionEvent.SessionID != modulecore.SessionID(delegation.sessionID) ||
		instructionEvent.TurnID != modulecore.TurnID(delegation.originTurnID) ||
		instructionEvent.ActorKind != "agent" || instructionEvent.ActorID != handoff.FromActor ||
		payloadString(instructionEvent.Payload, "from") != handoff.FromActor ||
		payloadString(instructionEvent.Payload, "source_actor_id") != handoff.FromActor ||
		payloadString(instructionEvent.Payload, "to") != delegation.assignee ||
		payloadString(instructionEvent.Payload, "text") != handoff.Instruction {
		return conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, errors.New("stored delegation instruction event is not a matching private event")
	}
	return handoff, instructionEvent, nil
}

func makeReadbackEvent(input ReadbackInput, delegation storedReadbackDelegation, handoff conversation.HandoffEnvelope, instructionEvent modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	readback := input.Readback
	kind, content, err := readbackPublicKindAndContent(readback)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	payload := map[string]any{
		"kind": kind, "schema_version": readback.SchemaVersion, "status": string(readback.Status),
		"understood_goal":              readback.UnderstoodGoal,
		"acknowledged_constraint_keys": append([]string{}, readback.AcknowledgedConstraintKeys...),
		"expected_output":              readback.ExpectedOutput,
		"delegation_id":                string(input.DelegationID), "delegation_revision": input.Revision,
		"availability": "free", "visibility": "public", "from": input.Assignee,
		"source_actor_id": input.Assignee, "to": "user", "content": content, "text": content,
		"reply_to_message_id": string(handoff.ReplyTo),
	}
	if delegation.activeTriggerKind == "clarification_reply" {
		payload["response_to_turn_id"] = delegation.activeTurnID
		payload["response_to_client_submission_id"] = delegation.activeClientSubmissionID
	}
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID,
		TraceID: handoff.SourceTraceID, CausationEventID: instructionEvent.EventID,
		EventType: "agent.response", ComponentID: "orchestrator", OccurredAt: input.OccurredAt.UTC(),
		SessionID: modulecore.SessionID(delegation.sessionID), TurnID: modulecore.TurnID(delegation.originTurnID),
		RequestID: input.DelegationID, MessageID: input.MessageID, Payload: payload,
	}, nil
}

func makeClarificationQuestionEvent(input ReadbackInput, delegation storedReadbackDelegation, handoff conversation.HandoffEnvelope, instructionEvent modulecore.EventEnvelope, controlTurnID modulecore.TurnID) modulecore.EventEnvelope {
	question := input.Readback.Text
	return modulecore.EventEnvelope{
		SchemaVersion:    modulecore.EventEnvelopeSchemaVersion,
		EventID:          input.EventID,
		TraceID:          handoff.SourceTraceID,
		CausationEventID: instructionEvent.EventID,
		EventType:        "conversation.clarification_question",
		ComponentID:      "conversation_runtime",
		OccurredAt:       input.OccurredAt.UTC(),
		SessionID:        modulecore.SessionID(delegation.sessionID),
		TurnID:           controlTurnID,
		RequestID:        input.DelegationID,
		MessageID:        input.MessageID,
		Payload: map[string]any{
			"kind": "clarification_question", "status": string(conversation.ReadbackStatusNeedsClarification),
			"schema_version": input.Readback.SchemaVersion,
			"delegation_id":  string(input.DelegationID), "delegation_revision": input.Revision,
			"visibility": "private", "from": input.Assignee, "source_actor_id": input.Assignee,
			"to": "mio", "content": question, "text": question,
			"reply_to_message_id": string(handoff.ReplyTo),
		},
	}
}

func readbackMessageIndex(event modulecore.EventEnvelope, input ReadbackInput, delegation storedReadbackDelegation, handoff conversation.HandoffEnvelope) MessageIndex {
	if input.Readback.Status == conversation.ReadbackStatusNeedsClarification {
		return MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID,
			SessionID: modulecore.SessionID(delegation.sessionID), TurnID: event.TurnID,
			FromActor: input.Assignee, ToKind: "agent", ToActor: "mio",
			Kind: "clarification_question", ReplyToMessageID: handoff.ReplyTo, Visibility: "private",
		}
	}
	return MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID,
		SessionID: modulecore.SessionID(delegation.sessionID), TurnID: modulecore.TurnID(delegation.originTurnID),
		FromActor: input.Assignee, ToKind: "user", ToActor: delegation.sessionOwnerID,
		Kind: payloadString(event.Payload, "kind"), ReplyToMessageID: handoff.ReplyTo, Visibility: "public",
	}
}

func insertClarificationControlTurn(ctx context.Context, tx *sql.Tx, input ReadbackInput, delegation storedReadbackDelegation, event modulecore.EventEnvelope, inputHash string, turnID modulecore.TurnID) (AcceptedTurn, error) {
	clientSubmissionID := clarificationControlSubmissionID(input.DelegationID, input.Revision)
	if err := turnID.Validate(); err != nil {
		return AcceptedTurn{}, fmt.Errorf("clarification control turn_id: %w", err)
	}
	if event.EventSeq <= 0 || event.TurnID != turnID || event.MessageID != input.MessageID ||
		payloadString(event.Payload, "delegation_id") != string(input.DelegationID) ||
		payloadUint64Field(event.Payload, "delegation_revision") != input.Revision {
		return AcceptedTurn{}, errors.New("clarification control input event does not match its trusted turn binding")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO live_conversation_turn
			(turn_id, session_id, owner_id, actor_id, trigger_kind, client_submission_id, submission_sha256,
			 accept_receipt_id, input_message_id, admission_seq, state, generation_epoch, source_turn_id, delegation_id)
		VALUES (?, ?, ?, ?, 'clarification_control', ?, ?, ?, ?, ?, 'accepted', ?, ?, ?)`,
		string(turnID), delegation.sessionID, delegation.sessionOwnerID, input.Assignee,
		clientSubmissionID, inputHash, input.OperationID, string(input.MessageID), int64(event.EventSeq),
		delegation.generationEpoch, delegation.originTurnID, string(input.DelegationID),
	)
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("persist accepted clarification control turn: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_session SET admission_seq = ?
		WHERE session_id = ? AND owner_id = ? AND runtime_version = 'v2' AND admission_seq < ?`,
		int64(event.EventSeq), delegation.sessionID, delegation.sessionOwnerID,
		int64(event.EventSeq),
	)
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("advance clarification session admission boundary: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("check clarification session admission boundary: %w", err)
	}
	if updated != 1 {
		return AcceptedTurn{}, ErrReadbackConflict
	}
	return AcceptedTurn{
		OperationID: input.OperationID, ClientSubmissionID: clientSubmissionID,
		SessionID: modulecore.SessionID(delegation.sessionID), TurnID: turnID,
		ActorID: input.Assignee, TriggerKind: "clarification_control",
		InputMessageID: input.MessageID, AdmissionSeq: event.EventSeq,
		GenerationEpoch: delegation.generationEpoch, State: "accepted", InputEvent: event,
	}, nil
}

func clarificationControlSubmissionID(delegationID modulecore.RequestID, revision uint64) string {
	return "clarification:" + string(delegationID) + ":" + strconv.FormatUint(revision, 10)
}

func readbackPublicKindAndContent(readback conversation.ReadbackEnvelope) (string, string, error) {
	switch readback.Status {
	case conversation.ReadbackStatusRejected:
		return "handoff_error", "The assigned agent rejected this handoff. " + readback.Text, nil
	case conversation.ReadbackStatusAccepted:
		return "readback", readback.Text, nil
	default:
		return "", "", errors.New("clarification readback cannot be represented as a public readback event")
	}
}

func insertReadbackTaskStart(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, sessionID modulecore.SessionID, delegationID modulecore.RequestID, revision uint64) error {
	workID := readbackStartWorkID(delegationID, revision)
	dedupeKey := readbackStartDedupeKey(delegationID, revision)
	if err := validateOutboxSource(ctx, tx, event.EventID, string(event.TraceID), sessionID); err != nil {
		return fmt.Errorf("validate delegated task start source: %w", err)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO live_work_outbox
			(work_id, dedupe_key, kind, source_event_id, session_id, priority, state, attempt, not_before, version)
		VALUES (?, ?, 'start_delegated_task', ?, ?, 5, 'pending', 0, '', 1)`,
		workID, dedupeKey, string(event.EventID), string(sessionID),
	)
	return err
}

func readbackStartWorkID(delegationID modulecore.RequestID, revision uint64) string {
	return "start:" + string(delegationID) + ":" + strconv.FormatUint(revision, 10)
}

func readbackStartDedupeKey(delegationID modulecore.RequestID, revision uint64) string {
	return "delegation:" + string(delegationID) + ":" + strconv.FormatUint(revision, 10) + ":start_task"
}

func validateStoredReadbackAcceptance(acceptance ReadbackAcceptance, input ReadbackInput) error {
	if acceptance.OperationID != input.OperationID || acceptance.ReceiptID != input.OperationID ||
		acceptance.DelegationID != input.DelegationID || acceptance.Revision != input.Revision ||
		acceptance.Status != input.Readback.Status || acceptance.Availability != "free" ||
		acceptance.Event.EventID == "" || acceptance.Event.EventSeq <= 0 || acceptance.Event.RequestID != input.DelegationID ||
		acceptance.Message.EventID != acceptance.Event.EventID || acceptance.Message.MessageID != acceptance.Event.MessageID {
		return errors.New("stored readback acceptance receipt is inconsistent")
	}
	if input.Readback.Status == conversation.ReadbackStatusNeedsClarification {
		controlTurn := acceptance.ControlTurn
		if controlTurn == nil || controlTurn.TurnID == "" || controlTurn.TurnID == acceptance.OriginTurnID ||
			controlTurn.OperationID != input.OperationID ||
			controlTurn.SessionID != acceptance.SessionID || controlTurn.ActorID != input.Assignee ||
			controlTurn.TriggerKind != "clarification_control" || controlTurn.ClientSubmissionID != clarificationControlSubmissionID(input.DelegationID, input.Revision) ||
			controlTurn.InputMessageID != acceptance.Event.MessageID || controlTurn.AdmissionSeq != acceptance.Event.EventSeq ||
			controlTurn.InputEvent.EventID != acceptance.Event.EventID ||
			controlTurn.InputEvent.TurnID != controlTurn.TurnID || controlTurn.InputEvent.SessionID != acceptance.SessionID ||
			controlTurn.State != "accepted" || acceptance.Event.EventType != "conversation.clarification_question" ||
			acceptance.Event.CausationEventID == "" || acceptance.Event.ActorKind != "" || acceptance.Event.ActorID != "" ||
			payloadString(acceptance.Event.Payload, "kind") != "clarification_question" ||
			payloadString(acceptance.Event.Payload, "status") != string(conversation.ReadbackStatusNeedsClarification) ||
			payloadString(acceptance.Event.Payload, "delegation_id") != string(input.DelegationID) ||
			payloadUint64Field(acceptance.Event.Payload, "delegation_revision") != input.Revision ||
			payloadString(acceptance.Event.Payload, "visibility") != "private" ||
			payloadString(acceptance.Event.Payload, "from") != input.Assignee || payloadString(acceptance.Event.Payload, "to") != "mio" ||
			payloadString(acceptance.Event.Payload, "text") != input.Readback.Text || acceptance.TaskStartQueued ||
			acceptance.Message.Visibility != "private" || acceptance.Message.ToKind != "agent" || acceptance.Message.ToActor != "mio" ||
			acceptance.Message.FromActor != input.Assignee || acceptance.Message.Kind != "clarification_question" ||
			acceptance.Message.TurnID != controlTurn.TurnID || acceptance.Message.ReplyToMessageID == "" {
			return errors.New("stored clarification control turn receipt is inconsistent")
		}
	} else if acceptance.ControlTurn != nil {
		return errors.New("non-clarification readback receipt carries a clarification control turn")
	} else if acceptance.TaskStartQueued != (input.Readback.Status == conversation.ReadbackStatusAccepted) {
		return errors.New("stored readback Task state is inconsistent")
	}
	if err := modulecore.ValidateEventEnvelope(acceptance.Event); err != nil {
		return fmt.Errorf("stored readback acceptance event: %w", err)
	}
	return nil
}
